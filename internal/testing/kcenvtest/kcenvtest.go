// Package kcenvtest starts envtest control planes for kc tests and decides
// what happens when one cannot start.
//
// Two environment variables select the mode (see docs/adr/0060-require-envtest-in-ci.md):
//
//   - KC_REQUIRE_ENVTEST=1: envtest must start. A startup failure fails the
//     package (from TestMain) or the test (from Start). CI sets this.
//   - KC_SKIP_ENVTEST=1: envtest is not started; tests that need it skip.
//   - neither: envtest is tried; if it cannot start, tests that need it skip.
//
// Setting both is an error.
//
// Every control plane started here is tuned for a loaded machine (see
// docs/adr/0244-envtest-under-load.md): longer start and stop timeouts, an
// API server that does not wait for open watches when it stops, and at most
// MaxPerTestControlPlanes per-test control planes running at once in one
// test binary.
package kcenvtest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	// RequireEnv names the variable that makes envtest mandatory.
	RequireEnv = "KC_REQUIRE_ENVTEST"
	// SkipEnv names the variable that turns envtest off.
	SkipEnv = "KC_SKIP_ENVTEST"

	// assetsEnv and defaultAssetsDir mirror controller-runtime's lookup in
	// pkg/internal/testing/process/bin_path_finder.go.
	assetsEnv        = "KUBEBUILDER_ASSETS"
	defaultAssetsDir = "/usr/local/kubebuilder/bin"

	// startTimeoutEnv and stopTimeoutEnv are envtest's own overrides
	// (pkg/envtest/server.go). When one is set, envtest reads it and Tune
	// leaves that timeout alone.
	startTimeoutEnv = "KUBEBUILDER_CONTROLPLANE_START_TIMEOUT"
	stopTimeoutEnv  = "KUBEBUILDER_CONTROLPLANE_STOP_TIMEOUT"

	// StartTimeout bounds each start attempt of etcd and kube-apiserver.
	// envtest's default of 20s is too short when a full parallel suite
	// boots a dozen control planes at once. Start returns as soon as the
	// process is ready, so the bound costs nothing on a quiet machine.
	StartTimeout = 90 * time.Second
	// StopTimeout bounds the wait after SIGTERM before envtest kills a
	// process and reports a timeout.
	StopTimeout = 60 * time.Second
	// WatchDrainGrace is passed to kube-apiserver as
	// --shutdown-watch-termination-grace-period. Without it the server
	// waits for open watches until its own 60s shutdown timeout, longer
	// than envtest's 20s stop timeout, so an informer still running at stop
	// time made Stop fail with "timeout waiting for process kube-apiserver
	// to stop". With it, a stop with live watches takes well under a second.
	WatchDrainGrace = "1s"

	// MaxPerTestControlPlanes caps the control planes Start runs at the
	// same time in one test binary. A test that called Start more often
	// than this would wait for itself, so no test may.
	MaxPerTestControlPlanes = 2
)

// perTestSlots holds one token per running control plane started by Start.
var perTestSlots = make(chan struct{}, MaxPerTestControlPlanes)

// Mode says whether envtest must, may or must not start.
type Mode int

const (
	// Auto tries envtest and skips dependent tests when it cannot start.
	Auto Mode = iota
	// Required fails when envtest cannot start.
	Required
	// Skipped does not start envtest and skips dependent tests.
	Skipped
)

func (m Mode) String() string {
	switch m {
	case Required:
		return "required"
	case Skipped:
		return "skipped"
	default:
		return "auto"
	}
}

// ModeFromEnv reads the mode from the environment through lookup.
// A value is parsed with strconv.ParseBool; an empty value counts as unset.
func ModeFromEnv(lookup func(string) (string, bool)) (Mode, error) {
	require, err := boolEnv(lookup, RequireEnv)
	if err != nil {
		return Auto, err
	}
	skip, err := boolEnv(lookup, SkipEnv)
	if err != nil {
		return Auto, err
	}
	switch {
	case require && skip:
		return Auto, fmt.Errorf("%s and %s are both set; choose one", RequireEnv, SkipEnv)
	case require:
		return Required, nil
	case skip:
		return Skipped, nil
	default:
		return Auto, nil
	}
}

