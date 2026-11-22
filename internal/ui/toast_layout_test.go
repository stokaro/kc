package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A toast replaces the function key bar, so the panels keep their rows and
// mouse hit-testing must not shift while it is shown.
func TestToastDoesNotMovePanels(t *testing.T) {
	a := &App{width: 120, height: 30, leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}
	without := a.panelsHeight()
	a.toastActive = true
	with := a.panelsHeight()
	if with != without {
		t.Fatalf("panel height %d with a toast, %d without", with, without)
	}
}

func TestToastMessagesPassDialogs(t *testing.T) {
	if !passesModal(showToastMsg{}) || !passesModal(toastTickMsg{}) {
		t.Fatalf("toast messages must reach the app while a dialog is open")
	}
}

// A toast covers the command line and leaves the function keys visible;
// with the command line hidden it falls back to the function bar.
func TestToastGoesToCommandLine(t *testing.T) {
	a := &App{width: 80, height: 30, toastActive: true, toastText: "Cannot switch"}
	if line := ansi.Strip(a.renderCommandLine()); !strings.Contains(line, "Cannot switch") {
		t.Fatalf("command line = %q, want the toast", line)
	}
	if a.functionBarState().ToastActive {
		t.Fatalf("the function bar must keep its keys while the command line shows the toast")
	}
	a.showTerminal = true
	if !a.functionBarState().ToastActive {
		t.Fatalf("with the command line hidden the toast belongs to the function bar")
	}
}
