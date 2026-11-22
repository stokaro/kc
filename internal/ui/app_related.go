package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/models"
)

// Keys that open the owners and used-by folders of the selected object.
// Esc j and Esc b do the same where Option does not act as Meta.
const (
	ownersKey = "alt+j"
	usedByKey = "alt+b"
)

// relatedFolderMsg carries a built owners or used-by folder back to the UI
// thread.
type relatedFolderMsg struct {
	panelIdx int
	from     models.Folder // folder the key was pressed in
	selID    string        // row the key was pressed on
	folder   models.Folder
	firstID  string // row to put the cursor on, empty for none
	info     string // shown instead of opening a folder
	err      error
}

// openRelated builds the owners or used-by folder of the selected object off
// the UI thread; handleRelatedFolder opens it.
func (a *App) openRelated(kind models.RelatedKind) tea.Cmd {
	panel := a.activePanelRef()
	if panel == nil {
		return nil
	}
	from := panel.Folder()
	src, ok := from.(models.RelatedSource)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := panel.SelectedNavItem(ctx)
	cancel()
	if !ok {
		return nil
	}
	obj, ok := item.(models.ObjectItem)
	if !ok || obj.GVR().Empty() || obj.Name() == "" {
		return a.ShowInfo("Select an object to see its owners or users", 2*time.Second)
	}
	selID, _, _, _ := obj.Columns()
	panelIdx := a.activePanel
	label := "Reading owners"
	if kind == models.RelatedUsedBy {
		label = "Finding users"
	}
	root := a.ctx
	return a.withBusy(label, 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, requestTimeout)
		defer cancel()
		msg := relatedFolderMsg{panelIdx: panelIdx, from: from, selID: selID}
		folder, err := src.RelatedFolder(ctx, kind, obj)
		what := obj.GVR().Resource + "/" + obj.Name()
		switch {
		case errors.Is(err, models.ErrNoOwners):
			msg.info = what + " has no owner"
			return msg
		case errors.Is(err, models.ErrNoUsedBy):
			msg.info = "kc does not know what uses " + obj.GVR().Resource
			return msg
		case err != nil:
			msg.err = fmt.Errorf("%s: %w", what, err)
			return msg
		}
		// Load the rows here, off the UI thread, so the cursor can go to
		// the first one as soon as the folder opens.
		folder.Len(ctx)
		for _, row := range folder.Lines(ctx, 0, 2) {
			if back, ok := row.(models.Back); ok && back.IsBack() {
				continue
			}
			msg.firstID, _, _, _ = row.Columns()
			break
		}
		msg.folder = folder
		return msg
	})
}

// handleRelatedFolder opens a folder built by openRelated in the panel the
// key was pressed in, unless the user moved on meanwhile.
func (a *App) handleRelatedFolder(msg relatedFolderMsg) tea.Cmd {
	panel := a.panelByIndex(msg.panelIdx)
	stale := panel == nil || a.activePanel != msg.panelIdx || panel.Folder() != msg.from
	if msg.err != nil {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("%v", msg.err)
		}
		return nil
	}
	if msg.info != "" {
		return a.ShowInfo(msg.info, 2*time.Second)
	}
	if stale {
		ctrllog.FromContext(a.ctx).WithName("related").V(1).Info("dropping related folder, panel changed", "panel", msg.panelIdx)
		if c, ok := msg.folder.(interface{ Close() }); ok {
			c.Close()
		}
		return nil
	}
	a.handleFolderNav(false, msg.selID, msg.folder)
	a.selectInOpenedFolder(panel, msg.firstID)
	return nil
}

// selectInOpenedFolder moves the cursor to a row of the folder just opened.
// openRelated loaded the rows already; a navigate-to-path routine that waits
// for late rows can replace this call.
func (a *App) selectInOpenedFolder(panel *Panel, id string) {
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	panel.SelectByRowID(ctx, id)
	cancel()
}