func boolEnv(lookup func(string) (string, bool), name string) (bool, error) {
	v, ok := lookup(name)
	if !ok || v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q: want a boolean such as 1 or 0", name, v)
	}
	return b, nil
}

// controlPlane is the part of envtest.Environment this package uses.
type controlPlane interface {
	Start() (*rest.Config, error)
	Stop() error
}

// unavailable holds why the package-level control plane is missing, for skip
// messages. It is written once in Main before any test runs.
var unavailable string

// Main starts one envtest control plane for a package, stores its config in
// *cfg, runs the tests and stops the control plane. It returns the exit code
// for os.Exit. In Required mode a startup failure returns 1 without running
// any test. In Auto mode tests run with *cfg nil and Require skips them.
func Main(m *testing.M, cfg **rest.Config) int {
	return runMain(m, Tune(&envtest.Environment{}, os.LookupEnv), os.LookupEnv, os.Stderr, cfg)
}

// Tune applies the settings for loaded machines to env and returns it.
// envtest's own timeout variables, read through lookup, and fields already
// set on env take precedence.
func Tune(env *envtest.Environment, lookup func(string) (string, bool)) *envtest.Environment {
	if _, ok := lookup(startTimeoutEnv); !ok && env.ControlPlaneStartTimeout == 0 {
		env.ControlPlaneStartTimeout = StartTimeout
	}
	if _, ok := lookup(stopTimeoutEnv); !ok && env.ControlPlaneStopTimeout == 0 {
		env.ControlPlaneStopTimeout = StopTimeout
	}
	const drainFlag = "shutdown-watch-termination-grace-period"
	args := env.ControlPlane.GetAPIServer().Configure()
	if args.Get(drainFlag).Get(nil) == nil {
		args.Set(drainFlag, WatchDrainGrace)
	}
	return env
}

func runMain(m interface{ Run() int }, env controlPlane, lookup func(string) (string, bool), stderr io.Writer, cfg **rest.Config) int {
	mode, err := ModeFromEnv(lookup)
	if err != nil {
		fmt.Fprintf(stderr, "kcenvtest: %v\n", err)
		return 2
	}
	if mode == Skipped {
		unavailable = SkipEnv + " is set"
		return m.Run()
	}
	c, err := start(env, lookup)
	if err != nil {
		if mode == Required {
			fmt.Fprintf(stderr, "kcenvtest: %s is set and envtest cannot start: %v\n", RequireEnv, err)
			return 1
		}
		unavailable = fmt.Sprintf("envtest cannot start (set %s=1 to fail instead): %v", RequireEnv, err)
		fmt.Fprintf(stderr, "kcenvtest: %s\n", unavailable)
		return m.Run()
	}
	*cfg = c
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(stderr, "kcenvtest: stop envtest: %v\n", err)
	}
	return code
}

// Require skips t when the package-level control plane from Main is not
// running. It fails t instead in Required mode, which only happens when the
// package has no TestMain calling Main.
func Require(t testing.TB, cfg *rest.Config) {
	t.Helper()
	if cfg != nil {
		return
	}
	if mode, err := ModeFromEnv(os.LookupEnv); err == nil && mode == Required {
		t.Fatalf("%s is set but no envtest control plane is running; does the package TestMain call kcenvtest.Main?", RequireEnv)
	}
	reason := unavailable
	if reason == "" {
		reason = "envtest not started"
	}
	t.Skip(reason)
}

// Start starts a control plane for one test and stops it in t.Cleanup.
// In Required mode a startup failure fails t; otherwise it skips t.
//
// Start waits while MaxPerTestControlPlanes control planes from other tests
// of the binary are running. Prefer the package control plane from Main;
// start one per test only when the test changes cluster-wide state, such as
// the served API types, that other tests must not see.
func Start(t testing.TB) *rest.Config {
	t.Helper()
	return StartEnv(t, &envtest.Environment{})
}

