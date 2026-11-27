package list

import (
	"context"
	"fmt"
	"path"
	"strings"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// The group keys of mc: C-a marks every row, "*" inverts the marks, "+"
// and "-" ask for a pattern and mark or unmark the rows whose name matches
// it. In a folder the marks live in the table; the ".." row is never
// marked.

// markableRow is a folder row the group keys act on.
type markableRow struct {
	id   string
	name string
}

// markableRows returns the rows of the folder that can carry a mark.
func (w *Widget) markableRows(ctx context.Context) []markableRow {
	rows := w.folderLines(ctx, 0, w.folderLen(ctx))
	out := make([]markableRow, 0, len(rows))
	for _, row := range rows {
		if back, ok := row.(models.Back); ok && back.IsBack() {
			continue
		}
		id, cells, _, ok := row.Columns()
		if !ok || id == "" {
			continue
		}
		name := ""
		if obj, ok := row.(models.ObjectItem); ok {
			name = obj.Name()
		} else if len(cells) > 0 {
			name = strings.TrimPrefix(cells[0], "/")
		}
		out = append(out, markableRow{id: id, name: name})
	}
	return out
}

// folderMarks reports whether marks live in the folder's table.
func (w *Widget) folderMarks() bool {
	return w.useFolder && w.folder != nil && w.bt != nil
}

// MarkedCount is the number of marked rows the list shows. It counts what
// SelectedItems returns, without sorting the rows.
func (w *Widget) MarkedCount(ctx context.Context) int {
	n := 0
	if w.folderMarks() {
		for _, id := range w.bt.SelectedIDs() {
			if _, ok := w.folder.ItemByID(ctx, id); ok {
				n++
			}
		}
		return n
	}
	for _, it := range w.items {
		if it.Selected {
			n++
		}
	}
	return n
}

// MarksGeneration identifies the marks the widget holds now. It changes
// when the widget shows another folder: a new table starts without marks,
// and its rows may have the IDs of the old one's.
func (w *Widget) MarksGeneration() uint64 { return w.marksGen }

// Unmark removes the marks of the rows ids while the widget still holds
// the marks of generation gen, and returns how many it removed. After a
// change of folder it removes none, so rows that only share an ID keep
// their marks.
func (w *Widget) Unmark(ctx context.Context, gen uint64, ids []string) int {
	if !w.folderMarks() || gen != w.marksGen || len(ids) == 0 {
		return 0
	}
	drop := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		drop[id] = struct{}{}
	}
	n := w.markRows(ctx, func(r markableRow, was bool) (bool, bool) {
		_, ok := drop[r.id]
		return false, ok && was
	})
	return n
}

// markRows marks (or unmarks) every row keep accepts and redraws the table.
// It returns how many rows changed.
func (w *Widget) markRows(ctx context.Context, keep func(markableRow, bool) (bool, bool)) int {
	marked := make(map[string]struct{})
	for _, id := range w.bt.SelectedIDs() {
		marked[id] = struct{}{}
	}
	changed := 0
	for _, r := range w.markableRows(ctx) {
		_, was := marked[r.id]
		now, apply := keep(r, was)
		if !apply || now == was {
			continue
		}
		w.bt.Mark(ctx, r.id, now)
		changed++
	}
	w.bt.Refresh(ctx)
	return changed
}

func (w *Widget) selectAll(ctx context.Context) {
	if w.folderMarks() {
		w.markRows(ctx, func(markableRow, bool) (bool, bool) { return true, true })
		return
	}
	for i := range w.items {
		w.items[i].Selected = true
	}
}

func (w *Widget) invertSelection(ctx context.Context) {
	if w.folderMarks() {
		w.markRows(ctx, func(_ markableRow, was bool) (bool, bool) { return !was, true })
		return
	}
	for i := range w.items {
		w.items[i].Selected = !w.items[i].Selected
	}
}

// MarkGroup marks (mark true) or unmarks the rows whose name matches the
// shell pattern, as mc's Select group and Unselect group do. The pattern
// uses path.Match syntax ("*", "?", "[...]"), matches the whole name and
// ignores case. It returns how many rows changed.
func (w *Widget) MarkGroup(ctx context.Context, pattern string, mark bool) (int, error) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return 0, nil
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return 0, fmt.Errorf("pattern %q: %w", pattern, err)
	}
	matches := func(name string) bool {
		ok, _ := path.Match(pattern, strings.ToLower(name))
		return ok
	}
	if w.folderMarks() {
		return w.markRows(ctx, func(r markableRow, _ bool) (bool, bool) {
			return mark, matches(r.name)
		}), nil
	}
	changed := 0
	for i := range w.items {
		if w.items[i].Name == ".." || w.items[i].Selected == mark || !matches(w.items[i].Name) {
			continue
		}
		w.items[i].Selected = mark
		changed++
	}
	return changed, nil
}

// requestMarkGroup asks the panel shell for the pattern dialog.
func requestMarkGroup(mark bool) tea.Cmd {
	return func() tea.Msg { return panelcontent.MarkGroupRequest{Mark: mark} }
}
