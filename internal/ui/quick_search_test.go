package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
)

type namesFolder struct{ rows []table.Row }

func (f *namesFolder) Columns() []table.Column { return []table.Column{{Title: "Name"}} }
func (f *namesFolder) Path() []string          { return nil }
func (f *namesFolder) ItemByID(_ context.Context, id string) (models.Item, bool) {
	for _, r := range f.rows {
		if rid, _, _, _ := r.Columns(); rid == id {
			it, ok := r.(models.Item)
			return it, ok
		}
	}
	return nil, false
}
func (f *namesFolder) Len(context.Context) int { return len(f.rows) }
func (f *namesFolder) Lines(_ context.Context, top, num int) []table.Row {
	if top >= len(f.rows) {
		return nil
	}
	return f.rows[top:min(len(f.rows), top+num)]
}
func (f *namesFolder) Above(context.Context, string, int) []table.Row { return nil }
func (f *namesFolder) Below(context.Context, string, int) []table.Row { return nil }
func (f *namesFolder) Find(_ context.Context, id string) (int, table.Row, bool) {
	for i, r := range f.rows {
		if rid, _, _, _ := r.Columns(); rid == id {
			return i, r, true
		}
	}
	return -1, nil, false
}

func TestPanelQuickSearchKeys(t *testing.T) {
	ctx := t.Context()
	var rows []table.Row
	for _, n := range []string{"configmaps", "pods", "podtemplates", "secrets"} {
		rows = append(rows, models.NewSimpleItem(n, []string{"/" + n}, nil, models.WhiteStyle()))
	}
	p := NewPanel("t")
	p.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	p.UseFolder(ctx, true)
	p.SetFolder(ctx, &namesFolder{rows: rows}, false)
	selected := func() string { return p.listWidget(ctx).CurrentItem(ctx).Name }
	press := func(text string) tea.KeyPressMsg {
		return tea.KeyPressMsg{Code: []rune(text)[0], Text: text}
	}

	if !p.StartQuickSearch() || !p.QuickSearchActive() {
		t.Fatalf("quick search did not start")
	}
	for _, step := range []struct {
		key     tea.KeyPressMsg
		handled bool
		text    string
		sel     string
	}{
		{press("p"), true, "p", "pods"},
		{press("x"), true, "p", "pods"}, // no row matches "px": ignored
		{press("o"), true, "po", "pods"},
		{tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}, true, "po", "podtemplates"},
		{tea.KeyPressMsg{Code: tea.KeyBackspace}, true, "p", "podtemplates"},
	} {
		if got := p.QuickSearchKey(step.key); got != step.handled {
			t.Fatalf("key %q handled=%v, want %v", step.key.String(), got, step.handled)
		}
		if p.search.text != step.text || selected() != step.sel {
			t.Fatalf("after %q: text %q selected %q, want %q %q", step.key.String(), p.search.text, selected(), step.text, step.sel)
		}
	}
	if !p.QuickSearchKey(tea.KeyPressMsg{Code: tea.KeyEscape}) || p.QuickSearchActive() {
		t.Fatalf("Esc must end the search and be consumed")
	}
	p.StartQuickSearch()
	if p.QuickSearchKey(tea.KeyPressMsg{Code: tea.KeyEnter}) || p.QuickSearchActive() {
		t.Fatalf("Enter must end the search and pass on")
	}
}
