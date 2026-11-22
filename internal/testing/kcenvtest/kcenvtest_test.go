package kcenvtest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func envMap(kv ...string) func(string) (string, bool) {
	m := make(map[string]string)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestModeFromEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		env     []string
		want    Mode
		wantErr string
	}{
		{name: "unset", want: Auto},
		{name: "empty values", env: []string{RequireEnv, "", SkipEnv, ""}, want: Auto},
		{name: "require", env: []string{RequireEnv, "1"}, want: Required},
		{name: "require true", env: []string{RequireEnv, "true"}, want: Required},
		{name: "require off", env: []string{RequireEnv, "0"}, want: Auto},
		{name: "skip", env: []string{SkipEnv, "1"}, want: Skipped},
		{name: "both", env: []string{RequireEnv, "1", SkipEnv, "1"}, wantErr: "both set"},
		{name: "require garbage", env: []string{RequireEnv, "yes please"}, wantErr: RequireEnv},
		{name: "skip garbage", env: []string{SkipEnv, "maybe"}, wantErr: SkipEnv},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ModeFromEnv(envMap(tt.env...))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("mode = %v, want %v", got, tt.want)
			}
		})
	}
}

// assetsDir returns a directory holding executable etcd and kube-apiserver
// stand-ins, so the preflight passes and the fake control plane decides.
func assetsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"etcd", "kube-apiserver"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheckAssets(t *testing.T) {
	t.Parallel()
	good := assetsDir(t)
	noExec := t.TempDir()
	for _, name := range []string{"etcd", "kube-apiserver"} {
		if err := os.WriteFile(filepath.Join(noExec, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name        string
		env         []string
		wantMissing bool
		wantReport  []string
	}{
		{name: "present", env: []string{assetsEnv, good}, wantReport: []string{"etcd: " + filepath.Join(good, "etcd") + " ok"}},
		{name: "absent dir", env: []string{assetsEnv, "/nonexistent/kc"}, wantMissing: true, wantReport: []string{"kube-apiserver: /nonexistent/kc/kube-apiserver missing"}},
		{name: "not executable", env: []string{assetsEnv, noExec}, wantMissing: true, wantReport: []string{"not executable"}},
		{name: "override wins", env: []string{assetsEnv, "/nonexistent/kc", "TEST_ASSET_ETCD", filepath.Join(good, "etcd"), "TEST_ASSET_KUBE_APISERVER", filepath.Join(good, "kube-apiserver")}},
		// The default directory is not expected on a developer machine or CI
		// runner; the report must say the variable is unset.
		{name: "unset", wantReport: []string{assetsEnv + " unset", defaultAssetsDir}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			report, missing := checkAssets(envMap(tt.env...))
			if tt.name != "unset" && missing != tt.wantMissing {
				t.Fatalf("missing = %v, want %v (report: %s)", missing, tt.wantMissing, report)
			}
			for _, want := range tt.wantReport {
				if !strings.Contains(report, want) {
					t.Fatalf("report %q does not contain %q", report, want)
				}
			}
		})
	}
}

type fakePlane struct {
	cfg      *rest.Config
	err      error
	started  int
	stopped  int
	stopErrs error
}

func (f *fakePlane) Start() (*rest.Config, error) {
	f.started++
	return f.cfg, f.err
}

func (f *fakePlane) Stop() error {
	f.stopped++
	return f.stopErrs
}

type fakeRunner struct{ runs int }

func (r *fakeRunner) Run() int {
	r.runs++
	return 0
}

func TestRunMain(t *testing.T) {
	// Not parallel: runMain writes the package-level unavailable reason.
	assets := assetsDir(t)
	ok := &rest.Config{Host: "https://127.0.0.1:1"}
	tests := []struct {
		name        string
		env         []string
		plane       *fakePlane
		wantCode    int
		wantRuns    int
		wantStarts  int
		wantStops   int
		wantCfg     bool
		wantStderr  string
		wantNoInErr string
	}{
		{name: "auto started", env: []string{assetsEnv, assets}, plane: &fakePlane{cfg: ok}, wantRuns: 1, wantStarts: 1, wantStops: 1, wantCfg: true},
		{name: "auto failure runs tests", env: []string{assetsEnv, assets}, plane: &fakePlane{err: errors.New("etcd exited")}, wantRuns: 1, wantStarts: 1, wantStderr: "etcd exited"},
		{name: "required started", env: []string{RequireEnv, "1", assetsEnv, assets}, plane: &fakePlane{cfg: ok}, wantRuns: 1, wantStarts: 1, wantStops: 1, wantCfg: true},
		{name: "required failure", env: []string{RequireEnv, "1", assetsEnv, assets}, plane: &fakePlane{err: errors.New("etcd exited")}, wantCode: 1, wantStarts: 1, wantStderr: "etcd exited"},
		{name: "required nil config", env: []string{RequireEnv, "1", assetsEnv, assets}, plane: &fakePlane{}, wantCode: 1, wantStarts: 1, wantStderr: "no config"},
		{name: "required missing binaries", env: []string{RequireEnv, "1", assetsEnv, "/nonexistent/kc"}, plane: &fakePlane{cfg: ok}, wantCode: 1, wantStderr: "/nonexistent/kc/etcd missing"},
		{name: "skip", env: []string{SkipEnv, "1"}, plane: &fakePlane{cfg: ok}, wantRuns: 1},
		{name: "conflict", env: []string{SkipEnv, "1", RequireEnv, "1"}, plane: &fakePlane{cfg: ok}, wantCode: 2, wantStderr: "both set"},
		{name: "stop error keeps code", env: []string{assetsEnv, assets}, plane: &fakePlane{cfg: ok, stopErrs: errors.New("boom")}, wantRuns: 1, wantStarts: 1, wantStops: 1, wantCfg: true, wantStderr: "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			var cfg *rest.Config
			r := &fakeRunner{}
			code := runMain(r, tt.plane, envMap(tt.env...), &stderr, &cfg)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if r.runs != tt.wantRuns {
				t.Errorf("runs = %d, want %d", r.runs, tt.wantRuns)
			}
			if tt.plane.started != tt.wantStarts || tt.plane.stopped != tt.wantStops {
				t.Errorf("starts/stops = %d/%d, want %d/%d", tt.plane.started, tt.plane.stopped, tt.wantStarts, tt.wantStops)
			}
			if (cfg != nil) != tt.wantCfg {
				t.Errorf("cfg set = %v, want %v", cfg != nil, tt.wantCfg)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr %q does not contain %q", stderr.String(), tt.wantStderr)
			}
			if tt.wantStderr == "" && stderr.Len() != 0 {
				t.Errorf("unexpected stderr %q", stderr.String())
			}
		})
	}
}

