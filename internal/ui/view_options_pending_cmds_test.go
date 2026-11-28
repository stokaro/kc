package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

type pendingProbeMsg struct{}

// Commands queued for the next Update must run when that Update is a View
// Options commit; the commit branch used to return only its own commands.
func TestViewOptionsCommitKeepsPendingCommands(t *testing.T) {
	cases := []struct {
		name string
		msg  ViewOptionsCommittedMsg
	}{
		{"panel options", ViewOptionsCommittedMsg{PanelIndex: 0, Accept: true, Close: true}},
		{"viewer options", ViewOptionsCommittedMsg{PanelIndex: -1, Viewer: &ViewOptionsViewerPayload{}, Accept: true, Close: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := NewApp()
			app.width, app.height = 120, 30
			app.Update(tea.WindowSizeMsg{Width: app.width, Height: app.height})
			app.showModal("view_options")
			app.enqueueCmd(func() tea.Msg { return pendingProbeMsg{} })

			_, cmd := app.Update(tc.msg)
			if len(collectMsgs[pendingProbeMsg](cmd)) == 0 {
				t.Fatal("the queued command was dropped")
			}
		})
	}
}

// A command queued from another goroutine runs with the next message,
// whatever case of Update handles it. A discovery refresh returned only
// its next watch, and a copy result only its toast, and the queued
// command was lost.
func TestUpdateKeepsPendingCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  tea.Msg
	}{
		{"discovery refreshed", DiscoveryRefreshedMsg{}},
		{"copy result", copyResultMsg{text: "Copied"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := NewApp()
			t.Cleanup(app.cancel)
			app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			app.enqueueCmd(func() tea.Msg { return pendingProbeMsg{} })

			_, cmd := app.Update(tc.msg)
			if got := len(collectMsgs[pendingProbeMsg](cmd)); got != 1 {
				t.Fatalf("the queued command ran %d times with %T, want once", got, tc.msg)
			}
			// It ran once: the next message does not run it again.
			_, cmd = app.Update(tc.msg)
			if got := len(collectMsgs[pendingProbeMsg](cmd)); got != 0 {
				t.Fatalf("the queued command ran again with the next %T", tc.msg)
			}
		})
	}
}
