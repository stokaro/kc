package info

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// rereadSource is a fakeSource that counts the rereads it was asked for.
type rereadSource struct {
	fakeSource
	rereads int
}

func (r *rereadSource) Reread() { r.rereads++ }

// The test is white-box like the rest of the package: it checks the tick
// generation, which the widget does not export.
func TestWidgetRereadReadsNow(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	var src *rereadSource
	h.target.Open = func() Summarizer {
		src = &rereadSource{key: h.target.Key, summary: h.summary}
		return src
	}
	if cmd := h.w.Reread(ctx); cmd != nil {
		t.Fatal("an inactive widget reread")
	}
	h.w.SetFocus(ctx, true)
	h.w.Init(ctx)
	gen := h.w.gen
	reads := len(src.reads)

	src.summary = models.InfoSummary{Bars: bars(40, 60)}
	cmd := h.w.Reread(ctx)
	if got := len(src.reads); got != reads+1 {
		t.Fatalf("reads = %d, want %d: a reread reads the summary at once", got, reads+1)
	}
	if src.rereads != 1 {
		t.Fatalf("source rereads = %d, want 1", src.rereads)
	}
	if h.w.summary.Bars == nil {
		t.Fatal("the reread summary is not shown")
	}
	if h.w.gen == gen {
		t.Fatal("the pending tick must go stale so the period starts over")
	}
	done, ok := findDone(cmd)
	if !ok || done.Err != nil {
		t.Fatalf("expected a successful RereadDoneMsg, got %#v (found %v)", done, ok)
	}

	src.summary = models.InfoSummary{Err: "overview cluster unavailable"}
	if done, ok := findDone(h.w.Reread(ctx)); !ok || done.Err == nil {
		t.Fatalf("expected the summary error in RereadDoneMsg, got %#v (found %v)", done, ok)
	}
}

// findDone runs the non-tick parts of cmd and returns the RereadDoneMsg.
func findDone(cmd tea.Cmd) (panelcontent.RereadDoneMsg, bool) {
	if cmd == nil {
		return panelcontent.RereadDoneMsg{}, false
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		return panelcontent.RereadDoneMsg{}, false
	}
	// The tick command blocks for the interval, so only the last command,
	// the report, runs.
	last := batch[len(batch)-1]
	if last == nil {
		return panelcontent.RereadDoneMsg{}, false
	}
	done, ok := last().(panelcontent.RereadDoneMsg)
	return done, ok
}
