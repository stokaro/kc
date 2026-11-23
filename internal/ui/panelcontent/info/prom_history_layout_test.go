package info

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// TestPromHistoryWrapsAndScrolls checks the Prometheus block in the pane of
// an 80x24 terminal (ADR 0176): its lines wrap with their indent instead of
// being cut, and End scrolls to them.
func TestPromHistoryWrapsAndScrolls(t *testing.T) {
	t.Parallel()
	const width, height = 38, 8
	h, f := promHarness(t)
	ctx := t.Context()
	_, _ = h.w.Update(ctx, tea.WindowSizeMsg{Width: width, Height: height})
	runPromRead(t, h.w, h.w.OnSelectionChanged(ctx, panelcontent.Selection{}))
	f.err = errors.New("prometheus monitoring/prom:9090: HTTP 503: no endpoints available")
	h.now = h.now.Add(3 * promHistoryRetry)
	runPromRead(t, h.w, h.w.OnSelectionChanged(ctx, panelcontent.Selection{}))

	var text []string
	for i, l := range h.w.content(width) {
		if w := ansi.StringWidth(l.text); w > width {
			t.Errorf("line %d is %d cells wide: %q", i, w, ansi.Strip(l.text))
		}
		text = append(text, ansi.Strip(l.text))
	}
	all := strings.Join(text, "\n")
	flat := strings.Join(strings.Fields(all), " ")
	for _, want := range []string{
		"Prometheus history, last 1h (monitoring/prom:9090)",
		"unavailable: prometheus monitoring/prom:9090: HTTP 503: no endpoints available",
		"7 pods by owner series", "max 1.50",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("content misses %q:\n%s", want, all)
		}
	}
	// A wrapped line keeps the indent of the line it continues.
	if !strings.Contains(all, "\n   unavailable: prometheus\n   monitoring/prom:9090") {
		t.Errorf("the error line does not wrap under its indent:\n%s", all)
	}

	_, _ = h.w.handleKey("end")
	if view := strings.Join(h.w.lines(width, height), "\n"); !strings.Contains(ansi.Strip(view), "max 512Mi") && !strings.Contains(ansi.Strip(view), "Top pods") {
		t.Fatalf("End did not reach the end of the content:\n%s", ansi.Strip(view))
	}
	if info := h.w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: width}); info.TopIndicator != "^" {
		t.Fatalf("frame after End: %+v", info)
	}
}
