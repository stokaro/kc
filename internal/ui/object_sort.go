package ui

import (
	"context"
	"slices"
	"strings"
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
