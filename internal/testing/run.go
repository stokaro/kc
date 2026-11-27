package kctesting

import (
	"context"
	"errors"
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
// control plane. Unexpected errors fail the test, including errors returned
// before cleanup. An error wrapping this context's cancellation is expected
// only if the context was already canceled when start returned.
func RunInBackground(t testing.TB, name string, start func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		err := start(ctx)
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			err = nil
		}
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s returned an unexpected error: %v", name, err)
			}
		case <-time.After(stopWait):
			t.Errorf("%s still running %v after its context was canceled", name, stopWait)
		}
	})
}
