package kctesting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
)

type backgroundRecorder struct {
	testing.TB
	cleanup  func()
	failures []string
}

func (r *backgroundRecorder) Cleanup(cleanup func()) { r.cleanup = cleanup }

func (r *backgroundRecorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func TestRunInBackgroundReportsErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		start func(context.Context) error
		want  string
	}{
		{
			name:  "clean early return",
			start: func(context.Context) error { return nil },
		},
		{
			name:  "startup failure survives cleanup cancellation",
			start: func(context.Context) error { return errors.New("cache refused to start") },
			want:  "cache refused to start",
		},
		{
			name:  "early cancellation error is unexpected",
			start: func(context.Context) error { return fmt.Errorf("startup: %w", context.Canceled) },
			want:  "startup: context canceled",
		},
		{
			name: "cleanup cancellation is expected",
			start: func(ctx context.Context) error {
				<-ctx.Done()
				return fmt.Errorf("cache stopped: %w", ctx.Err())
			},
		},
		{
			name: "unrelated shutdown failure is unexpected",
			start: func(ctx context.Context) error {
				<-ctx.Done()
				return errors.New("failed flushing cache")
			},
			want: "failed flushing cache",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				recorder := &backgroundRecorder{TB: t}
				RunInBackground(recorder, "cache", tt.start)
				// Finish an immediate return before cleanup cancels the context.
				// A worker waiting on ctx.Done stays blocked until cleanup.
				synctest.Wait()
				recorder.cleanup()
				if tt.want == "" {
					if len(recorder.failures) != 0 {
						t.Fatalf("unexpected failures: %v", recorder.failures)
					}
					return
				}
				if len(recorder.failures) != 1 || !strings.Contains(recorder.failures[0], tt.want) {
					t.Fatalf("failures = %v, want one containing %q", recorder.failures, tt.want)
				}
			})
		})
	}
}
