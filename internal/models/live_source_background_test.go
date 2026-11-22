package models

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	table "github.com/stokaro/kc/internal/table"
)

func backgroundSource(t *testing.T, populate func(context.Context) ([]table.Row, error), onDirty func()) *liveObjectRowSource {
	t.Helper()
	src := newLiveObjectRowSourceWithHooks(populate, onDirty, nil)
	src.useBackground(Deps{Ctx: t.Context()})
	return src
}

func oneRow(id string) []table.Row {
	return []table.Row{NewSimpleItem(id, []string{id}, []string{id}, WhiteStyle())}
}

// A slow list must not hold the UI past its deadline; the rows appear once
// the load finishes and the folder is told to repaint.
func TestLiveSourceSlowLoadDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	notified := make(chan struct{}, 4)
	src := backgroundSource(t, func(context.Context) ([]table.Row, error) {
		<-release
		return oneRow("a"), nil
	}, func() { notified <- struct{}{} })

	ui, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if n := src.Len(ui); n != 0 {
		t.Fatalf("Len = %d before the load finished", n)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Len blocked for %v", d)
	}
	close(release)
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatalf("the folder was never told the rows arrived")
	}
	if n := src.Len(t.Context()); n != 1 {
		t.Fatalf("Len = %d after the load", n)
	}
}

// A burst of events reloads the list at most once per interval.
func TestLiveSourceCoalescesEvents(t *testing.T) {
	old := listRefreshInterval
	listRefreshInterval = 200 * time.Millisecond
	t.Cleanup(func() { listRefreshInterval = old })

	var loads atomic.Int32
	var mu sync.Mutex
	dirty := 0
	src := backgroundSource(t, func(context.Context) ([]table.Row, error) {
		loads.Add(1)
		return oneRow("a"), nil
	}, func() { mu.Lock(); dirty++; mu.Unlock() })

	src.Len(t.Context())
	for range 50 {
		src.MarkDirty()
		src.Len(t.Context())
	}
	if n := loads.Load(); n != 1 {
		t.Fatalf("%d loads inside one interval, want 1", n)
	}
	// After the interval the pending change is loaded once.
	deadline := time.Now().Add(5 * time.Second)
	for loads.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		src.Len(t.Context())
	}
	if n := loads.Load(); n != 2 {
		t.Fatalf("%d loads after the interval, want 2", n)
	}
}

func TestLiveSourcePausedWhileDialogOpen(t *testing.T) {
	var loads atomic.Int32
	act := &Activity{}
	src := newLiveObjectRowSourceWithHooks(func(context.Context) ([]table.Row, error) {
		loads.Add(1)
		return oneRow("a"), nil
	}, nil, nil)
	src.useBackground(Deps{Ctx: t.Context(), Activity: act})
	src.Len(t.Context())

	act.Pause()
	src.lastLoad = time.Time{}
	src.MarkDirty()
	src.Len(t.Context())
	if n := loads.Load(); n != 1 {
		t.Fatalf("loaded %d times while paused, want 1", n)
	}
	act.Resume()
	src.Len(t.Context())
	if n := loads.Load(); n != 2 {
		t.Fatalf("loaded %d times after resume, want 2", n)
	}
}

func TestLiveSourceCloseStopsWatch(t *testing.T) {
	stopped := false
	src := newLiveObjectRowSourceWithHooks(func(context.Context) ([]table.Row, error) { return nil, nil }, nil,
		func(func(), func()) (func(), error) { return func() { stopped = true }, nil })
	src.Len(t.Context())
	src.Close()
	if !stopped {
		t.Fatalf("Close must cancel the watch")
	}
}
