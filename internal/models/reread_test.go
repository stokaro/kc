package models

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// These tests are white-box: they count the loads and watch starts of the
// unexported live row source, which the folder API does not show.

// countingLiveFolder is a folder over a live source whose loads and watch
// starts are counted.
func countingLiveFolder(t *testing.T, populate func(context.Context) ([]table.Row, error)) (f *BaseFolder, src *liveObjectRowSource, watches *atomic.Int32) {
	t.Helper()
	watches = new(atomic.Int32)
	f = NewBaseFolder(Deps{Ctx: t.Context()}, []table.Column{{Title: " Name"}}, nil)
	src = newLiveObjectRowSourceWithHooks(populate, f.markDirtyFromSource, func(func(), func()) (func(), error) {
		watches.Add(1)
		return func() {}, nil
	})
	src.useBackground(Deps{Ctx: t.Context()})
	f.SetRowSource(src)
	t.Cleanup(src.Close)
	return f, src, watches
}

// settledWatches waits until no background watch start of src runs and
// returns the number of watch starts so far.
func settledWatches(t *testing.T, src *liveObjectRowSource, watches *atomic.Int32) int32 {
	t.Helper()
	kctesting.Eventually(t, 5*time.Second, time.Millisecond, func() bool {
		src.mu.Lock()
		defer src.mu.Unlock()
		return !src.watchStarting
	}, "a background watch start did not end")
	return watches.Load()
}

func TestRereadLiveFolderLoadsAtOnceOnTheRunningWatch(t *testing.T) {
	var loads atomic.Int32
	f, src, watches := countingLiveFolder(t, func(context.Context) ([]table.Row, error) {
		loads.Add(1)
		return oneRow("a"), nil
	})
	ctx := t.Context()
	if n := f.Len(ctx); n != 1 {
		t.Fatalf("Len = %d, want 1", n)
	}
	settledWatches(t, src, watches)

	// Within listRefreshInterval of the first load a refresh waits; a
	// reread loads at once.
	f.Reread(ctx)
	f.Len(ctx)
	if n := loads.Load(); n != 2 {
		t.Fatalf("loads = %d after the reread, want 2", n)
	}
	if n := settledWatches(t, src, watches); n != 1 {
		t.Fatalf("watch starts = %d, want 1: a reread reads the shared cache and starts no watch", n)
	}
}

func TestRereadRetriesListAfterForbidden(t *testing.T) {
	var loads atomic.Int32
	var allowed atomic.Bool
	gr := schema.GroupResource{Resource: "secrets"}
	f, src, watches := countingLiveFolder(t, func(context.Context) ([]table.Row, error) {
		loads.Add(1)
		if !allowed.Load() {
			return nil, apierrors.NewForbidden(gr, "", nil)
		}
		return oneRow("s"), nil
	})
	ctx := t.Context()
	f.Len(ctx)
	settledWatches(t, src, watches)
	src.mu.Lock()
	blocked := src.listBlocked
	src.mu.Unlock()
	if !blocked {
		t.Fatal("a forbidden list did not block the source")
	}
	f.Refresh()
	f.Len(ctx)
	if n := loads.Load(); n != 1 {
		t.Fatalf("loads = %d, want 1: a refresh does not retry a forbidden list", n)
	}

	// The permission was granted; C-r tries again.
	allowed.Store(true)
	f.Reread(ctx)
	if n := f.Len(ctx); n != 1 {
		t.Fatalf("Len = %d after the reread, want 1", n)
	}
	if n := settledWatches(t, src, watches); n != 2 {
		t.Fatalf("watch starts = %d, want 2: the watch given up after forbidden starts again", n)
	}
}

func TestRereadClosedSourceStaysClosed(t *testing.T) {
	f, src, watches := countingLiveFolder(t, func(context.Context) ([]table.Row, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", nil)
	})
	f.Len(t.Context())
	settledWatches(t, src, watches)
	src.Close()
	f.Reread(t.Context())
	f.Len(t.Context())
	if n := settledWatches(t, src, watches); n != 1 {
		t.Fatalf("watch starts = %d, want 1: a closed source must not watch again", n)
	}
}

func TestRereadLocalDirListsNewFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewLocalDirFolder(Deps{Ctx: t.Context()}, dir)
	ctx := t.Context()
	before := f.Len(ctx)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := f.Len(ctx); n != before {
		t.Fatalf("Len = %d before the reread, want %d: the directory is read once", n, before)
	}
	f.Reread(ctx)
	if n := f.Len(ctx); n != before+1 {
		t.Fatalf("Len = %d after the reread, want %d", n, before+1)
	}
}

func TestRereadOverviewAsksForRecompute(t *testing.T) {
	e := &overviewEngine{listeners: make(map[int]func()), now: time.Now}
	f := &OverviewFolder{BaseFolder: NewBaseFolder(Deps{Ctx: t.Context()}, nil, nil), engine: e}
	f.Reread(t.Context())
	if !e.dirty.Load() {
		t.Fatal("a reread of the overview did not ask the engine to recompute")
	}
	e.dirty.Store(false)
	(&OverviewInfo{engine: e}).Reread()
	if !e.dirty.Load() {
		t.Fatal("a reread of Info did not ask the engine to recompute")
	}
}
