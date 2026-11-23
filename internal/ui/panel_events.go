package ui

import panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"

// PanelSelectionChangedMsg is emitted when the selection within a panel changes.
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
