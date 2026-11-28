package ui

import panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"

// PanelSelectionChangedMsg reports Selection as Panel's selection to both
// panels. The App reports a moved list by itself after every message
// (App.reportMovedSelections); this message reports a selection that is
// not the row under the list's cursor, and it stands until the list moves.
type PanelSelectionChangedMsg struct {
	Panel     *Panel
	Selection panelcontent.Selection
}

// PanelModeSelectedMsg switches a panel to a mode. The panel options send
// it when their "Panel mode" row is applied.
type PanelModeSelectedMsg struct {
	PanelIndex int
	Mode       PanelViewMode
}
