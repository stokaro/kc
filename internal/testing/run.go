package kctesting

import (
	"context"
	"testing"
	"time"
)

// stopWait bounds how long a cleanup waits for a background start function
// to return after its context was canceled.
const stopWait = 30 * time.Second

// RunInBackground calls start in a goroutine with a context derived from
// t.Context. A cleanup cancels the context and waits until start returns,
// so informers started by it have closed their watches before cleanups
// registered earlier run, such as the one that stops a per-test envtest
// control plane. start returning before the cleanup is not an error; what
// it returns is ignored, as in the code it stands in for.
func RunInBackground(t testing.TB, name string, start func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(stopWait):
			t.Errorf("%s still running %v after its context was canceled", name, stopWait)
		}
	})
}
