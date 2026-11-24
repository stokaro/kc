package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestDialogDrawsThePanelsLive: a windowed dialog draws the panels as they
// are now, not as they were when it opened. The Info panel beside a drain
// dialog said "loading" and listed the evicted pods until the dialog
// closed, because the dialog kept a picture of the screen from its start.
func TestDialogDrawsThePanelsLive(t *testing.T) {
	t.Parallel()
	a := newTransferApp(t)
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	view := newDrainProgressModel([]string{"kind-worker"}, func() {})
	modal := NewModal("Drain", view)
	a.modalManager.Register(nodeDrainModal, modal)
	w, h := view.PreferredSize(a.width, a.height)
	a.configureModalWindow(modal, view, a.activePanel, "", w+2, h+2)
	a.showModal(nodeDrainModal)
	const news = "node kind-worker drained"
	if before := dialogScreen(t, a); strings.Contains(before, news) {
		t.Fatalf("the message is on the screen before it was sent:\n%s", before)
	}

	// The screen behind the dialog changes while the dialog stays open.
	a.Update(showToastMsg{text: news, ttl: time.Minute, info: true})
	requireDrawn(t, dialogScreen(t, a), news, "Drain")
}
