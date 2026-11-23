package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The C-x w dialog shows its whole description, the example and the time
// zone included, on an 80-column terminal and on a wide one.
func TestWindowDialogShowsTheWholeDescription(t *testing.T) {
	t.Parallel()
	zone := time.Now().Format("-07:00")
	for _, width := range []int{80, 160} {
		a := NewApp()
		t.Cleanup(a.cancel)
		a.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		a.showWindowDialog()
		screen := ansi.Strip(viewString(a.View()))
		// Words wrapped onto the next dialog line are joined back.
		var text []string
		for line := range strings.SplitSeq(screen, "\n") {
			text = append(text, strings.Trim(line, " │║|"))
		}
		joined := strings.Join(strings.Fields(strings.Join(text, " ")), " ")
		for _, want := range []string{
			"15m or 2h (live)", "START .. END", "such as 10:00 .. 11:30", "(time zone " + zone + ")",
			"empty turns it off", "Logs, Info and the dashboard use it.",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("width %d: the dialog misses %q:\n%s", width, want, screen)
			}
		}
	}
}
