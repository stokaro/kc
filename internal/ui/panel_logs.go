package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	panelcontent "github.com/sttts/kc/internal/ui/panelcontent"
	logswidget "github.com/sttts/kc/internal/ui/panelcontent/logs"
)

// logsWidget returns the panel's Logs widget, nil before Logs mode was used.
func (p *Panel) logsWidget() *logswidget.Widget {
	w, _ := p.widgets[PanelModeLogs].(*logswidget.Widget)
	return w
}

// FollowLogs points the Logs widget at the other panel's selection. It does
// nothing unless the panel is in Logs mode.
func (p *Panel) FollowLogs(ctx context.Context, sel panelcontent.Selection) tea.Cmd {
	if p.mode != PanelModeLogs {
		return nil
	}
	w, ok := p.ensureActiveWidget(ctx).(*logswidget.Widget)
	if !ok {
		return nil
	}
	p.invalidateRenderCache()
	return w.Follow(ctx, sel)
}

// deliverLogs hands a background message to the Logs widget, active or not:
// an inactive widget drops it, which ends a stale read loop.
func (p *Panel) deliverLogs(ctx context.Context, msg logswidget.Msg) tea.Cmd {
	w := p.logsWidget()
	if w == nil {
		return nil
	}
	cmd, handled := w.Update(ctx, msg)
	if handled {
		p.invalidateRenderCache()
	}
	return cmd
}
