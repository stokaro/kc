package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// rereadKey reads the active panel again, as C-r does in mc. While the
// integrated terminal fills the screen, C-r belongs to the shell (history
// search); a command that holds the panel's focus gets it as well.
const rereadKey = "ctrl+r"

// rereadDoneTTL is how long the confirmation of a reread stays.
const rereadDoneTTL = time.Second

// rereadPanel reads again what panel idx shows (C-r, Left/Right > Reread):
// the folder of a list, the object of Describe and Manifest, the summary
// of Info. See docs/adr/0235-panel-reread.md.
func (a *App) rereadPanel(idx int) tea.Cmd {
	panel := a.panelByIndex(idx)
	if panel == nil {
		return nil
	}
	if panel.Frozen() {
		return a.ShowInfo("Frozen; C-x f returns to live data", 2*time.Second)
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	defer a.invalidateView("reread")
	switch panel.Mode() {
	case PanelModeList:
		if !rereadFolder(ctx, panel) {
			return a.ShowInfo(panelcontent.NothingToReread, rereadDoneTTL)
		}
		return a.ShowInfo("Reread", rereadDoneTTL)
	case PanelModeCommand:
		// Running a command again can change the cluster; its own keys
		// restart it.
		return a.ShowInfo("C-r does not run the command again", 2*time.Second)
	}
	if cmd, ok := panel.RereadContent(ctx); ok {
		return cmd
	}
	return a.ShowInfo(panelcontent.NothingToReread, rereadDoneTTL)
}

// rereadFolder reads the folder of a list panel again and repaints it. The
// cursor stays on the row with the same ID, or moves to its neighbor when
// the row is gone; marks stay on the rows that are still listed. It
// reports false when the panel shows no folder.
func rereadFolder(ctx context.Context, panel *Panel) bool {
	folder := panel.Folder()
	if folder == nil {
		return false
	}
	switch f := folder.(type) {
	case models.Rereader:
		f.Reread(ctx)
	case interface{ Refresh() }:
		f.Refresh()
	}
	panel.RefreshFolder(ctx)
	return true
}

// handleRereadDone confirms a finished reread, or shows why it failed.
func (a *App) handleRereadDone(msg panelcontent.RereadDoneMsg) tea.Cmd {
	a.invalidateView("reread done")
	if msg.Err != nil {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("Reread failed: %v", msg.Err)
		}
		return a.ShowToast("Reread failed: "+msg.Err.Error(), 5*time.Second)
	}
	if msg.Note != "" {
		return a.ShowInfo(msg.Note, 2*time.Second)
	}
	return a.ShowInfo("Reread", rereadDoneTTL)
}

// RereadContent asks the active widget of a Describe, Manifest, Logs or
// Info panel to read its content again. ok is false when the widget cannot.
func (p *Panel) RereadContent(ctx context.Context) (tea.Cmd, bool) {
	widget := p.ensureActiveWidget(ctx)
	r, ok := widget.(panelcontent.Rereader)
	if !ok {
		return nil, false
	}
	p.invalidateRenderCache()
	return r.Reread(ctx), true
}
