package reload_test

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/ui/panelcontent/reload"
)

// harness drives a Loader with a fake clock. Ticks fire when their command
// runs and record the wait they were scheduled with.
type harness struct {
	t     *testing.T
	l     *reload.Loader
	now   time.Time
	waits []time.Duration
	calls []string // known versions passed to fetch, per call
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, now: time.Unix(1000, 0)}
	h.l = &reload.Loader{
		Now: func() time.Time { return h.now },
		After: func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			h.waits = append(h.waits, d)
			return func() tea.Msg { return fn(h.now) }
		},
	}
	h.l.SetActive(true)
	return h
}

// fetch returns a Fetch that reports version v and counts its calls.
func (h *harness) fetch(value, version string) reload.Fetch {
	return func(known string) reload.Result {
		h.calls = append(h.calls, known)
		if version != "" && version == known {
			return reload.Result{Version: version, Unchanged: true}
		}
		return reload.Result{Value: value, Version: version}
	}
}

// deliver runs cmd and feeds its message to the loader.
func (h *harness) deliver(cmd tea.Cmd) (reload.Result, bool, tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		h.t.Fatalf("expected a command, got nil")
	}
	res, apply, next, handled := h.l.Update(cmd())
	if !handled {
		h.t.Fatalf("loader did not handle its own message")
	}
	return res, apply, next
}

func TestShowNewKeyLoadsAtOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cmd := h.l.Show("a", h.fetch("A", "1"))
	if !h.l.Loading() {
		t.Fatalf("expected Loading before the result arrived")
	}
	if len(h.calls) != 0 {
		t.Fatalf("fetch ran on the UI goroutine; it must run in the command")
	}
	res, apply, next := h.deliver(cmd)
	if !apply || res.Value != "A" {
		t.Fatalf("expected result A to apply, got apply=%v value=%v", apply, res.Value)
	}
	if next != nil {
		t.Fatalf("expected no follow-up command")
	}
	if h.l.Loading() {
		t.Fatalf("expected Loading to end with the result")
	}
	if len(h.waits) != 0 {
		t.Fatalf("a new target must not wait for a tick, got waits %v", h.waits)
	}
}

func TestOneLoadInFlightAndStaleResultDropped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cmdA := h.l.Show("a", h.fetch("A", "1"))
	if cmd := h.l.Show("b", h.fetch("B", "7")); cmd != nil {
		t.Fatalf("expected the load of b to wait for the load in flight")
	}
	_, apply, next := h.deliver(cmdA)
	if apply {
		t.Fatalf("result for a must be dropped after the selection moved to b")
	}
	res, apply, _ := h.deliver(next)
	if !apply || res.Value != "B" {
		t.Fatalf("expected the queued load of b to apply, got apply=%v value=%v", apply, res.Value)
	}
}

func TestRefreshWaitsForInterval(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.deliver(h.l.Show("a", h.fetch("A", "1")))
	h.now = h.now.Add(300 * time.Millisecond)
	cmd := h.l.Refresh()
	if len(h.waits) != 1 || h.waits[0] != reload.Interval-300*time.Millisecond {
		t.Fatalf("expected one tick of %v, got %v", reload.Interval-300*time.Millisecond, h.waits)
	}
	if again := h.l.Refresh(); again != nil {
		t.Fatalf("a second refresh while a tick is pending must not schedule another")
	}
	// The tick starts the load; the cached version is unchanged.
	_, _, load := h.deliver(cmd)
	_, apply, next := h.deliver(load)
	if apply {
		t.Fatalf("unchanged version must not replace the content")
	}
	if got := h.calls[len(h.calls)-1]; got != "1" {
		t.Fatalf("expected fetch to see known version 1, got %q", got)
	}
	// The refresh asked while the tick was pending was folded into it.
	if next != nil {
		t.Fatalf("expected no extra refresh, got a command")
	}
}

func TestRefreshDuringLoadRunsAfterIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cmd := h.l.Show("a", h.fetch("A", "1"))
	if r := h.l.Refresh(); r != nil {
		t.Fatalf("refresh during a load must wait for it")
	}
	_, _, next := h.deliver(cmd)
	if next == nil {
		t.Fatalf("expected the pending refresh to be scheduled after the load")
	}
	if len(h.waits) != 1 || h.waits[0] != reload.Interval {
		t.Fatalf("expected the refresh to wait a full interval, got %v", h.waits)
	}
}

func TestRefreshPausedRearms(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	paused := true
	h.l.Paused = func() bool { return paused }
	h.deliver(h.l.Show("a", h.fetch("A", "1")))
	h.now = h.now.Add(2 * time.Second)
	tick := h.l.Refresh()
	_, _, next := h.deliver(tick)
	if len(h.calls) != 1 {
		t.Fatalf("paused refresh must not fetch, got %d fetches", len(h.calls))
	}
	if len(h.waits) != 2 || h.waits[1] != reload.Interval {
		t.Fatalf("expected a re-armed tick of one interval, got %v", h.waits)
	}
	paused = false
	_, _, load := h.deliver(next)
	h.deliver(load)
	if len(h.calls) != 2 {
		t.Fatalf("expected the refresh to fetch after resume, got %d fetches", len(h.calls))
	}
}

func TestInactiveLoaderCatchesUpOnShow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.deliver(h.l.Show("a", h.fetch("A", "1")))
	h.now = h.now.Add(2 * time.Second)
	tick := h.l.Refresh()
	h.l.SetActive(false)
	_, _, next := h.deliver(tick)
	if next != nil || len(h.calls) != 1 {
		t.Fatalf("an inactive loader must not fetch")
	}
	if cmd := h.l.Show("b", h.fetch("B", "2")); cmd != nil {
		t.Fatalf("an inactive loader must not start a load")
	}
	h.l.SetActive(true)
	cmd := h.l.Show("b", h.fetch("B", "2"))
	res, apply, _ := h.deliver(cmd)
	if !apply || res.Value != "B" {
		t.Fatalf("expected b to load after activation, got apply=%v value=%v", apply, res.Value)
	}
}

func TestStaleTickIgnored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.deliver(h.l.Show("a", h.fetch("A", "1")))
	tick := h.l.Refresh()
	loadB := h.l.Show("b", h.fetch("B", "2"))
	if _, _, next := h.deliver(tick); next != nil {
		t.Fatalf("a tick for the old target must do nothing")
	}
	res, apply, _ := h.deliver(loadB)
	if !apply || res.Value != "B" {
		t.Fatalf("expected b to apply, got apply=%v value=%v", apply, res.Value)
	}
}

func TestClearDropsResult(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cmd := h.l.Show("a", h.fetch("A", "1"))
	h.l.Show("", nil)
	if h.l.Loading() || h.l.Key() != "" {
		t.Fatalf("expected no target after clear")
	}
	if _, apply, _ := h.deliver(cmd); apply {
		t.Fatalf("result after clear must be dropped")
	}
	if cmd := h.l.Refresh(); cmd != nil {
		t.Fatalf("refresh without a target must do nothing")
	}
}

func TestForeignMessagesNotHandled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	other := newHarness(t)
	cmd := other.l.Show("a", other.fetch("A", "1"))
	if _, _, _, handled := h.l.Update(cmd()); handled {
		t.Fatalf("a loader must not take another loader's result")
	}
	if _, _, _, handled := h.l.Update(tea.KeyPressMsg{}); handled {
		t.Fatalf("a loader must not take unrelated messages")
	}
}
