package kctesting_test

import (
	"context"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	kctesting "github.com/stokaro/kc/internal/testing"
)

func TestUniqueName(t *testing.T) {
	t.Parallel()
	label := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	tests := []struct {
		name string
		base string
	}{
		{name: "short", base: "helm-releases"},
		{name: "too long", base: strings.Repeat("n", 80)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, b := kctesting.UniqueName(tt.base), kctesting.UniqueName(tt.base)
			if a == b {
				t.Fatalf("two calls returned the same name %q", a)
			}
			for _, n := range []string{a, b} {
				if len(n) > 63 || !label.MatchString(n) {
					t.Fatalf("%q is not a DNS label", n)
				}
				if !strings.HasPrefix(n, tt.base[:min(len(tt.base), 57)]+"-") {
					t.Fatalf("%q does not start with the base", n)
				}
			}
		})
	}
}

func TestRunInBackgroundWaitsForReturn(t *testing.T) {
	t.Parallel()
	var started, returned atomic.Bool
	t.Run("inner", func(t *testing.T) {
		kctesting.RunInBackground(t, "loop", func(ctx context.Context) error {
			started.Store(true)
			<-ctx.Done()
			returned.Store(true)
			return ctx.Err()
		})
		kctesting.Eventually(t, 5*time.Second, 10*time.Millisecond, started.Load, "start never ran")
		if returned.Load() {
			t.Fatal("start returned before the test ended")
		}
	})
	if !returned.Load() {
		t.Fatal("cleanup finished while start was still running")
	}
}
