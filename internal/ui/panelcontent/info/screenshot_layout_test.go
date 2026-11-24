package info

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
)

// TestBarWithMarkKeepsThePanelBackground renders the Memory row of a
// reported screenshot: a red "! lim" mark, then the pods line next to the
// bar. Text after the red mark must keep the panel's blue background.
func TestBarWithMarkKeepsThePanelBackground(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.summary = models.InfoSummary{UsageAt: h.now, Bars: []models.InfoBar{{
		Label: "Memory", Known: true, Usage: true, Base: "allocatable", Percent: 23,
		Detail: "pods 2.4/10.4 Gi allocatable, 23%", Flag: "ok", Health: models.RowHealthOK,
		AttentionText: "! lim", Attention: models.RowHealthNotReady,
	}}}
	h.w.SetFocus(ctx, true)
	_ = h.w.Init(ctx)
	var line string
	for _, l := range h.w.lines(140, 20) {
		if strings.Contains(ansi.Strip(l), "! lim") {
			line = l
		}
	}
	if !strings.Contains(ansi.Strip(line), "pods 2.4/10.4 Gi") {
		t.Fatalf("the pods line is not next to the bar: %q", ansi.Strip(line))
	}
	out := lipgloss.NewStyle().Background(lipgloss.Blue).Width(140).Render(line)
	before := out[:strings.Index(out, "pods 2.4")]
	reset := strings.LastIndex(before, "\x1b[m")
	if reset < 0 || !strings.Contains(before[reset:], "44") {
		t.Fatalf("the text after the red mark has no panel background: %q", out)
	}
}

// TestSparklineReadsAsAHistory checks that the label comes first and names
// the history, and that the sparkline is framed with the window it spans,
// followed by a blank line, so it does not read as part of the next bar.
func TestSparklineReadsAsAHistory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.summary = models.InfoSummary{UsageAt: h.now, Bars: bars(2, 23)}
	h.w.SetFocus(ctx, true)
	_ = h.w.Init(ctx)
	for range 3 {
		h.now = h.now.Add(10 * time.Second)
		h.last().summary.UsageAt = h.now
		_, _ = h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen})
	}
	lines := make([]string, 0, 40)
	for _, l := range h.w.lines(100, 60) {
		lines = append(lines, ansi.Strip(l))
	}
	view := strings.Join(lines, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "history, % of allocatable · 4 samples over 30s") {
			continue
		}
		if i+2 >= len(lines) || !strings.Contains(lines[i+1], "▕") || !strings.HasSuffix(lines[i+1], "▏ 45s of 15m, top 10%") || strings.TrimSpace(lines[i+2]) != "" {
			t.Fatalf("label is not followed by a framed sparkline and a blank line:\n%s", view)
		}
		return
	}
	t.Fatalf("no history label:\n%s", view)
}
