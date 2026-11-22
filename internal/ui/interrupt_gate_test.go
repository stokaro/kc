package ui

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestInterruptGateSwallowsWhileHeldAndShortlyAfter(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	g := &interruptGate{now: func() time.Time { return now }}
	if g.swallow() {
		t.Fatal("a fresh gate swallows SIGINT")
	}
	releaseA := g.hold()
	releaseB := g.hold()
	if !g.swallow() {
		t.Fatal("a held gate lets SIGINT through")
	}
	releaseA()
	releaseA() // a second call must not release the other holder
	if !g.swallow() {
		t.Fatal("the gate opened while a second holder still runs")
	}
	releaseB()
	if !g.swallow() {
		t.Fatal("the C-c that ended the child must still be swallowed right after it ended")
	}
	now = now.Add(interruptGrace)
	if g.swallow() {
		t.Fatal("SIGINT is still swallowed after the grace period")
	}
}

func TestForwardQuitSignals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		held     bool
		sig      os.Signal
		wantQuit bool
	}{
		{name: "SIGINT quits when no child owns the terminal", sig: syscall.SIGINT, wantQuit: true},
		{name: "SIGINT is the child's while it owns the terminal", held: true, sig: syscall.SIGINT},
		{name: "SIGTERM always quits", held: true, sig: syscall.SIGTERM, wantQuit: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var g interruptGate
			if tc.held {
				t.Cleanup(g.hold())
			}
			sigs := make(chan os.Signal, 1)
			quit := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				forwardQuitSignals(sigs, &g, func() { close(quit) })
			}()
			sigs <- tc.sig
			close(sigs)
			<-done
			select {
			case <-quit:
				if !tc.wantQuit {
					t.Fatal("quit was called")
				}
			default:
				if tc.wantQuit {
					t.Fatal("quit was not called")
				}
			}
		})
	}
}
