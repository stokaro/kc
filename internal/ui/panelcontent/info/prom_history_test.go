package info

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// fakePromHistory is a PromHistorySource whose reads the test answers.
type fakePromHistory struct {
	starts   int
	canceled int
	h        promhistory.History
	err      error
}

func (f *fakePromHistory) source(key string) *PromHistorySource {
	return &PromHistorySource{Key: key, Window: time.Hour, Start: func() (func() (promhistory.History, error), func()) {
		f.starts++
		h, err := f.h, f.err
		return func() (promhistory.History, error) { return h, err }, func() { f.canceled++ }
	}}
}

// samplePromHistory is an hour at one-minute steps on a minute boundary:
// samples an hour back, a gap, and samples at the end.
func samplePromHistory(end time.Time) promhistory.History {
	start := end.Add(-time.Hour)
	pt := func(minute int, v float64) promhistory.Point {
		return promhistory.Point{At: start.Add(time.Duration(minute) * time.Minute), Value: v}
	}
	return promhistory.History{
		Target: promhistory.Target{Kind: "Deployment", Namespace: "shop", Name: "web"},
		Source: "monitoring/prom:9090",
		Start:  start, End: end, Step: time.Minute,
		CPU:       []promhistory.Point{pt(0, 0.5), pt(1, 0.25), pt(59, 1.5), pt(60, 0.75)},
		Memory:    []promhistory.Point{pt(0, 512<<20), pt(60, 256<<20)},
		Pods:      7,
		PodsKnown: true,
	}
}

func promHarness(t *testing.T) (*harness, *fakePromHistory) {
	t.Helper()
	h := newHarness(t)
	h.summary = models.InfoSummary{UsageAt: h.now, Bars: bars(10, 20)}
	f := &fakePromHistory{h: samplePromHistory(h.now.Truncate(time.Minute))}
	h.target.Workload = &models.InfoWorkload{Namespace: "shop", Name: "web"}
	h.target.PromHistory = f.source("ctx|web")
	h.w.SetFocus(t.Context(), true)
	return h, f
}

// runPromRead executes cmd, which must be one history read, and feeds the result.
func runPromRead(t *testing.T, w *Widget, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no history read started")
	}
	msg, ok := cmd().(promHistoryMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want promHistoryMsg", msg)
	}
	if _, handled := w.Update(t.Context(), msg); !handled {
		t.Fatal("history result not handled")
	}
}

func promScreen(w *Widget) string {
	return ansi.Strip(strings.Join(w.lines(120, 60), "\n"))
}

func TestPromHistoryReadAndView(t *testing.T) {
	t.Parallel()
	h, f := promHarness(t)
	ctx := t.Context()
	cmd := h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if got := promScreen(h.w); !strings.Contains(got, "Prometheus history, last 1h\n   reading...") {
		t.Fatalf("before the result:\n%s", got)
	}
	runPromRead(t, h.w, cmd)
	got := promScreen(h.w)
	for _, want := range []string{
		"Prometheus history, last 1h (monitoring/prom:9090)",
		"max 1.50, end 750m",
		"max 512Mi, end 256Mi",
		"step 1m, 7 pods by owner series\n   59 of 61 steps without samples",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}
	// The CPU sparkline starts with the two samples, then a gap; the peak
	// is a full block.
	var cpuLine string
	for line := range strings.SplitSeq(got, "\n") {
		if strings.HasPrefix(line, " CPU ") && strings.Contains(line, "max ") {
			cpuLine = line
		}
	}
	if !strings.Contains(cpuLine, "▃▂ ") || !strings.Contains(cpuLine, "█") {
		t.Errorf("CPU sparkline %q, want values, a gap and the peak", cpuLine)
	}

	// Not due yet: no second read within a minute.
	if cmd := h.w.OnSelectionChanged(ctx, panelcontent.Selection{}); cmd != nil || f.starts != 1 {
		t.Fatalf("read again at once: cmd %v starts %d", cmd != nil, f.starts)
	}
	h.now = h.now.Add(time.Minute)
	runPromRead(t, h.w, h.w.OnSelectionChanged(ctx, panelcontent.Selection{}))
	if f.starts != 2 {
		t.Fatalf("starts = %d, want a second read after a minute", f.starts)
	}

	// A failure keeps the last good history and says so.
	f.err = errors.New("prometheus monitoring/prom:9090: HTTP 503: no endpoints available")
	h.now = h.now.Add(time.Minute)
	runPromRead(t, h.w, h.w.OnSelectionChanged(ctx, panelcontent.Selection{}))
	got = promScreen(h.w)
	if !strings.Contains(got, "unavailable: prometheus monitoring/prom:9090: HTTP 503") || !strings.Contains(got, "max 1.50") {
		t.Errorf("after a failure:\n%s", got)
	}
	// After a failure the next read waits longer.
	h.now = h.now.Add(time.Minute)
	if cmd := h.w.OnSelectionChanged(ctx, panelcontent.Selection{}); cmd != nil {
		t.Error("retried a failure after one minute")
	}
}

