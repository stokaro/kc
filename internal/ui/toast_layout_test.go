package ui

import "testing"

// A toast replaces the function key bar, so the panels keep their rows and
// mouse hit-testing must not shift while it is shown.
func TestToastDoesNotMovePanels(t *testing.T) {
	a := &App{width: 120, height: 30, leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}
	_, _, without, _ := a.panelAreaMetrics()
	a.toastActive = true
	_, _, with, _ := a.panelAreaMetrics()
	if with != without {
		t.Fatalf("panel height %d with a toast, %d without", with, without)
	}
}

func TestToastMessagesPassDialogs(t *testing.T) {
	if !passesModal(showToastMsg{}) || !passesModal(toastTickMsg{}) {
		t.Fatalf("toast messages must reach the app while a dialog is open")
	}
}
