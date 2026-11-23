package reload_test

import "testing"

func TestRereadLoadsAtOnceAndIgnoresKnownVersion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.deliver(h.l.Show("a", h.fetch("A", "1")))

	cmd := h.l.Reread()
	if !h.l.Rereading() {
		t.Fatalf("expected Rereading while the reread runs")
	}
	if h.l.Loading() {
		t.Fatalf("a reread of shown content must not count as Loading")
	}
	res, apply, _ := h.deliver(cmd)
	if len(h.waits) != 0 {
		t.Fatalf("a reread must not wait for a tick, got waits %v", h.waits)
	}
	if got := h.calls[len(h.calls)-1]; got != "" {
		t.Fatalf("a reread must not pass the known version, got %q", got)
	}
	if !apply || !res.Reread || res.Value != "A" {
		t.Fatalf("expected an applied reread result, got apply=%v reread=%v value=%v", apply, res.Reread, res.Value)
	}
	if h.l.Rereading() {
		t.Fatalf("expected Rereading to end with the result")
	}
}

func TestRereadWhileLoadingRunsAfterTheLoad(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	first := h.l.Show("a", h.fetch("A", "1"))
	if cmd := h.l.Reread(); cmd != nil {
		t.Fatalf("a reread during a load must wait for it")
	}
	res, _, next := h.deliver(first)
	if res.Reread {
		t.Fatalf("the first load does not answer the reread")
	}
	if !h.l.Rereading() {
		t.Fatalf("expected the reread to be pending")
	}
	res, apply, _ := h.deliver(next)
	if !apply || !res.Reread {
		t.Fatalf("expected the queued reread to load and apply, got apply=%v reread=%v", apply, res.Reread)
	}
	if got := h.calls[len(h.calls)-1]; got != "" {
		t.Fatalf("the queued reread must not pass the known version, got %q", got)
	}
}

func TestRereadWithoutTargetDoesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if cmd := h.l.Reread(); cmd != nil {
		t.Fatalf("expected no command without a target")
	}
	if h.l.Rereading() {
		t.Fatalf("expected no pending reread without a target")
	}
}
