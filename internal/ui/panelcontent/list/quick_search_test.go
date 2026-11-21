package list

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	models "github.com/sttts/kc/internal/models"
	table "github.com/sttts/kc/internal/table"
	panelcontent "github.com/sttts/kc/internal/ui/panelcontent"
)

// rowsFolder serves fixed rows.
type rowsFolder struct{ rows []table.Row }

func (f *rowsFolder) Columns() []table.Column { return []table.Column{{Title: "Name"}} }
func (f *rowsFolder) Path() []string          { return nil }
func (f *rowsFolder) ItemByID(_ context.Context, id string) (models.Item, bool) {
	for _, r := range f.rows {
		if rid, _, _, _ := r.Columns(); rid == id {
			it, ok := r.(models.Item)
			return it, ok
		}
	}
	return nil, false
}
func (f *rowsFolder) Len(context.Context) int { return len(f.rows) }
func (f *rowsFolder) Lines(_ context.Context, top, num int) []table.Row {
	if top >= len(f.rows) {
		return nil
	}
	return f.rows[top:min(len(f.rows), top+num)]
}
func (f *rowsFolder) Above(context.Context, string, int) []table.Row { return nil }
func (f *rowsFolder) Below(context.Context, string, int) []table.Row { return nil }
func (f *rowsFolder) Find(_ context.Context, id string) (int, table.Row, bool) {
	for i, r := range f.rows {
		if rid, _, _, _ := r.Columns(); rid == id {
			return i, r, true
		}
	}
	return -1, nil, false
}

func TestQuickSearch(t *testing.T) {
	ctx := t.Context()
	row := func(name string) table.Row {
		return models.NewSimpleItem(name, []string{"/" + name}, nil, models.WhiteStyle())
	}
	folder := &rowsFolder{rows: []table.Row{models.BackItem{}, row("configmaps"), row("Secrets"), row("serviceaccounts"), row("services")}}
	w := New(panelcontent.WidgetDeps{})
	w.UseFolder(true)
	_, _ = w.Update(ctx, tea.WindowSizeMsg{Width: 30, Height: 10})
	w.SetFolder(ctx, folder, true)

	steps := []struct {
		prefix string
		next   bool
		ok     bool
		want   string
	}{
		{prefix: "se", ok: true, want: "Secrets"}, // case-insensitive, first from the top
		{prefix: "se", next: true, ok: true, want: "serviceaccounts"},
		{prefix: "se", next: true, ok: true, want: "services"},
		{prefix: "se", next: true, ok: true, want: "Secrets"}, // wraps
		{prefix: "serv", ok: true, want: "serviceaccounts"},
		{prefix: "x", ok: false, want: "serviceaccounts"},  // no match keeps the selection
		{prefix: "..", ok: false, want: "serviceaccounts"}, // the back row is never found
	}
	for _, s := range steps {
		if got := w.QuickSearch(ctx, s.prefix, s.next); got != s.ok {
			t.Fatalf("QuickSearch(%q, %v) = %v, want %v", s.prefix, s.next, got, s.ok)
		}
		if got := w.items[w.selected].Name; got != s.want {
			t.Fatalf("after QuickSearch(%q, %v) selected %q, want %q", s.prefix, s.next, got, s.want)
		}
	}
}

// A click reports the row counted from the table header; the first data
// row sits right below it.
func TestClickSelectsRowUnderMouse(t *testing.T) {
	ctx := t.Context()
	var rows []table.Row
	for _, n := range []string{"a", "b", "c"} {
		rows = append(rows, models.NewSimpleItem(n, []string{"/" + n}, nil, models.WhiteStyle()))
	}
	w := New(panelcontent.WidgetDeps{})
	w.UseFolder(true)
	_, _ = w.Update(ctx, tea.WindowSizeMsg{Width: 30, Height: 10})
	w.SetFolder(ctx, &rowsFolder{rows: rows}, false)
	for row, want := range map[int]string{1: "a", 2: "b", 3: "c"} {
		w.handleMouse(ctx, panelcontent.MouseMsg{Intent: panelcontent.MouseIntentClick, Row: row, Button: tea.MouseLeft})
		if got := w.CurrentItem(ctx).Name; got != want {
			t.Fatalf("click on row %d selected %q, want %q", row, got, want)
		}
	}
	w.handleMouse(ctx, panelcontent.MouseMsg{Intent: panelcontent.MouseIntentClick, Row: 0, Button: tea.MouseLeft})
	if got := w.CurrentItem(ctx).Name; got != "c" {
		t.Fatalf("a click on the header must not move the selection, got %q", got)
	}
}
