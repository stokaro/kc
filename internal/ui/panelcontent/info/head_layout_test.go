package info

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/investigation"
	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// TestHeadLinesWrap checks the head of Info in a narrow pane (ADR 0176):
// the scope, a stale usage age and the window coverage wrap under their
// indent instead of being cut, and every line of the stale age keeps its
// color.
func TestHeadLinesWrap(t *testing.T) {
	t.Parallel()
	const width = 30
	now := harnessNow
	target := Target{Key: "k", Label: "ctx: namespace a-rather-long-namespace-name", Open: func() Summarizer {
		return &fakeSource{summary: models.InfoSummary{UsageAt: now.Add(-10 * time.Minute), UsageReadAt: now, Bars: bars(10, 20)}}
	}}
	w := New(panelcontent.WidgetDeps{}, Options{
		Resolve: func(context.Context) Target { return target },
		Now:     func() time.Time { return now },
		Window:  func() (investigation.Window, *time.Location) { return investigation.Window{Last: time.Hour}, time.UTC },
	})
	w.SetFocus(t.Context(), true)
	_ = w.Init(t.Context())

	all := w.content(width)
	var text []string
	stale := 0
	for i, l := range all {
		if got := ansi.StringWidth(l.text); got > width {
			t.Errorf("line %d is %d cells wide: %q", i, got, ansi.Strip(l.text))
		}
		plain := ansi.Strip(l.text)
		text = append(text, plain)
		// The head is the scope (two lines here), then the usage age.
		if i >= 2 && i < 5 && plain != l.text {
			stale++
		}
	}
	joined := strings.Join(text, "\n")
	// Wrapping may break inside a long word, so compare without spaces.
	flat := strings.Join(strings.Fields(joined), "")
	for _, want := range []string{
		"ctx: namespace a-rather-long-namespace-name",
		"usage measured 10m ago: stale, metrics-server returns old samples, read 0s ago",
		// One sample, ten minutes old: it starts late and no longer covers the end.
		"window last 1h: session usage history from 11:50:00 only, stale since 11:50:00 (incomplete)",
	} {
		if !strings.Contains(flat, strings.Join(strings.Fields(want), "")) {
			t.Errorf("head misses %q:\n%s", want, joined)
		}
	}
	if stale != 3 {
		t.Errorf("the stale usage age is colored on %d lines, want each of its wrapped lines:\n%s", stale, joined)
	}
}
