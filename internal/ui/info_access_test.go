package ui

// White-box: gatedInfo and its access check are unexported; the Info
// widget sees them only through the Summarizer interface.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
	infowidget "github.com/stokaro/kc/internal/ui/panelcontent/info"
)

type fakeSummarizer struct {
	closed atomic.Bool
}

func (f *fakeSummarizer) Summary(models.InfoFocus) models.InfoSummary {
	return models.InfoSummary{Node: "engine"}
}

func (f *fakeSummarizer) Close() { f.closed.Store(true) }

func TestGatedInfo(t *testing.T) {
	nodes := schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	tests := []struct {
		name       string
		check      func(context.Context, schema.GroupVersionResource) (bool, error)
		wantEngine bool
	}{
		{name: "allowed", check: func(context.Context, schema.GroupVersionResource) (bool, error) { return true, nil }, wantEngine: true},
		{name: "nodes denied", check: func(_ context.Context, gvr schema.GroupVersionResource) (bool, error) { return gvr != nodes, nil }},
		{name: "pods denied", check: func(_ context.Context, gvr schema.GroupVersionResource) (bool, error) { return gvr == nodes, nil }},
		{name: "review fails", check: func(context.Context, schema.GroupVersionResource) (bool, error) { return false, errors.New("timeout") }, wantEngine: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var opened atomic.Int32
				inner := &fakeSummarizer{}
				g := newGatedInfo(t.Context(), tc.check, func() infowidget.Summarizer {
					opened.Add(1)
					return inner
				})
				synctest.Wait()
				s := g.Summary(models.InfoFocus{})
				if tc.wantEngine {
					if s.Node != "engine" || opened.Load() != 1 {
						t.Fatalf("summary = %+v, opened %d; want the engine's", s, opened.Load())
					}
				} else if s.Err != infoNotPermitted || opened.Load() != 0 {
					t.Fatalf("summary = %+v, opened %d; want %q and no engine", s, opened.Load(), infoNotPermitted)
				}
				g.Summary(models.InfoFocus{})
				g.Close()
				if opened.Load() > 1 {
					t.Fatalf("engine opened %d times", opened.Load())
				}
				if tc.wantEngine && !inner.closed.Load() {
					t.Fatal("Close did not stop the engine")
				}
			})
		})
	}
}

// TestGatedInfoLoadsUntilAnswered: Summary does not wait for the reviews.
func TestGatedInfoLoadsUntilAnswered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		g := newGatedInfo(t.Context(), func(ctx context.Context, _ schema.GroupVersionResource) (bool, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return true, nil
		}, func() infowidget.Summarizer { return &fakeSummarizer{} })
		if s := g.Summary(models.InfoFocus{}); !s.Loading {
			t.Fatalf("summary before the answer = %+v, want Loading", s)
		}
		close(release)
		synctest.Wait()
		if s := g.Summary(models.InfoFocus{}); s.Node != "engine" {
			t.Fatalf("summary after the answer = %+v", s)
		}
		g.Close()
	})
}
