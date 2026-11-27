package promhistory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stokaro/kc/internal/promhistory"
)

// blockingRead counts its runs and returns v once release is closed, or
// the context's error.
func blockingRead(runs *atomic.Int32, release <-chan struct{}, v any) func(context.Context) (any, error) {
	return func(ctx context.Context) (any, error) {
		runs.Add(1)
		select {
		case <-release:
			return v, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func TestSharedJoinsIdenticalReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := promhistory.NewShared(2, 8)
		release := make(chan struct{})
		var runs atomic.Int32
		read := blockingRead(&runs, release, "v")
		var wg sync.WaitGroup
		results := make([]any, 3)
		for i := range results {
			wg.Go(func() {
				v, err := s.Do(t.Context(), "k", time.Minute, read)
				if err != nil {
					t.Errorf("Do: %v", err)
				}
				results[i] = v
			})
		}
		synctest.Wait()
		if runs.Load() != 1 {
			t.Fatalf("%d reads for three identical calls, want 1", runs.Load())
		}
		close(release)
		wg.Wait()
		for i, v := range results {
			if v != "v" {
				t.Errorf("result %d = %v", i, v)
			}
		}
		// A result within its TTL is reused without a read; after it, the
		// next call reads again.
		if v, err := s.Do(t.Context(), "k", time.Minute, read); err != nil || v != "v" || runs.Load() != 1 {
			t.Errorf("cached Do = %v, %v after %d reads", v, err, runs.Load())
		}
		time.Sleep(time.Minute)
		if _, err := s.Do(t.Context(), "k", time.Minute, read); err != nil || runs.Load() != 2 {
			t.Errorf("expired Do: %v after %d reads, want a second read", err, runs.Load())
		}
		// Another key is another read.
		if _, err := s.Do(t.Context(), "other", time.Minute, read); err != nil || runs.Load() != 3 {
			t.Errorf("other key: %v after %d reads", err, runs.Load())
		}
	})
}

func TestSharedCancelsWhenEveryCallerLeft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := promhistory.NewShared(1, 8)
		var runs atomic.Int32
		never := make(chan struct{})
		read := blockingRead(&runs, never, nil)
		ctx1, cancel1 := context.WithCancel(t.Context())
		ctx2, cancel2 := context.WithCancel(t.Context())
		errs := make(chan error, 2)
		go func() { _, err := s.Do(ctx1, "k", time.Minute, read); errs <- err }()
		go func() { _, err := s.Do(ctx2, "k", time.Minute, read); errs <- err }()
		synctest.Wait()
		cancel1()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("first caller: %v", err)
		}
		synctest.Wait()
		// The second caller still waits, and so does the read.
		select {
		case err := <-errs:
			t.Fatalf("second caller returned %v while the read ran", err)
		default:
		}
		cancel2()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("second caller: %v, want context cancellation", err)
		}
		synctest.Wait()
		// The read saw its context end: a new call reads again, the
		// canceled result was not kept.
		release := make(chan struct{})
		close(release)
		if v, err := s.Do(t.Context(), "k", time.Minute, blockingRead(&runs, release, 1)); err != nil || v != 1 || runs.Load() != 2 {
			t.Errorf("after a canceled read: %v, %v, %d reads", v, err, runs.Load())
		}
	})
}

func TestSharedKeepsTheFirstCallersDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := promhistory.NewShared(1, 8)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		_, err := s.Do(ctx, "k", time.Minute, func(ctx context.Context) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the deadline", err)
		}
	})
}

func TestSharedBoundsConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := promhistory.NewShared(2, 8)
		var runs atomic.Int32
		release := make(chan struct{})
		var wg sync.WaitGroup
		for i := range 5 {
			wg.Go(func() {
				_, _ = s.Do(t.Context(), fmt.Sprint(i), 0, blockingRead(&runs, release, nil))
			})
		}
		synctest.Wait()
		if runs.Load() != 2 {
			t.Fatalf("%d reads running, want the bound of 2", runs.Load())
		}
		close(release)
		wg.Wait()
		if runs.Load() != 5 {
			t.Errorf("%d reads in all, want 5", runs.Load())
		}
	})
}

func TestSharedBoundsItsResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := promhistory.NewShared(1, 2)
		var runs atomic.Int32
		release := make(chan struct{})
		close(release)
		read := blockingRead(&runs, release, 1)
		for _, k := range []string{"a", "b", "c", "a"} {
			if _, err := s.Do(t.Context(), k, time.Hour, read); err != nil {
				t.Fatal(err)
			}
		}
		// "a" was dropped when "c" came in: four reads, not three.
		if runs.Load() != 4 {
			t.Errorf("%d reads, want 4", runs.Load())
		}
	})
}
