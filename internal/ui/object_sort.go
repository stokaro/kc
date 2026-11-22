package ui

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/ui/menubar"
)

// Sort keys for object lists. mc binds no sort keys by default; these follow
// kc's Meta keys (M-c, M-s, M-w), each with an Esc fallback.
const (
	sortNextKey    = "alt+o" // sort by the next column, ascending
	sortReverseKey = "alt+r" // reverse the sort direction
)

// nextSortOrder returns the order for the column after the sorted one,
// ascending, wrapping after the last column.
func nextSortOrder(keys []string, applied string) string {
	if len(keys) == 0 {
		return applied
	}
	cur := strings.TrimPrefix(applied, "-")
	i := slices.Index(keys, cur)
	return keys[(i+1)%len(keys)]
}

// reverseSortOrder flips the direction of applied.
func reverseSortOrder(_ []string, applied string) string {
	if rest, ok := strings.CutPrefix(applied, "-"); ok {
		return rest
	}
	return "-" + applied
}

// changeObjectSort applies next to the sort of the active panel's object
// list. It reports false when the panel shows no object list, so the key
// goes on to the shell line as before.
func (a *App) changeObjectSort(next func(keys []string, applied string) string) bool {
	panel := a.activePanelRef()
	if panel == nil || a.showTerminal {
		return false
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	st, ok := panel.SortState(ctx)
	if !ok {
		return false
	}
	panel.SetObjectOrder(ctx, next(st.Keys, st.Applied))
	a.syncPanelConfig(panel)
	a.invalidateView("object sort")
	return true
}

// toggleNamespaceGrouping switches the active panel between lists grouped by
// namespace and one list sorted across namespaces. It reports false when the
// panel shows no list that spans namespaces.
func (a *App) toggleNamespaceGrouping() bool {
	panel := a.activePanelRef()
	if panel == nil || a.showTerminal {
		return false
	}
	on, applies := panel.NamespaceGrouping()
	if !applies {
		return false
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	panel.SetNamespaceGrouping(ctx, !on)
	a.syncPanelConfig(panel)
	a.invalidateView("namespace grouping")
	return true
}

// groupNamespacesItem is the Left/Right entry that toggles the grouping by
// namespace; it is checked while the list is grouped.
func (a *App) groupNamespacesItem(idx int, list bool) menubar.Item {
	item := menubar.Item{Label: "Group &by namespace", Disabled: true}
	panel := a.panelByIndex(idx)
	if panel == nil || !list {
		return item
	}
	on, applies := panel.NamespaceGrouping()
	item.Checked = on
	item.Disabled = !applies
	item.Run = a.onPanel(idx, func() tea.Cmd {
		a.toggleNamespaceGrouping()
		return nil
	})
	return item
}
