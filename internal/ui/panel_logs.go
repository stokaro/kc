package ui

import (
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// logsWidget returns the panel's Logs widget, nil before Logs mode was used.
func (p *Panel) logsWidget() *logswidget.Widget {
	w, _ := p.widgets[PanelModeLogs].(*logswidget.Widget)
	return w
}
