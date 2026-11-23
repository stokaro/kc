package ui

// White-box like the rest of package ui: the modal window and the App's
// dialog stack are unexported.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestDialogTitleFitsItsFrame checks a title longer than its dialog: the
// window widens to fit it when the screen has room, and the title is cut
// with "…" when it has not. Either way the top border ends in its corner
// and is as wide as the rest of the frame.
func TestDialogTitleFitsItsFrame(t *testing.T) {
	t.Parallel()
	title := "Hotlist: Enter go, Ins add, F2 save view, F4 update view, F6 rename, Del remove"
	tests := []struct {
		name   string
		screen int
		whole  bool
	}{
		{name: "screen wide enough", screen: 150, whole: true},
		{name: "narrow screen", screen: 60},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewModal(title, NewHelpViewer(""))
			m.SetDimensions(tc.screen, 30)
			bg := lipgloss.NewStyle().Width(tc.screen).Height(30).Render("")
			m.SetWindowed(40, 8, bg)
			m.Show()
			lines := strings.Split(ansi.Strip(viewString(m.View())), "\n")
			win := m.drawn
			top := ansi.Cut(lines[win.Y], win.X, win.X+win.Width)
			if !strings.HasPrefix(top, "┌") || !strings.HasSuffix(top, "┐") {
				t.Fatalf("top border %q does not end in its corners", top)
			}
			if got := strings.Contains(top, title); got != tc.whole {
				t.Errorf("whole title shown = %v, want %v: %q", got, tc.whole, top)
			}
			if !tc.whole && !strings.Contains(top, "…") {
				t.Errorf("cut title has no ellipsis: %q", top)
			}
			if bottom := ansi.Cut(lines[win.Y+win.Height-1], win.X, win.X+win.Width); !strings.HasPrefix(bottom, "└") || !strings.HasSuffix(bottom, "┘") {
				t.Errorf("bottom border %q is not as wide as the top", bottom)
			}
		})
	}
}

// TestHelpOverADialogHidesTheDialog checks that the help window opened in a
// dialog draws over the panels alone, so no strips of the dialog and its
// shadow stay around it, and that closing the help shows the dialog again.
func TestHelpOverADialogHidesTheDialog(t *testing.T) {
	t.Parallel()
	a := helpTestApp(t, 160, 40)
	a.showWindowDialog()
	if !strings.Contains(strings.Join(helpScreen(a), "\n"), "15m or 2h") {
		t.Fatal("the investigation window dialog does not show")
	}
	// The dialog's top-left corner lies outside the help window.
	win := a.modalManager.modals[windowModalKey].drawn
	corner := func() string {
		return ansi.Cut(helpScreen(a)[win.Y], win.X, win.X+1)
	}
	if got := corner(); got != "┌" {
		t.Fatalf("dialog corner = %q before the help, want ┌", got)
	}
	helpPress(a, tea.KeyF1)
	if got := corner(); got == "┌" {
		t.Fatalf("the dialog shows behind the help:\n%s", strings.Join(helpScreen(a), "\n"))
	}
	helpPress(a, tea.KeyEscape)
	if screen := strings.Join(helpScreen(a), "\n"); !strings.Contains(screen, "[< OK >]") {
		t.Fatalf("the dialog does not show after the help closed:\n%s", screen)
	}
}
