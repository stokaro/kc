package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// A context kc cannot start in stops kc before the UI. The error is one
// plain line on stderr, and nothing reaches stdout, where the UI draws:
// before, the unknown --context error followed the escape sequences of
// the UI's exit cleanup, although the UI never started.
func TestStartupContextErrorsStopBeforeUI(t *testing.T) {
	t.Parallel()
	contexts := []string{"good", "refused", "badtoken", "limited"}
	tests := []struct {
		name     string
		args     []string
		current  string
		contexts []string
		// want is stderr, with %[1]s for the kubeconfig path.
		want string
	}{
		{
			name:     "current-context not defined",
			current:  "typo-context",
			contexts: contexts,
			want:     `Error: current-context "typo-context" in %[1]s is not defined; contexts: badtoken, good, limited, refused; pick one with --context`,
		},
		{
			name:     "current-context not defined, kc get",
			args:     []string{"get", "pods"},
			current:  "typo-context",
			contexts: contexts,
			want:     `Error: current-context "typo-context" in %[1]s is not defined; contexts: badtoken, good, limited, refused; pick one with --context`,
		},
		{
			name:     "no current-context",
			contexts: contexts,
			want:     `Error: no current-context is set in %[1]s; contexts: badtoken, good, limited, refused; pick one with --context`,
		},
		{
			name:     "unknown --context",
			args:     []string{"--context", "nope"},
			current:  "good",
			contexts: contexts,
			want:     `Error: unknown context "nope" in %[1]s; contexts: badtoken, good, limited, refused`,
		},
		{
			name:     "terminal control in a context name",
			current:  "typo",
			contexts: []string{"good", "evil\x1b]0;title\a\x1b[2J"},
			want:     `Error: current-context "typo" in %[1]s is not defined; contexts: "evil\x1b]0;title\a\x1b[2J", good; pick one with --context`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeTestKubeconfig(t, tc.current, tc.contexts)
			// The servers are unreachable; kc must stop before it dials.
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			// #nosec G204 G702 -- the test runs its own test binary.
			cmd := exec.CommandContext(ctx, os.Args[0], tc.args...)
			// The later KUBECONFIG wins over the one offlineEnv sets.
			cmd.Env = append(offlineEnv(t), "KUBECONFIG="+path)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("kc %v did not stop within 30s, so it started the UI; stdout %q, stderr %q", tc.args, stdout.String(), stderr.String())
			}
			code := 0
			if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("running %v: %v", tc.args, err)
			}
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout %q, want nothing", stdout.String())
			}
			if want := fmt.Sprintf(tc.want, path) + "\n"; stderr.String() != want {
				t.Fatalf("stderr %q,\nwant   %q", stderr.String(), want)
			}
			if strings.ContainsRune(stderr.String(), '\x1b') {
				t.Fatalf("stderr %q holds an escape character", stderr.String())
			}
		})
	}
}

// writeTestKubeconfig writes a kubeconfig whose contexts all name one
// unreachable server, and returns its path.
func writeTestKubeconfig(t *testing.T, current string, contexts []string) string {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	cfg.CurrentContext = current
	cfg.Clusters["c"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1"}
	cfg.AuthInfos["u"] = &clientcmdapi.AuthInfo{Token: "t"}
	for _, name := range contexts {
		cfg.Contexts[name] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "u"}
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	return path
}
