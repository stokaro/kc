package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stokaro/kc"
	apimachineryversion "k8s.io/apimachinery/pkg/version"
)

type fakeDiscovery struct {
	version *apimachineryversion.Info
	err     error
}

func (f fakeDiscovery) ServerVersion() (*apimachineryversion.Info, error) {
	return f.version, f.err
}

func TestCollectVersionInfoIncludesServer(t *testing.T) {
	ctx := t.Context()
	server := &apimachineryversion.Info{GitVersion: "v1.2.3"}
	expectedClient := kc.ClientGoVersion()

	info, err := collectVersionInfo(ctx, fakeDiscovery{version: server})
	if err != nil {
		t.Fatalf("collectVersionInfo returned error: %v", err)
	}
	if info.serverVersion != server {
		t.Fatalf("expected server version %v, got %v", server, info.serverVersion)
	}
	if info.clientGo != expectedClient {
		t.Fatalf("expected clientGo version %s, got %s", expectedClient, info.clientGo)
	}
}

func TestCollectVersionInfoPropagatesError(t *testing.T) {
	ctx := t.Context()
	_, err := collectVersionInfo(ctx, fakeDiscovery{err: errors.New("boom")})
	if err == nil {
		t.Fatalf("expected error from collectVersionInfo, got nil")
	}
}

func TestPrintVersionInfo(t *testing.T) {
	origVersion := version
	origCommit := commit
	origDate := date
	version = "v0.0.0-test"
	commit = "abcdef"
	date = "2024-01-01T00:00:00Z"
	t.Cleanup(func() {
		version = origVersion
		commit = origCommit
		date = origDate
	})

	expectedClientGo := kc.ClientGoVersion()
	info, err := collectVersionInfo(t.Context(), fakeDiscovery{
		version: &apimachineryversion.Info{GitVersion: "v1.2.3"},
	})
	if err != nil {
		t.Fatalf("collectVersionInfo returned error: %v", err)
	}

	var buf bytes.Buffer
	printVersionInfo(&buf, info)

	output := buf.String()
	expect := []string{
		"kc Version: v0.0.0-test",
		"Client Version (client-go): " + expectedClientGo,
		"Server Version: v1.2.3",
		"Commit: abcdef",
		"Date: 2024-01-01T00:00:00Z",
	}
	for _, want := range expect {
		if !strings.Contains(output, want) {
			t.Fatalf("expected output to contain %q, got %q", want, output)
		}
	}
}

func TestResolveBuildVersion(t *testing.T) {
	t.Parallel()
	vcs := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123abcd"},
		{Key: "vcs.time", Value: "2026-01-02T03:04:05Z"},
	}
	tests := []struct {
		name                string
		v, c, d             string
		bi                  *debug.BuildInfo
		wantV, wantC, wantD string
	}{
		{
			name: "no build info keeps defaults",
			v:    "dev", c: "unknown", d: "unknown",
			wantV: "dev", wantC: "unknown", wantD: "unknown",
		},
		{
			name: "go install fills module version",
			v:    "dev", c: "unknown", d: "unknown",
			bi:    &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}},
			wantV: "v1.2.3", wantC: "unknown", wantD: "unknown",
		},
		{
			name: "local build without module version keeps dev",
			v:    "dev", c: "unknown", d: "unknown",
			bi:    &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs},
			wantV: "dev", wantC: "0123abcd", wantD: "2026-01-02T03:04:05Z",
		},
		{
			name: "ldflags win over build info",
			v:    "v9.9.9", c: "feedface", d: "2025-05-05T00:00:00Z",
			bi:    &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}, Settings: vcs},
			wantV: "v9.9.9", wantC: "feedface", wantD: "2025-05-05T00:00:00Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, c, d := resolveBuildVersion(tc.v, tc.c, tc.d, tc.bi)
			if v != tc.wantV || c != tc.wantC || d != tc.wantD {
				t.Fatalf("got (%q, %q, %q), want (%q, %q, %q)", v, c, d, tc.wantV, tc.wantC, tc.wantD)
			}
		})
	}
}

// errExit stops kong after a hook asked it to exit, so the test can check the
// exit code without ending the test process.
type errExit struct{ code int }

func TestVersionFlagPrintsClientVersionAndExits(t *testing.T) {
	var out bytes.Buffer
	var cli cliFlags
	opts := append(cliOptions(),
		kong.Writers(&out, &out),
		kong.Exit(func(code int) { panic(errExit{code: code}) }),
	)
	parser, err := kong.New(&cli, opts...)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	code := -1
	func() {
		defer func() {
			if r := recover(); r != nil {
				e, ok := r.(errExit)
				if !ok {
					panic(r)
				}
				code = e.code
			}
		}()
		_, _ = parser.Parse([]string{"--version"})
	}()

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output %q", code, out.String())
	}
	for _, want := range []string{"kc Version: ", "Client Version (client-go): " + kc.ClientGoVersion()} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q does not contain %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "Server Version") {
		t.Fatalf("--version must not print a server version, got %q", out.String())
	}
}

func TestRunVersionCommandClientOnly(t *testing.T) {
	// A kubeconfig path that does not exist: --client must not read it.
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var stdout, stderr bytes.Buffer
	if err := runVersionCommand(t.Context(), &stdout, &stderr, true); err != nil {
		t.Fatalf("runVersionCommand(clientOnly) error: %v", err)
	}
	if !strings.Contains(stdout.String(), "kc Version: ") {
		t.Fatalf("stdout %q has no kc version", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// runMainEnv makes the test binary run main() instead of the tests, so the
// tests below exercise the real command line end to end, as the Homebrew
// formula test does, without building a second binary.
const runMainEnv = "KC_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		os.Args = append([]string{"kc"}, os.Args[1:]...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// offlineEnv returns an environment with an empty home directory, a
// kubeconfig path that does not exist and no in-cluster service variables.
func offlineEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{
		runMainEnv + "=1",
		"HOME=" + home,
		"KUBECONFIG=" + filepath.Join(home, "missing-kubeconfig"),
	}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch {
		case key == "HOME", key == "KUBECONFIG", key == runMainEnv, strings.HasPrefix(key, "KUBERNETES_"):
			continue
		}
		env = append(env, kv)
	}
	return env
}

func TestVersionWithoutClusterAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{name: "version flag", args: []string{"--version"}, wantCode: 0, wantOut: "kc Version: "},
		{name: "version client", args: []string{"version", "--client"}, wantCode: 0, wantOut: "kc Version: "},
		{name: "version needs a cluster", args: []string{"version"}, wantCode: 1, wantOut: "kc Version: "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.CommandContext(t.Context(), os.Args[0], tc.args...)
			cmd.Env = offlineEnv(t)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			code := 0
			if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("running %v: %v", tc.args, err)
			}
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d; stdout %q, stderr %q", code, tc.wantCode, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.wantOut) {
				t.Fatalf("stdout %q does not contain %q", stdout.String(), tc.wantOut)
			}
			if tc.wantCode == 0 && strings.Contains(stdout.String(), "Server Version") {
				t.Fatalf("client-only output has a server version: %q", stdout.String())
			}
		})
	}
}
