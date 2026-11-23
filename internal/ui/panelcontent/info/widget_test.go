package info

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

type fakeSource struct {
	key     string
	summary models.InfoSummary
	reads   []string // nodes asked for
	closed  bool
}

func (f *fakeSource) Summary(focus models.InfoFocus) models.InfoSummary {
	f.reads = append(f.reads, focus.Node)
	return f.summary
}

func (f *fakeSource) Close() { f.closed = true }

// harness drives a widget with a target the test changes.
type harness struct {
	w       *Widget
	target  Target
	opened  []*fakeSource
	summary models.InfoSummary
	now     time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	h.setScope("ctx|ns", "ctx: namespace ns")
	h.w = New(panelcontent.WidgetDeps{}, Options{
		Resolve: func(context.Context) Target { return h.target },
		Now:     func() time.Time { return h.now },
	})
	return h
}

func (h *harness) setScope(key, label string) {
	h.target = Target{Key: key, Label: label, Open: func() Summarizer {
		src := &fakeSource{key: key, summary: h.summary}
		h.opened = append(h.opened, src)
		return src
	}}
}

func (h *harness) last() *fakeSource { return h.opened[len(h.opened)-1] }

func bars(cpu, mem float64) []models.InfoBar {
	return []models.InfoBar{
		{Label: "CPU", Known: true, Usage: true, Base: "allocatable", Percent: cpu, Detail: "pods 1.0/4.0 cores allocatable, 25%", More: []string{"node 1.2/4.0 cores capacity, 30%", "req 10%, lim 20%"}, Flag: "ok", Health: models.RowHealthOK},
		{Label: "Memory", Known: true, Usage: true, Base: "allocatable", Percent: mem, Detail: "pods 5.5/10.0 Gi allocatable, 55% (under-req)", More: []string{"node 6.0/12.0 Gi capacity, 50%", "req 10%, lim 64%"}, Flag: "under-req", Health: models.RowHealthNotReady},
		{Label: "Pods", Known: true, Percent: 25, Detail: "27/110", Flag: "ok"},
		{Label: "Disk", Detail: "forbidden: nodes/proxy"},
	}
}

func TestWidgetLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	if cmd := h.w.Init(ctx); cmd != nil || len(h.opened) != 0 {
		t.Fatalf("an inactive widget started: cmd %v, opened %d", cmd != nil, len(h.opened))
	}
	h.w.SetFocus(ctx, true)
	if cmd := h.w.Init(ctx); cmd == nil {
		t.Fatal("Init of an active widget returned no tick")
	}
	if len(h.opened) != 1 || len(h.last().reads) != 1 {
		t.Fatalf("opened %d sources, %d reads, want 1 and 1", len(h.opened), len(h.last().reads))
	}

	// Its own tick reads again and ticks on; another widget's tick and a
	// tick of an old generation are dropped.
	gen := h.w.gen
	if cmd, handled := h.w.Update(ctx, tickMsg{w: h.w, gen: gen}); !handled || cmd == nil {
		t.Fatalf("own tick: handled %v cmd %v", handled, cmd != nil)
	}
	if _, handled := h.w.Update(ctx, tickMsg{w: New(panelcontent.WidgetDeps{}, Options{}), gen: gen}); handled {
		t.Fatal("handled another widget's tick")
	}
	if cmd, handled := h.w.Update(ctx, tickMsg{w: h.w, gen: gen - 1}); !handled || cmd != nil {
		t.Fatalf("stale tick: handled %v cmd %v, want handled and no new tick", handled, cmd != nil)
	}
	if got := len(h.last().reads); got != 2 {
		t.Fatalf("reads = %d, want 2", got)
	}

	// The node under the other panel's cursor narrows the summary; the
	// source stays because the scope did not change.
	h.target.Node = "mba"
	_ = h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if len(h.opened) != 1 || h.last().reads[len(h.last().reads)-1] != "mba" {
		t.Fatalf("node selection: opened %d, reads %v", len(h.opened), h.last().reads)
	}

	// A new scope closes the old source and opens a new one.
	first := h.last()
	h.setScope("ctx|other", "ctx: namespace other")
	_ = h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if !first.closed || len(h.opened) != 2 {
		t.Fatalf("scope change: old closed %v, opened %d", first.closed, len(h.opened))
	}

	// Leaving the mode closes the source and stops the ticks.
	h.w.SetFocus(ctx, false)
	if !h.last().closed {
		t.Fatal("leaving the mode did not close the source")
	}
	if cmd, _ := h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen}); cmd != nil {
		t.Fatal("an inactive widget ticks on")
	}
}

