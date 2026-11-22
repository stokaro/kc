package list

import (
	"context"
	"image/color"

	"charm.land/lipgloss/v2"

	models "github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// displayList decorates folder rows for display. Rows that cannot be entered
// are drawn like regular files in Midnight Commander: indented by one space so
// their names line up with "/dir" entries, and in a dimmer color than folders
// so it is clear there is nothing below them.
type displayList struct {
	table.List
}

func newDisplayList(l table.List) table.List {
	if l == nil {
		return nil
	}
	return displayList{List: l}
}

func (l displayList) Lines(ctx context.Context, top, num int) []table.Row {
	return decorateRows(l.List.Lines(ctx, top, num))
}

func (l displayList) Above(ctx context.Context, rowID string, num int) []table.Row {
	return decorateRows(l.List.Above(ctx, rowID, num))
}

func (l displayList) Below(ctx context.Context, rowID string, num int) []table.Row {
	return decorateRows(l.List.Below(ctx, rowID, num))
}

func (l displayList) Find(ctx context.Context, rowID string) (int, table.Row, bool) {
	idx, row, ok := l.List.Find(ctx, rowID)
	if ok {
		row = decorateRow(row)
	}
	return idx, row, ok
}

// decorateRows returns decorated copies. Folders may hand out their internal
// slice, so it must not be modified in place.
func decorateRows(rows []table.Row) []table.Row {
	out := make([]table.Row, len(rows))
	for i := range rows {
		out[i] = decorateRow(rows[i])
	}
	return out
}

func decorateRow(row table.Row) table.Row {
	if row == nil || !isLeafRow(row) {
		return row
	}
	return leafRow{Row: row}
}

func isLeafRow(row table.Row) bool {
	if _, ok := row.(models.Enterable); ok {
		return false
	}
	// A link to a live object carries its own "@" marker, like a symlink
	// in mc.
	if _, ok := row.(models.ObjectLink); ok {
		return false
	}
	if back, ok := row.(models.Back); ok && back.IsBack() {
		return false
	}
	return true
}

type leafRow struct {
	table.Row
}

var leafStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(uistyles.ColorGrey))

//revive:disable-next-line:function-result-limit the signature is the table.Row interface
func (r leafRow) Columns() (string, []string, []*lipgloss.Style, bool) {
	id, cells, styles, ok := r.Row.Columns()
	if len(cells) == 0 {
		return id, cells, styles, ok
	}
	cells = append([]string(nil), cells...)
	cells[0] = " " + cells[0]
	styles = append([]*lipgloss.Style(nil), styles...)
	for len(styles) < len(cells) {
		styles = append(styles, nil)
	}
	for i, st := range styles {
		if isDefaultRowStyle(st) {
			styles[i] = &leafStyle
		}
	}
	return id, cells, styles, ok
}

var defaultRowForeground = models.WhiteStyle().GetForeground()

// isDefaultRowStyle reports whether a cell uses the plain row style. Cells with
// a status color (not ready, deleting, ...) keep their own color.
func isDefaultRowStyle(st *lipgloss.Style) bool {
	if st == nil {
		return true
	}
	fg := st.GetForeground()
	if _, unset := fg.(lipgloss.NoColor); unset {
		return true
	}
	return sameColor(fg, defaultRowForeground)
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
