//go:build unix

package ui

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/models"
)

// An attach without stdin runs kubectl in the cooked terminal, where C-c
// sends SIGINT to kc as well. The fake kubectl plays the tty: it sends
// SIGINT to its parent, the test binary, which handles SIGINT the way Run
// does.
func TestExecSessionKeepsKcAliveOnChildSIGINT(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nkill -INT $PPID\nsleep 0.2\nexit 130\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil { // #nosec G306 -- the fake kubectl must be executable.
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tests := []struct {
		name     string
		gated    bool
		wantQuit bool
	}{
		{name: "without the gate the SIGINT quits kc", wantQuit: true},
		{name: "with the gate the SIGINT stays with kubectl", gated: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gate interruptGate
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT)
			quit := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				forwardQuitSignals(sigs, &gate, func() { close(quit) })
			}()
			t.Cleanup(func() {
				signal.Stop(sigs)
				close(sigs)
				<-done
			})

			target := models.ExecTarget{Namespace: "demo", Pod: "bashpod", Container: "bash"}
			session := newExecSession(target, models.ExecAttach)
			if tc.gated {
				session.interrupts = &gate
			}
			var stdout strings.Builder
			session.SetStdout(&stdout)
			session.SetStderr(&strings.Builder{})
			session.SetStdin(strings.NewReader(""))
			if err := session.Run(); err == nil {
				t.Fatal("Run: want the fake kubectl's exit error")
			}
			if !strings.Contains(stdout.String(), "C-c leaves") {
				t.Fatalf("banner = %q, want the C-c hint", stdout.String())
			}

			// The grace period of the gate is longer than this wait.
			select {
			case <-quit:
				if !tc.wantQuit {
					t.Fatal("kc quit on the SIGINT meant for kubectl")
				}
			case <-time.After(300 * time.Millisecond):
				if tc.wantQuit {
					t.Fatal("the SIGINT did not reach the quit handler")
				}
			}
		})
	}
}