// StartEnv is Start for a caller that configures env itself, for example
// with CRDs to install.
func StartEnv(t testing.TB, env *envtest.Environment) *rest.Config {
	t.Helper()
	return startForTest(t, Tune(env, os.LookupEnv), os.LookupEnv, perTestSlots)
}

func startForTest(t testing.TB, env controlPlane, lookup func(string) (string, bool), slots chan struct{}) *rest.Config {
	t.Helper()
	mode, err := ModeFromEnv(lookup)
	if err != nil {
		t.Fatalf("kcenvtest: %v", err)
		return nil
	}
	if mode == Skipped {
		t.Skip(SkipEnv + " is set")
		return nil
	}
	slots <- struct{}{}
	cfg, err := start(env, lookup)
	if err != nil {
		<-slots
		if mode == Required {
			t.Fatalf("%s is set and envtest cannot start: %v", RequireEnv, err)
			return nil
		}
		t.Skipf("envtest cannot start (set %s=1 to fail instead): %v", RequireEnv, err)
		return nil
	}
	t.Cleanup(func() {
		defer func() { <-slots }()
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	return cfg
}

// start checks the binaries before starting so a missing asset directory
// reports which files are absent instead of a bare exec error.
func start(env controlPlane, lookup func(string) (string, bool)) (*rest.Config, error) {
	report, missing := checkAssets(lookup)
	if missing {
		return nil, fmt.Errorf("required binaries are missing (%s)", report)
	}
	for attempt := 1; ; attempt++ {
		cfg, err := env.Start()
		if err == nil {
			if cfg == nil {
				_ = env.Stop()
				return nil, errors.New("start control plane: no config returned")
			}
			return cfg, nil
		}
		// envtest.Environment.Start leaves etcd and kube-apiserver running
		// when a step after starting them fails.
		_ = env.Stop()
		if attempt == startAttempts || !strings.Contains(err.Error(), defaultNamespaceErr) {
			return nil, fmt.Errorf("start control plane (%s): %w", report, err)
		}
	}
}

const (
	// defaultNamespaceErr is how envtest reports that the "default"
	// namespace did not appear within a fixed 5s after kube-apiserver
	// became ready (waitForDefaultNamespace in pkg/envtest/server.go,
	// controller-runtime v0.25.3). The wait is not configurable and fails
	// on a loaded machine while the server itself is fine, so start
	// tries again with a fresh control plane.
	defaultNamespaceErr = "default namespace didn't register within deadline"
	startAttempts       = 3
)

// checkAssets resolves etcd and kube-apiserver the way envtest does and
// describes each one. missing is true when either is absent or not
// executable.
func checkAssets(lookup func(string) (string, bool)) (report string, missing bool) {
	dir, ok := lookup(assetsEnv)
	var parts []string
	if ok {
		parts = append(parts, fmt.Sprintf("%s=%q", assetsEnv, dir))
	} else {
		dir = defaultAssetsDir
		parts = append(parts, fmt.Sprintf("%s unset, using %s; run: export %s=\"$(setup-envtest use -p path 1.37.x)\"", assetsEnv, dir, assetsEnv))
	}
	for _, name := range []string{"etcd", "kube-apiserver"} {
		path := filepath.Join(dir, name)
		if p, ok := lookup("TEST_ASSET_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); ok {
			path = p
		}
		state := "ok"
		switch fi, err := os.Stat(path); {
		case err != nil:
			state = "missing"
			missing = true
		case fi.IsDir() || fi.Mode().Perm()&0o111 == 0:
			state = "not executable"
			missing = true
		}
		parts = append(parts, fmt.Sprintf("%s: %s %s", name, path, state))
	}
	return strings.Join(parts, "; "), missing
}
