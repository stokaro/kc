package logs_test

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/logview"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// TestPanelDropsOldestLines feeds more lines than the panel keeps, one per
// read, while the user reads the oldest ones.
func TestPanelDropsOldestLines(t *testing.T) {
	t.Parallel()
	body := newChanBody()
	src := newFakeSource(map[string]*chanBody{"pod-a": body})
	w := logswidget.New(panelcontent.WidgetDeps{}, logswidget.Options{
		Source: src.Source,
		After:  immediate,
		Limits: logview.Limits{MaxRecords: 20},
	})
	w.Resize(t.Context(), panelcontent.Size{Width: 80, Height: 10})
	next := open(t, w, podItem("ns", "pod-a"))
	feed := func(from, to int) {
		t.Helper()
		for i := from; i < to; i++ {
			body.lines <- fmt.Sprintf("line %d\n", i)
			next = update(t, w, run(t, next))
		}
	}
	info := func() panelcontent.FrameInfo {
		return w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{})
	}

	feed(0, 15)
	if got := info().FooterStatus; strings.Contains(got, "dropped") {
		t.Fatalf("FooterStatus = %q before any line was dropped", got)
	}
	// Scroll to the oldest line: the panel pauses.
	update(t, w, tea.KeyPressMsg{Code: tea.KeyHome})
	if got := screenLines(t, w); len(got) == 0 || got[0] != "line 0" {
		t.Fatalf("home did not show the oldest line: %q", got)
	}

	// Line 20 passes the limit: the oldest tenth (lines 0-2) goes, and
	// with it the line at the top of the view.
	feed(15, 21)
	if got := screenLines(t, w); len(got) == 0 || got[0] != "line 3" {
		t.Fatalf("expected the view to start at line 3, got %q", got)
	}
	if got := info(); got.FooterStatus == "" || !strings.HasSuffix(got.FooterStatus, "• 3 older dropped") {
		t.Fatalf("FooterStatus = %q, want the dropped count", got.FooterStatus)
	}
	if got := info().HeaderStatus; !strings.Contains(got, "its lines were dropped") {
		t.Fatalf("HeaderStatus = %q, want the moved-view notice", got)
	}
	// The next scroll clears the notice; the count stays.
	update(t, w, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := info().HeaderStatus; got != "paused, End follows" {
		t.Fatalf("HeaderStatus after a scroll = %q", got)
	}

	// Following, the panel never holds more than the limit.
	update(t, w, tea.KeyPressMsg{Code: tea.KeyEnd})
	feed(21, 200)
	_, total := lastPosition(info().FooterStatus)
	if total > 20 {
		t.Fatalf("panel holds %d lines, over the limit of 20", total)
	}
	if got := screen(t, w); !strings.Contains(got, "line 199") {
		t.Fatalf("newest line not shown:\n%s", got)
	}
	if got := info().HeaderStatus; got != "" {
		t.Fatalf("HeaderStatus while following = %q", got)
	}
}

// screenLines returns the non-empty screen rows without padding.
func screenLines(t *testing.T, w *logswidget.Widget) []string {
	t.Helper()
	var out []string
	for row := range strings.SplitSeq(screen(t, w), "\n") {
		if row = strings.TrimSpace(row); row != "" {
			out = append(out, row)
		}
	}
	return out
}

// lastPosition parses "current/total • ..." of the panel footer.
func lastPosition(status string) (current, total int) {
	_, _ = fmt.Sscanf(status, "%d/%d", &current, &total)
	return current, total
}