// recorder captures the outcome of startForTest. testing.TB cannot be
// implemented outside the testing package, so it embeds a real one and
// overrides the methods startForTest calls.
type recorder struct {
	testing.TB
	fatal    string
	skip     string
	cleanups []func()
}

func (r *recorder) Helper()                        {}
func (r *recorder) Fatalf(format string, a ...any) { r.fatal = fmt.Sprintf(format, a...) }
func (r *recorder) Skip(a ...any)                  { r.skip = fmt.Sprint(a...) }
func (r *recorder) Skipf(format string, a ...any)  { r.skip = fmt.Sprintf(format, a...) }
func (r *recorder) Errorf(format string, a ...any) { r.fatal = fmt.Sprintf(format, a...) }
func (r *recorder) Cleanup(f func())               { r.cleanups = append(r.cleanups, f) }

func TestStartForTest(t *testing.T) {
	t.Parallel()
	assets := assetsDir(t)
	ok := &rest.Config{Host: "https://127.0.0.1:1"}
	tests := []struct {
		name      string
		env       []string
		plane     *fakePlane
		wantCfg   bool
		wantFatal string
		wantSkip  string
	}{
		{name: "auto started", env: []string{assetsEnv, assets}, plane: &fakePlane{cfg: ok}, wantCfg: true},
		{name: "auto failure skips", env: []string{assetsEnv, assets}, plane: &fakePlane{err: errors.New("etcd exited")}, wantSkip: "etcd exited"},
		{name: "required failure fails", env: []string{RequireEnv, "1", assetsEnv, assets}, plane: &fakePlane{err: errors.New("etcd exited")}, wantFatal: "etcd exited"},
		{name: "required missing fails", env: []string{RequireEnv, "1", assetsEnv, "/nonexistent/kc"}, plane: &fakePlane{cfg: ok}, wantFatal: "missing"},
		{name: "skip mode skips", env: []string{SkipEnv, "1"}, plane: &fakePlane{cfg: ok}, wantSkip: SkipEnv},
		{name: "conflict fails", env: []string{SkipEnv, "1", RequireEnv, "1"}, plane: &fakePlane{cfg: ok}, wantFatal: "both set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &recorder{TB: t}
			cfg := startForTest(r, tt.plane, envMap(tt.env...))
			if (cfg != nil) != tt.wantCfg {
				t.Errorf("cfg set = %v, want %v", cfg != nil, tt.wantCfg)
			}
			if !strings.Contains(r.fatal, tt.wantFatal) || (tt.wantFatal == "") != (r.fatal == "") {
				t.Errorf("fatal = %q, want %q", r.fatal, tt.wantFatal)
			}
			if !strings.Contains(r.skip, tt.wantSkip) || (tt.wantSkip == "") != (r.skip == "") {
				t.Errorf("skip = %q, want %q", r.skip, tt.wantSkip)
			}
			for _, f := range r.cleanups {
				f()
			}
			if tt.wantCfg && tt.plane.stopped != 1 {
				t.Errorf("stopped = %d after cleanup, want 1", tt.plane.stopped)
			}
		})
	}
}

const childEnv = "KCENVTEST_CHILD"

// TestChildStart is the body of TestRequiredInvalidAssetsFails. It only does
// something in the child process.
func TestChildStart(t *testing.T) {
	if os.Getenv(childEnv) == "" {
		t.Skip("child process only")
	}
	Start(t)
}

// TestRequiredInvalidAssetsFails runs the real envtest path in a child test
// binary with an asset directory that does not exist and checks that
// required mode turns that into a failing exit status that names the
// missing binary. In auto mode the same child must pass by skipping.
func TestRequiredInvalidAssetsFails(t *testing.T) {
	t.Parallel()
	run := func(extra ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestChildStart$", "-test.v")
		cmd.Env = append(os.Environ(), childEnv+"=1", assetsEnv+"=/nonexistent/kc-envtest", RequireEnv+"=", SkipEnv+"=")
		cmd.Env = append(cmd.Env, extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run(RequireEnv + "=1")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("required mode: err = %v, want a nonzero exit; output:\n%s", err, out)
	}
	for _, want := range []string{"--- FAIL: TestChildStart", "/nonexistent/kc-envtest/kube-apiserver missing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("required mode output does not contain %q:\n%s", want, out)
		}
	}

	out, err = run()
	if err != nil {
		t.Fatalf("auto mode: err = %v, want success; output:\n%s", err, out)
	}
	if !strings.Contains(out, "--- SKIP: TestChildStart") {
		t.Fatalf("auto mode did not skip:\n%s", out)
	}
}
