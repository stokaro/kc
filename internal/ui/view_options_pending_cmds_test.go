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
