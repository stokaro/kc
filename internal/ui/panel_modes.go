package ui

import (
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// PanelViewMode identifies the active widget/view embedded inside a panel.
type PanelViewMode int

const (
	PanelModeList PanelViewMode = iota
	PanelModeDescribe
	PanelModeManifest
	PanelModeCommand
	// PanelModeLogs streams the log of the other panel's selection.
	PanelModeLogs
	// PanelModeInfo shows bars for the load of the other panel's scope, like
	// mc's Info panel.
	PanelModeInfo
)

// PanelWidget abstracts panel content implementations.
type PanelWidget = panelcontent.Widget

// PanelWidgetFactory constructs a widget bound to the provided panel shell.
type PanelWidgetFactory func(*Panel) panelcontent.Widget

var panelModeOrder = []PanelViewMode{PanelModeList, PanelModeDescribe, PanelModeManifest, PanelModeLogs, PanelModeInfo, PanelModeCommand}

// PanelMouseType identifies mouse intents routed to widgets.
type PanelMouseType = panelcontent.MouseIntent

const (
	PanelMouseClick PanelMouseType = panelcontent.MouseIntentClick
	PanelMouseWheel PanelMouseType = panelcontent.MouseIntentWheel
)

// PanelMouseMsg conveys mouse events with panel-relative context.
type PanelMouseMsg = panelcontent.MouseMsg

// NextPanelMode returns the next mode in the default ordering.
func NextPanelMode(current PanelViewMode) PanelViewMode {
	for i, mode := range panelModeOrder {
		if mode == current {
			return panelModeOrder[(i+1)%len(panelModeOrder)]
		}
	}
	return panelModeOrder[0]
}

// PanelModeOrder returns the canonical ordering of panel modes.
func PanelModeOrder() []PanelViewMode {
	return append([]PanelViewMode(nil), panelModeOrder...)
}

// modeLabel is the name of a mode in the panel border, the panel options
// and the menu bar.
func modeLabel(mode PanelViewMode) string {
	switch mode {
	case PanelModeList:
		return "List"
	case PanelModeDescribe:
		return "Describe"
	case PanelModeManifest:
		return "Manifest"
	case PanelModeLogs:
		return "Logs"
	case PanelModeInfo:
		return "Info"
	case PanelModeCommand:
		return "Command"
	default:
		return "Unknown"
	}
}
