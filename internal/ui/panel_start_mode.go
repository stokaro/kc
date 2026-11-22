package ui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/pkg/appconfig"
)

// panelStartMode maps a layout.leftPanelMode / layout.rightPanelMode value
// to the panel mode. The config loader rejects unknown values, so the
// fallback to List only covers a Config built in code.
func panelStartMode(m appconfig.PanelStartMode) PanelViewMode {
	switch m {
	case appconfig.PanelStartDescribe:
		return PanelModeDescribe
	case appconfig.PanelStartManifest:
		return PanelModeManifest
	case appconfig.PanelStartLogs:
		return PanelModeLogs
	case appconfig.PanelStartInfo:
		return PanelModeInfo
	default:
		return PanelModeList
	}
}

// initPanels starts both panels for Init: the list widgets first, then the
// start modes replace them where the layout asks for another mode, as
// C-1 / C-2 would. It runs after the config is loaded.
func (a *App) initPanels() tea.Cmd {
	leftInit, rightInit := a.leftPanel.Init(), a.rightPanel.Init()
	return tea.Batch(leftInit, rightInit, a.applyStartPanelModes())
}

// applyStartPanelModes puts each panel in the mode the layout config names.
// kc does not save panel modes, so nothing restored competes with it. Start targets (kc get, kc logs, kc overview) navigate
// the left panel afterwards and leave the right panel's mode alone, except
// kc get TYPE NAME -o yaml, which switches it to Manifest.
func (a *App) applyStartPanelModes() tea.Cmd {
	if a.cfg == nil {
		return nil
	}
	modes := [2]PanelViewMode{
		panelStartMode(a.cfg.Layout.LeftPanelMode),
		panelStartMode(a.cfg.Layout.RightPanelMode),
	}
	var cmds []tea.Cmd
	for idx, mode := range modes {
		panel := a.panelByIndex(idx)
		if panel == nil || panel.Mode() == mode {
			continue
		}
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		cmds = append(cmds, a.setPanelMode(ctx, panel, mode, fmt.Sprintf("panel %d start mode", idx)))
		cancel()
	}
	return tea.Batch(cmds...)
}