func TestPromHistoryStaleResultDropped(t *testing.T) {
	t.Parallel()
	h, f := promHarness(t)
	ctx := t.Context()
	old := h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if old == nil {
		t.Fatal("no read started")
	}
	// The cursor moves to another workload while the read runs.
	other := &fakePromHistory{h: f.h}
	other.h.Target.Name = "api"
	h.target.PromHistory = other.source("ctx|api")
	next := h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if f.canceled != 1 {
		t.Fatalf("canceled = %d, want the obsolete read canceled", f.canceled)
	}
	if _, handled := h.w.Update(ctx, old()); !handled {
		t.Fatal("old result not handled")
	}
	if got := promScreen(h.w); !strings.Contains(got, "reading...") || strings.Contains(got, "max ") {
		t.Fatalf("the obsolete result replaced the current one:\n%s", got)
	}
	runPromRead(t, h.w, next)
	if got := promScreen(h.w); !strings.Contains(got, "max 1.50") {
		t.Fatalf("current result not shown:\n%s", got)
	}

	// Leaving the mode cancels a read in flight and its result is dropped.
	h.now = h.now.Add(time.Minute)
	late := h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	h.w.SetFocus(ctx, false)
	if other.canceled < 2 {
		t.Fatalf("canceled = %d, want the read in flight canceled on leave", other.canceled)
	}
	if _, handled := h.w.Update(ctx, late()); !handled || h.w.prom.h != nil {
		t.Fatal("a result after leaving the mode was stored")
	}
}

func TestPromHistoryStates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*promhistory.History)
		want string
	}{
		{name: "no owner series", edit: func(h *promhistory.History) { h.CPU, h.Memory, h.NoOwners = nil, nil, true }, want: "no kube_pod_owner series in shop (kube-state-metrics missing?):\n   pods cannot be tied to Deployment shop/web"},
		{name: "no samples", edit: func(h *promhistory.History) { h.CPU, h.Memory = nil, nil }, want: "no samples for the pods of Deployment shop/web in this window"},
		{name: "memory unavailable", edit: func(h *promhistory.History) { h.Memory = nil }, want: " Memory no series"},
		{name: "old last sample", edit: func(h *promhistory.History) { h.CPU = h.CPU[:3]; h.Memory = h.Memory[:1] }, want: "last sample 1m ago"},
		{name: "backend warning", edit: func(h *promhistory.History) { h.Warnings = []string{"PromQL info: metric might not be a counter"} }, want: "backend: PromQL info"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, f := promHarness(t)
			tc.edit(&f.h)
			runPromRead(t, h.w, h.w.OnSelectionChanged(t.Context(), panelcontent.Selection{}))
			if got := promScreen(h.w); !strings.Contains(got, tc.want) {
				t.Errorf("view lacks %q:\n%s", tc.want, got)
			}
		})
	}
}

func TestPromHistoryWithoutSourceOrPaused(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h, f := promHarness(t)
	h.w.deps.Paused = func() bool { return true }
	if cmd := h.w.OnSelectionChanged(ctx, panelcontent.Selection{}); cmd != nil || f.starts != 0 {
		t.Fatal("read while a dialog is open")
	}
	h.w.deps.Paused = nil
	h.target.PromHistory = nil
	if cmd := h.w.OnSelectionChanged(ctx, panelcontent.Selection{}); cmd != nil {
		t.Fatal("read without a source")
	}
	if got := promScreen(h.w); strings.Contains(got, "Prometheus") {
		t.Errorf("history block without a backend:\n%s", got)
	}
}

// The overview may be refused while Prometheus answers: a namespace-only
// user still sees the history.
func TestPromHistoryShownWhenOverviewRefused(t *testing.T) {
	t.Parallel()
	h, _ := promHarness(t)
	h.summary = models.InfoSummary{Err: "not permitted"}
	h.setScope("ctx|ns", "ctx: namespace ns")
	h.target.Workload = &models.InfoWorkload{Namespace: "shop", Name: "web"}
	f := &fakePromHistory{h: samplePromHistory(h.now.Truncate(time.Minute))}
	h.target.PromHistory = f.source("ctx|web")
	runPromRead(t, h.w, h.w.OnSelectionChanged(t.Context(), panelcontent.Selection{}))
	got := promScreen(h.w)
	if !strings.Contains(got, "Overview unavailable: not permitted") || !strings.Contains(got, "max 1.50") {
		t.Errorf("view:\n%s", got)
	}
}

// A finding row shows its preview instead of the bars, and the Prometheus
// block belongs to the workload view only.
func TestPromHistoryNotOnFindingRows(t *testing.T) {
	t.Parallel()
	h, _ := promHarness(t)
	runPromRead(t, h.w, h.w.OnSelectionChanged(t.Context(), panelcontent.Selection{}))
	if got := promScreen(h.w); !strings.Contains(got, "Prometheus history") {
		t.Fatalf("workload view lacks the block:\n%s", got)
	}
	h.target.Finding = &models.FindingPreview{ID: "health/pods-pending", Title: "/pods pending"}
	_ = h.w.OnSelectionChanged(t.Context(), panelcontent.Selection{})
	if got := promScreen(h.w); !strings.Contains(got, "/pods pending") || strings.Contains(got, "Prometheus history") {
		t.Errorf("finding view:\n%s", got)
	}
}
