package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/workloadaction"
)

// panelOriginMsg is a request a row of panel sent: a confirmation, a shell
// or attach session, a link, a workload action, the type catalog. The App
// acts on it in the panel's connection. Without the panel, the request
// would go to whatever panel is active when the message arrives, and a
// Tab typed meanwhile would retarget it to the other panel's cluster.
type panelOriginMsg struct {
	panel *Panel
	msg   tea.Msg
}

// stampOrigin wraps cmd, a command a widget of p returned, so the row
// requests among its messages carry p.
func (p *Panel) stampOrigin(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg { return stampMsg(p, cmd()) }
}

func stampMsg(p *Panel, msg tea.Msg) tea.Msg {
	switch m := msg.(type) {
	case tea.BatchMsg:
		out := make(tea.BatchMsg, len(m))
		for i, c := range m {
			out[i] = p.stampOrigin(c)
		}
		return out
	case models.ConfirmRequest, models.ExecRequest, models.OpenObjectRequest, models.OpenTypeCatalogRequest, workloadaction.Request:
		return panelOriginMsg{panel: p, msg: m}
	}
	return msg
}

// handlePanelOrigin runs a stamped row request in its panel. A panel that
// is gone (never, today) leaves the request to the active panel.
func (a *App) handlePanelOrigin(m panelOriginMsg) tea.Cmd {
	idx, ok := a.panelIndexFor(m.panel)
	if !ok {
		idx = a.activePanel
	}
	panel := a.panelByIndex(idx)
	switch req := m.msg.(type) {
	case models.ConfirmRequest:
		return a.showConfirm(a.targetConn(idx), req.Action)
	case models.ExecRequest:
		if panel == nil {
			return nil
		}
		return a.resolveExecTarget(panel.Folder(), req.Item, req.Mode)
	case models.OpenObjectRequest:
		return a.openLinkedObjectFrom(idx, req.Link)
	case models.OpenTypeCatalogRequest:
		return a.showTypeCatalog(idx, req.OtherOnly)
	case workloadaction.Request:
		return a.startWorkloadAction(req)
	}
	return nil
}
