package info

// White-box: the history and its rendering are internal to the Info
// widget; these tests drive them with a fake clock.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

var t0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

// fill adds a sample of v every step from from to to, both included.
func fill(s *series, from, to, step time.Duration, v float64) {
	for d := from; d <= to; d += step {
		s.add(t0.Add(d), v, "allocatable")
	}
}

func TestHistoryRenderContinuous(t *testing.T) {
	t.Parallel()
	var s series
	fill(&s, 0, 5*time.Minute, pollInterval, 50)
	view, ok := s.render(t0.Add(5*time.Minute), 60)
	if !ok {
		t.Fatal("no view")
	}
	if want := "history, % of allocatable · 21 samples over 5m"; view.label != want {
		t.Fatalf("label = %q, want %q", view.label, want)
	}
	if strings.ContainsRune(view.line, gapCell) {
		t.Fatalf("a continuous history shows a gap: %q", view.line)
	}
	// 5 of 15 minutes are collected: about a third of the cells, at the
	// right; the rest is blank, not a flat zero.
	trimmed := strings.TrimLeft(view.line, string(pendingCell))
	if n := len([]rune(trimmed)); n < 20 || n > 22 {
		t.Fatalf("collected cells = %d of 60: %q", n, view.line)
	}
	if strings.ContainsRune(view.line, '▁') {
		t.Fatalf("cells before the first sample must stay blank: %q", view.line)
	}
}

func TestHistoryRenderJitterIsNoGap(t *testing.T) {
	t.Parallel()
	var s series
	at := time.Duration(0)
	for i := range 40 {
		s.add(t0.Add(at), 30, "allocatable")
		at += pollInterval + time.Duration(i%2*2-1)*900*time.Millisecond
	}
	for _, width := range []int{20, 45, 60, 100} {
		view, _ := s.render(t0.Add(at), width)
		if strings.ContainsRune(view.line, gapCell) || strings.Contains(view.label, "gap") {
			t.Fatalf("width %d: polls 14-16s apart show a gap: %q %q", width, view.line, view.label)
		}
	}
}

func TestHistoryRenderGapAndStale(t *testing.T) {
	t.Parallel()
	var s series
	fill(&s, 0, 2*time.Minute, pollInterval, 20)
	fill(&s, 5*time.Minute, 7*time.Minute, pollInterval, 80) // three minutes missed
	view, _ := s.render(t0.Add(7*time.Minute), 60)
	if !strings.Contains(view.label, "1 gap") || strings.Contains(view.label, "stale") {
		t.Fatalf("label = %q", view.label)
	}
	line := strings.TrimLeft(view.line, string(pendingCell))
	if got := strings.Count(line, string(gapCell)); got < 10 || got > 13 {
		t.Fatalf("gap cells = %d, want about 12 (3m at 15s): %q", got, view.line)
	}
	if !strings.HasPrefix(line, "▂") || !strings.HasSuffix(line, "▇") {
		t.Fatalf("oldest and newest samples keep their values: %q", line)
	}

	// No sample for three minutes since: the newest cells are gaps and
	// the label says when the last sample was taken.
	stale, _ := s.render(t0.Add(10*time.Minute), 60)
	if !strings.Contains(stale.label, "stale, last 3m ago") {
		t.Fatalf("label = %q", stale.label)
	}
	if !strings.HasSuffix(stale.line, strings.Repeat(string(gapCell), 10)) {
		t.Fatalf("stale cells must be gaps: %q", stale.line)
	}
	if !strings.Contains(stale.label, "18 samples over 7m") {
		t.Fatalf("the span is oldest to newest sample, not the window: %q", stale.label)
	}
}