func TestWidgetViewAndHistory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.summary = models.InfoSummary{
		UsageAt: h.now.Add(-5 * time.Second),
		Bars:    bars(25, 55),
		Top:     []models.InfoConsumer{{Namespace: "db", Name: "postgres-0", CPU: "120m", Memory: "2048Mi"}},
	}
	h.w.SetFocus(ctx, true)
	_ = h.w.Init(ctx)
	view := ansi.Strip(strings.Join(h.w.lines(60, 40), "\n"))
	for _, want := range []string{
		"ctx: namespace ns", "usage measured 5s ago",
		"CPU", " 25%", "pods 1.0/4.0 cores allocatable", "55% under-req",
		"        node 6.0/12.0 Gi capacity, 50%\n        req 10%, lim 64%",
		"Disk   n/a", "forbidden: nodes/proxy",
		"Top pods by memory", "2048Mi   120m  db/postgres-0",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view misses %q:\n%s", want, view)
		}
	}
	if strings.ContainsAny(view, "▁▂▃▄▅▆▇") {
		t.Fatalf("a sparkline after one sample:\n%s", view)
	}
	if !strings.Contains(view, "% of allocatable: collecting, 1 sample") {
		t.Fatalf("one sample must say it is collecting:\n%s", view)
	}
	for i, line := range h.w.lines(60, 40) {
		if w := ansi.StringWidth(line); w > 60 {
			t.Fatalf("line %d is %d cells wide: %q", i, w, line)
		}
	}

	// The same measurement adds no sample; a new one does.
	h.last().summary.Bars = bars(25, 100)
	_, _ = h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen})
	if got := len(h.w.history[h.w.historyKey()].mem.samples); got != 1 {
		t.Fatalf("samples after a read of the same measurement = %d, want 1", got)
	}
	h.last().summary.UsageAt = h.now
	_, _ = h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen})
	hist := h.w.history[h.w.historyKey()]
	if len(hist.mem.samples) != 2 || hist.mem.samples[1].v != 100 {
		t.Fatalf("memory samples = %v, want 55 then 100", hist.mem.samples)
	}
	view = ansi.Strip(strings.Join(h.w.lines(60, 40), "\n"))
	if !strings.Contains(view, "        history, % of allocatable · 2 samples over 5s\n        ▕") {
		t.Fatalf("no labeled memory sparkline:\n%s", view)
	}
	if got := len(h.w.lines(60, 3)); got != 3 {
		t.Fatalf("lines in 3 rows = %d", got)
	}
}

// TestWidgetRendersCapacityRows checks the Memory row of issue #165 as the
// panel prints it: the bar's percentage is truncated and carries no
// overcommitted word, the limits line does, and every line under the bar
// is indented under it. A bar of requests adds no usage sample.
func TestWidgetRendersCapacityRows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.summary = models.InfoSummary{
		UsageAt: h.now,
		Bars: []models.InfoBar{
			{Label: "CPU", Known: true, Percent: 14.75, Detail: "req 0.6/4.0 cores allocatable, 14%", More: []string{"lim 112% (overcommitted)", "use n/a: loading"}, Flag: "ok", Health: models.RowHealthNotReady},
			{Label: "Memory", Known: true, Usage: true, Percent: 22.9, Detail: "pods 2.3/10.4 Gi allocatable, 22%", More: []string{"node 6.0/14.9 Gi capacity, 40%", "req 25%, lim 116% (overcommitted)"}, Flag: "ok", Health: models.RowHealthNotReady},
		},
		TopNote: "no pods in empty",
	}
	h.w.SetFocus(ctx, true)
	_ = h.w.Init(ctx)
	lines := h.w.lines(60, 40)
	view := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{
		" 14%\n        req 0.6/4.0 cores allocatable, 14%\n        lim 112% (overcommitted)\n        use n/a: loading\n",
		" 22%\n        pods 2.3/10.4 Gi allocatable, 22%\n        node 6.0/14.9 Gi capacity, 40%\n        req 25%, lim 116% (overcommitted)\n",
		" Top pods by memory\n   no pods in empty",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view misses %q:\n%s", want, view)
		}
	}
	for _, line := range lines {
		if s := ansi.Strip(line); strings.HasPrefix(s, " Memory") && strings.Contains(s, "overcommitted") {
			t.Fatalf("the memory bar carries the limits' word: %q", s)
		}
	}
	if h.w.history[h.w.historyKey()] != nil {
		t.Fatal("a CPU bar of requests was recorded as usage")
	}
}

