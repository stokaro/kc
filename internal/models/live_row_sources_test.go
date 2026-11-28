package models

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	schema "k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// waitLines waits until src serves want rows, reading as a repaint does.
func waitLines(t *testing.T, src rowSource, want int, what string) {
	t.Helper()
	kctesting.Eventually(t, 5*time.Second, 20*time.Millisecond, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()
		return len(src.Lines(ctx, 0, 10)) == want
	}, what)
}

// The rows of a ConfigMap's or Secret's keys load in the background and
// load again after a change marks them dirty.
func TestNewLiveKeyRowSourceRefresh(t *testing.T) {
	t.Parallel()
	rowsSet := [][]table.Row{
		{
			NewSimpleItem("cm/key1", []string{"key1"}, []string{"cm", "key1"}, WhiteStyle()),
			NewSimpleItem("cm/key2", []string{"key2"}, []string{"cm", "key2"}, WhiteStyle()),
		},
		{
			NewSimpleItem("cm/key3", []string{"key3"}, []string{"cm", "key3"}, WhiteStyle()),
		},
	}

	var idx, populateCalls, dirtyCalls atomic.Int32
	src := newLiveKeyRowSource(
		Deps{Ctx: t.Context()},
		schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
		"default",
		"example",
		func(context.Context) ([]table.Row, error) {
			populateCalls.Add(1)
			return rowsSet[idx.Load()], nil
		},
		func() { dirtyCalls.Add(1) },
	)
	t.Cleanup(src.Close)

	if first := src.Lines(t.Context(), 0, 10); len(first) != len(rowsSet[0]) {
		t.Fatalf("expected %d rows, got %d", len(rowsSet[0]), len(first))
	}
	if n := populateCalls.Load(); n != 1 {
		t.Fatalf("expected one populate call, got %d", n)
	}

	idx.Store(1)
	before := dirtyCalls.Load()
	src.MarkDirty()
	if dirtyCalls.Load() == before {
		t.Fatal("MarkDirty did not tell the folder")
	}
	waitLines(t, src, len(rowsSet[1]), "the key rows did not load again after MarkDirty")
	if n := populateCalls.Load(); n != 2 {
		t.Fatalf("expected populate to refresh once, got %d calls", n)
	}
}

// The rows of a pod's sections and of a container list load in the
// background and load again after a change marks them dirty.
func TestPodRowSourcesRefresh(t *testing.T) {
	t.Parallel()
	rowsSet := [][]table.Row{
		{
			NewSimpleItem("section/containers", []string{"/containers"}, []string{"pod", "containers"}, WhiteStyle()),
			NewSimpleItem("section/init", []string{"/init"}, []string{"pod", "init"}, WhiteStyle()),
		},
		{
			NewSimpleItem("section/containers", []string{"/containers"}, []string{"pod", "containers"}, WhiteStyle()),
		},
	}
	deps := Deps{Ctx: t.Context()}
	tests := []struct {
		name  string
		build func(populate func(context.Context) ([]table.Row, error)) *liveObjectRowSource
	}{
		{"pod sections", func(populate func(context.Context) ([]table.Row, error)) *liveObjectRowSource {
			return newPodSectionRowSource(deps, "default", "pod", populate, func() {})
		}},
		{"container list", func(populate func(context.Context) ([]table.Row, error)) *liveObjectRowSource {
			return newPodContainerRowSource(deps, "default", "pod", containerKindPrimary, populate, func() {})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var idx atomic.Int32
			src := tt.build(func(context.Context) ([]table.Row, error) { return rowsSet[idx.Load()], nil })
			t.Cleanup(src.Close)
			if got := len(src.Lines(t.Context(), 0, 10)); got != len(rowsSet[0]) {
				t.Fatalf("%d rows, want %d", got, len(rowsSet[0]))
			}
			idx.Store(1)
			src.MarkDirty()
			waitLines(t, src, len(rowsSet[1]), "the rows did not load again after MarkDirty")
		})
	}
}

func TestLiveObjectRowSourceStopsListingAfterForbidden(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	var populateCalls int
	src := newLiveObjectRowSourceWithHooks(
		func(context.Context) ([]table.Row, error) {
			populateCalls++
			return nil, apierrors.NewForbidden(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, "", errors.New("denied"))
		},
		func() {},
		nil,
	)
	src.setTarget(liveSourceTarget{gvr: gvr, namespace: "default"})

	ctx := t.Context()
	if rows := src.Lines(ctx, 0, 5); rows != nil {
		t.Fatalf("expected no rows on forbidden list, got %d", len(rows))
	}
	if populateCalls != 1 {
		t.Fatalf("expected one populate attempt, got %d", populateCalls)
	}

	src.MarkDirty()
	if rows := src.Lines(ctx, 0, 5); rows != nil {
		t.Fatalf("expected no rows after forbidden list retry, got %d", len(rows))
	}
	if populateCalls != 1 {
		t.Fatalf("expected forbidden list to suppress retries, got %d calls", populateCalls)
	}
}

func TestLiveObjectRowSourceStopsWatchRetriesAfterForbidden(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	var populateCalls int
	var watchCalls int
	src := newLiveObjectRowSourceWithHooks(
		func(context.Context) ([]table.Row, error) {
			populateCalls++
			return make([]table.Row, 0), nil
		},
		func() {},
		func(func(), func()) (func(), error) {
			watchCalls++
			return nil, apierrors.NewForbidden(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, "", errors.New("no watch"))
		},
	)
	src.setTarget(liveSourceTarget{gvr: gvr, namespace: "default"})

	ctx := t.Context()
	_ = src.Lines(ctx, 0, 5)
	if watchCalls != 1 {
		t.Fatalf("expected one watch attempt, got %d", watchCalls)
	}
	if populateCalls != 1 {
		t.Fatalf("expected populate to proceed once despite watch forbidden, got %d", populateCalls)
	}

	src.MarkDirty()
	_ = src.Lines(ctx, 0, 5)
	if watchCalls != 1 {
		t.Fatalf("expected watch retries to be suppressed, got %d attempts", watchCalls)
	}
	if populateCalls != 2 {
		t.Fatalf("expected refresh to skip watch and repopulate, got %d populate calls", populateCalls)
	}
}