func TestHistoryRenderKeepsTheIntervalOnResize(t *testing.T) {
	t.Parallel()
	var s series
	fill(&s, 0, 6*time.Minute, pollInterval, 40)
	now := t0.Add(6 * time.Minute)
	collected := func(width int) float64 {
		view, _ := s.render(now, width)
		cells := []rune(view.line)
		return float64(len([]rune(strings.TrimLeft(view.line, string(pendingCell))))) / float64(len(cells))
	}
	// Six of fifteen minutes, at any width; wider than one cell per poll
	// the line stays at maxCells.
	for _, width := range []int{15, 30, 60, 90, 200} {
		if got := collected(width); got < 0.36 || got > 0.48 {
			t.Errorf("width %d: %0.2f of the cells hold samples, want about 0.4", width, got)
		}
		view, _ := s.render(now, width)
		if n := len([]rune(view.line)); n != min(width, maxCells) {
			t.Errorf("width %d: %d cells", width, n)
		}
		if view.label != "history, % of allocatable · 25 samples over 6m" {
			t.Errorf("width %d: label %q", width, view.label)
		}
	}
}

func TestHistoryStates(t *testing.T) {
	t.Parallel()
	var none series
	if _, ok := none.render(t0, 60); ok {
		t.Fatal("an empty series renders")
	}
	var one series
	one.add(t0, 10, "limits")
	if v, _ := one.render(t0, 60); v.line != "" || v.label != "history, % of limits: collecting, 1 sample" {
		t.Fatalf("one sample: %+v", v)
	}
	var old series
	fill(&old, 0, time.Minute, pollInterval, 10)
	if v, _ := old.render(t0.Add(20*time.Minute), 60); v.line != "" || v.label != "history, % of allocatable: no sample yet" {
		t.Fatalf("only old samples: %+v", v)
	}
	// A new base starts the series again.
	var s series
	fill(&s, 0, time.Minute, pollInterval, 10)
	s.add(t0.Add(75*time.Second), 90, "requests")
	if len(s.samples) != 1 || s.base != "requests" {
		t.Fatalf("base change kept %d samples of base %q", len(s.samples), s.base)
	}
	// Samples older than the window go, but one stays to tell a gap from
	// a continuation at the first cell.
	var long series
	fill(&long, 0, 40*time.Minute, pollInterval, 10)
	if got := len(long.samples); got != maxCells+1 {
		t.Fatalf("kept %d samples, want %d", got, maxCells+1)
	}
}

// TestWidgetHistoryFollowsItsTarget checks that a context switch does not
// join histories, and that unmeasured usage adds no sample.
func TestWidgetHistoryFollowsItsTarget(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := t0
	summary := models.InfoSummary{UsageAt: now, Bars: bars(30, 30)}
	target := Target{Key: "ctx-a|ns", Label: "ctx-a", Open: func() Summarizer { return &liveSource{s: &summary} }}
	w := New(panelcontent.WidgetDeps{}, Options{Resolve: func(context.Context) Target { return target }, Now: func() time.Time { return now }})
	w.SetFocus(ctx, true)
	_ = w.Init(ctx)
	for range 8 {
		now = now.Add(pollInterval)
		summary.UsageAt = now
		_ = w.OnSelectionChanged(ctx, panelcontent.Selection{})
	}
	if view := ansi.Strip(strings.Join(w.lines(60, 40), "\n")); !strings.Contains(view, "9 samples over 2m") {
		t.Fatalf("context a:\n%s", view)
	}

	target = Target{Key: "ctx-b|ns", Label: "ctx-b", Open: func() Summarizer { return &liveSource{s: &summary} }}
	now = now.Add(pollInterval)
	summary.UsageAt = now
	_ = w.OnSelectionChanged(ctx, panelcontent.Selection{})
	view := ansi.Strip(strings.Join(w.lines(60, 40), "\n"))
	if !strings.Contains(view, "collecting, 1 sample") || strings.Contains(view, "samples over") {
		t.Fatalf("context b joined the history of a:\n%s", view)
	}

	// Usage that is not measured adds nothing, not a zero.
	now = now.Add(pollInterval)
	summary = models.InfoSummary{UsageAt: now, Bars: []models.InfoBar{{Label: "CPU", Known: true, Percent: 10, Detail: "req"}, {Label: "Memory", Detail: "metrics unavailable"}}}
	_ = w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if got := len(w.history[w.historyKey()].cpu.samples); got != 1 {
		t.Fatalf("unmeasured usage added samples: %d", got)
	}
}

// liveSource returns whatever the test put into s.
type liveSource struct{ s *models.InfoSummary }

func (l *liveSource) Summary(models.InfoFocus) models.InfoSummary { return *l.s }
func (l *liveSource) Close()                                      {}
