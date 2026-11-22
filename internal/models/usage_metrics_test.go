package models

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// fakeUsage counts polls and returns a served snapshot.
type fakeUsage struct {
	polls atomic.Int32
	mu    sync.Mutex
	scope []string
}

func (f *fakeUsage) fetch(_ context.Context, _ Deps, kind usageKind, namespace string, _ bool) *usageSnapshot {
	f.polls.Add(1)
	f.mu.Lock()
	f.scope = append(f.scope, kind.String()+":"+namespace)
	f.mu.Unlock()
	return &usageSnapshot{served: true, values: make(map[string]usageValues)}
}

func newTestUsage(f *fakeUsage) *UsageMetrics {
	return &UsageMetrics{interval: usagePollInterval, fetch: f.fetch, now: time.Now}
}

func TestUsagePollerStartsPollsAndStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f := &fakeUsage{}
		m := newTestUsage(f)
		deps := Deps{Ctx: ctx, ClusterKey: kccluster.Key{ContextName: "c"}}
		key := usagePollerKey{cluster: deps.ClusterKey, kind: usagePods, namespace: "ns"}
		var notified atomic.Int32
		notify := func() { notified.Add(1) }
		watcher := new(int)

		snap, _ := m.snapshot(deps, usagePods, "ns", watcher, notify)
		if snap != nil {
			t.Fatalf("snapshot before the first poll = %+v, want nil", snap)
		}
		synctest.Wait()
		if got := f.polls.Load(); got != 1 {
			t.Fatalf("polls after start = %d, want 1 at once", got)
		}
		if got := notified.Load(); got != 1 {
			t.Fatalf("notifications after the first poll = %d, want 1", got)
		}
		snap, fresh := m.snapshot(deps, usagePods, "ns", watcher, notify)
		if snap == nil || !snap.served || !fresh {
			t.Fatalf("snapshot after the poll = %+v fresh=%v, want a fresh served snapshot", snap, fresh)
		}

		// A second list in the same scope shares the poller.
		m.snapshot(deps, usagePods, "ns", new(int), nil)
		synctest.Wait()
		if got := f.polls.Load(); got != 1 {
			t.Fatalf("polls after a second reader = %d, want still 1", got)
		}

		// The reader keeps reading: one poll per interval.
		for i := 2; i <= 3; i++ {
			time.Sleep(usagePollInterval)
			synctest.Wait()
			m.snapshot(deps, usagePods, "ns", watcher, notify)
			if got := f.polls.Load(); got != int32(i) {
				t.Fatalf("polls after %d intervals = %d, want %d", i-1, got, i)
			}
		}

		// Nobody reads any more: the poller stops after the idle window, and
		// wakes the readers one last time.
		before := notified.Load()
		time.Sleep(3 * usagePollInterval)
		synctest.Wait()
		if m.running(key) {
			t.Fatal("poller still running without readers")
		}
		if got := f.polls.Load(); got > 5 {
			t.Fatalf("polls while idle = %d, want at most 2 more after the last read", got-3)
		}
		if notified.Load() <= before {
			t.Fatal("readers were not woken when the poller stopped")
		}
		stopped := f.polls.Load()
		time.Sleep(5 * usagePollInterval)
		synctest.Wait()
		if got := f.polls.Load(); got != stopped {
			t.Fatalf("stopped poller polled %d more times", got-stopped)
		}

		// Reading again restarts it, and the old snapshot is stale.
		snap, fresh = m.snapshot(deps, usagePods, "ns", watcher, notify)
		if snap == nil || fresh {
			t.Fatalf("snapshot after a long stop = %+v fresh=%v, want the old one, stale", snap, fresh)
		}
		synctest.Wait()
		if !m.running(key) || f.polls.Load() != stopped+1 {
			t.Fatalf("restart: running=%v polls=%d, want a poll at once", m.running(key), f.polls.Load()-stopped)
		}
	})
}

func TestUsagePollerPausesWhileADialogIsOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f := &fakeUsage{}
		m := newTestUsage(f)
		activity := &Activity{}
		deps := Deps{Ctx: ctx, Activity: activity}
		watcher := new(int)

		activity.Pause()
		m.snapshot(deps, usageNodes, "", watcher, nil)
		synctest.Wait()
		time.Sleep(usagePollInterval)
		synctest.Wait()
		if got := f.polls.Load(); got != 0 {
			t.Fatalf("polls while paused = %d, want 0", got)
		}
		activity.Resume()
		m.snapshot(deps, usageNodes, "", watcher, nil)
		time.Sleep(usagePollInterval)
		synctest.Wait()
		if got := f.polls.Load(); got != 1 {
			t.Fatalf("polls after resume = %d, want 1", got)
		}
	})
}

func TestUsagePollersPerScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f := &fakeUsage{}
		m := newTestUsage(f)
		deps := Deps{Ctx: ctx}
		m.snapshot(deps, usagePods, "a", new(int), nil)
		m.snapshot(deps, usagePods, "", new(int), nil)
		m.snapshot(deps, usageNodes, "", new(int), nil)
		synctest.Wait()
		if got := f.polls.Load(); got != 3 {
			t.Fatalf("polls = %d, want one per scope", got)
		}
		// Ending the app context stops every poller.
		cancel()
		synctest.Wait()
		for _, k := range []usagePollerKey{{kind: usagePods, namespace: "a"}, {kind: usagePods}, {kind: usageNodes}} {
			if m.running(k) {
				t.Errorf("poller %+v still running after the context ended", k)
			}
		}
	})
}

func TestNilUsageMetricsAddsNothing(t *testing.T) {
	t.Parallel()
	var m *UsageMetrics
	if snap, fresh := m.snapshot(Deps{}, usagePods, "", new(int), nil); snap != nil || fresh {
		t.Fatal("nil registry returned a snapshot")
	}
}