func TestWidgetHintsAndStates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	tests := []struct {
		name    string
		target  Target
		summary models.InfoSummary
		want    string
	}{
		{name: "no cluster", target: Target{Hint: "Not connected to a cluster."}, want: "Not connected to a cluster."},
		{name: "loading", summary: models.InfoSummary{Loading: true}, want: "Loading..."},
		{name: "error", summary: models.InfoSummary{Err: "no cluster"}, want: "Overview unavailable: no cluster"},
		{name: "node scope", target: Target{Node: "mba"}, summary: models.InfoSummary{Bars: bars(1, 1)}, want: "node mba"},
		{name: "workload scope", target: Target{Node: "mba", Workload: &webWorkload}, summary: models.InfoSummary{Bars: bars(1, 1)}, want: "deployments.apps shop/web"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := tc.target
			if target.Hint == "" {
				target.Key = "k"
				target.Open = func() Summarizer { return &fakeSource{summary: tc.summary} }
			}
			w := New(panelcontent.WidgetDeps{}, Options{Resolve: func(context.Context) Target { return target }})
			w.SetFocus(ctx, true)
			_ = w.Init(ctx)
			if view := ansi.Strip(strings.Join(w.lines(60, 20), "\n")); !strings.Contains(view, tc.want) {
				t.Fatalf("view misses %q:\n%s", tc.want, view)
			}
		})
	}
}

var webWorkload = models.InfoWorkload{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespace: "shop", Name: "web"}

// TestWidgetAsksForTheWorkload checks that a workload under the other
// panel's cursor reaches the summary and keeps a history of its own.
func TestWidgetAsksForTheWorkload(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var focuses []models.InfoFocus
	target := Target{Key: "k", Open: func() Summarizer { return &focusSource{seen: &focuses} }}
	w := New(panelcontent.WidgetDeps{}, Options{Resolve: func(context.Context) Target { return target }})
	w.SetFocus(ctx, true)
	_ = w.Init(ctx)
	scopeKey := w.historyKey()

	target.Workload = &webWorkload
	_ = w.OnSelectionChanged(ctx, panelcontent.Selection{})
	last := focuses[len(focuses)-1]
	if last.Workload == nil || *last.Workload != webWorkload {
		t.Fatalf("focus = %+v, want the workload", last)
	}
	if w.historyKey() == scopeKey || !strings.Contains(w.historyKey(), "apps/v1, Resource=deployments") {
		t.Fatalf("history key %q does not separate the workload from %q", w.historyKey(), scopeKey)
	}
}

type focusSource struct{ seen *[]models.InfoFocus }

func (f *focusSource) Summary(focus models.InfoFocus) models.InfoSummary {
	*f.seen = append(*f.seen, focus)
	return models.InfoSummary{Bars: bars(1, 1)}
}

func (f *focusSource) Close() {}

func TestRenderBarAndSparkline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pct   float64
		width int
		want  string
	}{
		{0, 4, "░░░░"},
		{50, 4, "██░░"},
		{55, 10, "█████▌░░░░"},
		{100, 3, "███"},
		{150, 3, "███"},
		{-5, 2, "░░"},
	}
	for _, tc := range tests {
		if got := renderBar(tc.pct, tc.width); got != tc.want {
			t.Errorf("renderBar(%v, %d) = %q, want %q", tc.pct, tc.width, got, tc.want)
		}
	}
	for v, want := range map[float64]rune{0: '▁', 50: '▅', 100: '█', 120: '█', -3: '▁'} {
		if got := sparkRune(v); got != want {
			t.Errorf("sparkRune(%v) = %q, want %q", v, got, want)
		}
	}
}
