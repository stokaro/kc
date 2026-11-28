package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// selectionKey tells one list selection from another: the connection and
// namespace the list was read in, its path and the row under its cursor.
// The row ID alone is not enough: ".." and an object's name repeat in the
// same folder of another namespace or context.
type selectionKey struct {
	conn, scope, path, row string
}

// reportMovedSelections tells the other panel about every List panel
// whose selection moved since the last report, whatever moved it: a cursor
// key, a cd, a namespace or context switch, the history, the hotlist or a
// refresh that removed the row under the cursor. Update runs it after
// every message, so a follower hears about a move in the Update that made
// it and its frame and content change together (ADR 0377).
func (a *App) reportMovedSelections() tea.Cmd {
	if a.ctx == nil || a.showTerminal {
		// The panels are hidden behind the full-screen terminal; the
		// first Update after it closes reports what moved meanwhile.
		return nil
	}
	var cmds []tea.Cmd
	for idx := range 2 {
		panel := a.panelByIndex(idx)
		if panel == nil || panel.Mode() != PanelModeList {
			continue
		}
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		key := a.listSelectionKey(ctx, idx, panel)
		if key == panel.reported {
			cancel()
			continue
		}
		sel := panel.listSelection(ctx, key.row)
		cancel()
		panel.reported = key
		cmds = append(cmds, a.reportSelection(panel, sel))
	}
	if len(cmds) == 0 && !a.takeOriginMoves() {
		return nil
	}
	// A follower's frame names the scope of the panel it follows, or where
	// the object it keeps was read; set it in the same Update as the
	// content. The shell line of an active follower runs there too.
	a.syncPanelScopeLabels()
	a.ensureTermContext()
	if len(cmds) == 0 {
		return nil
	}
	a.invalidateFunctionBar("selection changed")
	a.invalidateView("selection changed")
	return tea.Batch(cmds...)
}

// takeOriginMoves reports whether a follower showed an object of another
// origin since the last call: a mode change or an unpin may have moved it
// without a report.
func (a *App) takeOriginMoves() bool {
	moved := false
	for idx := range 2 {
		if p := a.panelByIndex(idx); p != nil && p.originMoved {
			p.originMoved = false
			moved = true
		}
	}
	return moved
}

// listSelectionKey is the key of the selection of panel idx's own list.
func (a *App) listSelectionKey(ctx context.Context, idx int, panel *Panel) selectionKey {
	return selectionKey{
		conn:  a.ownConn(idx).connID(),
		scope: a.panelScope[idx],
		path:  panel.GetCurrentPath(),
		row:   panel.currentSelectionID(ctx),
	}
}

// reportSelection tells panel and the other panel that sel is panel's
// selection. The returned command loads what their widgets show.
func (a *App) reportSelection(panel *Panel, sel panelcontent.Selection) tea.Cmd {
	notify := func(p *Panel) tea.Cmd {
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		defer cancel()
		return p.NotifySelection(ctx, sel)
	}
	cmds := []tea.Cmd{notify(panel)}
	if idx, ok := a.panelIndexFor(panel); ok {
		if other := a.panelByIndex(1 - idx); other != nil {
			cmds = append(cmds, notify(other))
		}
	}
	return tea.Batch(cmds...)
}
