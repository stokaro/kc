package ui

// White-box like the rest of package ui: the frame hit columns and the
// list widget behind a Panel are unexported.

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/models"
	table "github.com/sttts/kc/internal/table"
)

// filterFolder is a namesFolder that hides rows by name with a RowFilter.
type filterFolder struct {
	all    []table.Row
	filter models.RowFilter
	namesFolder
}

func newFilterFolder(names ...string) *filterFolder {
	f := &filterFolder{}
	for _, n := range names {
		f.all = append(f.all, models.NewSimpleItem(n, []string{n}, nil, models.WhiteStyle()))
	}
	f.SetRowFilter(models.RowFilter{})
	return f
}

func (f *filterFolder) RowFilter() models.RowFilter { return f.filter }
func (f *filterFolder) SetRowFilter(filter models.RowFilter) {
	f.filter = filter
	f.rows = nil
	for _, r := range f.all {
		id, _, _, _ := r.Columns()
		if filter.Matches(models.RowFacts{Name: id, ID: id}) {
			f.rows = append(f.rows, r)
		}
	}
}

func mustFilter(t *testing.T, text string) models.RowFilter {
	t.Helper()
	f, err := models.ParseRowFilter(text)
	if err != nil {
		t.Fatalf("ParseRowFilter(%q): %v", text, err)
	}
	return f
}

func TestPanelRowFilterKeepsSelection(t *testing.T) {
	ctx := t.Context()
	p := NewPanel("t")
	p.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	p.UseFolder(ctx, true)
	p.SetFolder(ctx, newFilterFolder("api", "pods", "podtemplates", "secrets"), false)
	lw := p.listWidget(ctx)
	lw.SelectByRowID(ctx, "pods")
	selected := func() string { return p.currentSelectionID(ctx) }
	if got := selected(); got != "pods" {
		t.Fatalf("selected %q before filtering, want pods", got)
	}

	steps := []struct {
		name string
		text string
		want string
	}{
		{name: "kept row keeps the cursor", text: "po", want: "pods"},
		{name: "clearing keeps the cursor", text: "", want: "pods"},
		// A hidden cursor row leaves the cursor at its position, as when
		// a live update deletes the row.
		{name: "inverse hides the cursor row", text: "!^pods$", want: "podtemplates"},
		{name: "label selector on rows without labels", text: "-l app", want: ""},
		{name: "cleared again", text: "", want: "api"},
	}
	for _, step := range steps {
		if !p.SetRowFilter(ctx, mustFilter(t, step.text)) {
			t.Fatalf("%s: SetRowFilter refused a filterable folder", step.name)
		}
		if got := selected(); got != step.want {
			t.Fatalf("%s: selected %q, want %q", step.name, got, step.want)
		}
	}
	if f, ok := p.RowFilter(); !ok || f.Active() {
		t.Fatalf("RowFilter() = %+v, %v; want an inactive filter", f, ok)
	}
}

func TestPanelRowFilterNeedsFilterableFolder(t *testing.T) {
	ctx := t.Context()
	p := NewPanel("t")
	p.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	p.UseFolder(ctx, true)
	p.SetFolder(ctx, &namesFolder{}, false)
	if _, ok := p.RowFilter(); ok {
		t.Fatalf("RowFilter() ok for a folder that cannot filter")
	}
	if p.SetRowFilter(ctx, mustFilter(t, "x")) {
		t.Fatalf("SetRowFilter() accepted a folder that cannot filter")
	}
}

func TestPanelFrameShowsRowFilter(t *testing.T) {
	ctx := t.Context()
	p := NewPanel("t")
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	p.UseFolder(ctx, true)
	p.SetFolder(ctx, newFilterFolder("api", "web"), false)
	p.SetScope(panelScopeLabel{Context: "mba", Namespace: "default"})

	bottom := func() string {
		lines := strings.Split(ansi.Strip(p.Render(ctx, 80, 12, true)), "\n")
		return lines[len(lines)-1]
	}
	if strings.Contains(bottom(), "Filter:") {
		t.Fatalf("frame shows a filter before one is set: %q", bottom())
	}

	filter := mustFilter(t, "!web")
	filter.FaultsOnly = true
	p.SetRowFilter(ctx, filter)
	line := bottom()
	label := "Filter: !web, faults"
	col := strings.Index(line, label)
	if col < 0 {
		t.Fatalf("bottom border %q has no %q", line, label)
	}
	// Columns count cells, the border glyphs are multi-byte.
	x := ansi.StringWidth(line[:col])
	if got := p.scopeHit(x); got != "filter" {
		t.Errorf("scopeHit(%d) = %q, want filter", x, got)
	}
	if got := p.scopeHit(x - 2); got != "" {
		t.Errorf("scopeHit(%d) = %q left of the label, want none", x-2, got)
	}
	if !strings.Contains(line, "mba") {
		t.Errorf("filter label pushed out the scope: %q", line)
	}
}

func TestRowFilterModelValidates(t *testing.T) {
	m := NewRowFilterModel(1, "web")
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
		t.Fatalf("Enter with a valid filter emitted nothing")
	} else if msg, ok := cmd().(RowFilterResultMsg); !ok || msg.Panel != 1 || msg.Text != "web" || !msg.Confirm || !msg.Close {
		t.Fatalf("result = %#v", cmd())
	}

	bad := NewRowFilterModel(0, "web(")
	bad.SetDimensions(60, 6)
	if _, cmd := bad.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		if msg, ok := cmd().(RowFilterResultMsg); ok && msg.Confirm {
			t.Fatalf("an invalid regex was accepted: %#v", msg)
		}
	}
	if !strings.Contains(ansi.Strip(bad.View().Content), "invalid regular expression") {
		t.Fatalf("dialog does not show the error: %q", ansi.Strip(bad.View().Content))
	}
}

func TestViewOptionsFaultsOnly(t *testing.T) {
	m := NewViewOptionsModel(ViewOptionsConfig{
		PanelIndex:        1,
		SkipPanelSection:  true,
		PanelWidthPercent: -1,
		Objects:           &ViewOptionsObjectsConfig{ShowFaultsOnly: true},
	})
	if !regexp.MustCompile(`Faults only\s*: No`).MatchString(ansi.Strip(m.View().Content)) {
		t.Fatalf("dialog has no Faults only row: %q", ansi.Strip(m.View().Content))
	}
	for range len(m.entries) {
		if m.entries[m.focus].option == viewOptionObjectFaultsOnly {
			break
		}
		m.moveFocus(1)
	}
	if m.entries[m.focus].option != viewOptionObjectFaultsOnly {
		t.Fatalf("could not focus Faults only")
	}
	m.adjustCurrent(0)
	got := m.commit(true, false)
	if got.Objects == nil || !got.Objects.HasFaultsOnly || !got.Objects.FaultsOnly {
		t.Fatalf("commit objects = %+v", got.Objects)
	}

	plain := NewViewOptionsModel(ViewOptionsConfig{SkipPanelSection: true, Objects: &ViewOptionsObjectsConfig{}})
	if strings.Contains(ansi.Strip(plain.View().Content), "Faults only") {
		t.Fatalf("Faults only shown for a folder that cannot filter")
	}
}

var _ models.RowFilterable = (*filterFolder)(nil)

func TestAppRowFilterKeys(t *testing.T) {
	ctx := t.Context()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	folder := newFilterFolder("api", "pods", "web")
	app.leftPanel.UseFolder(ctx, true)
	app.leftPanel.SetFolder(ctx, folder, false)
	app.activePanel = 0

	app.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt})
	if !app.modalManager.IsModalVisible() {
		t.Fatalf("M-f did not open the Filter dialog")
	}
	app.Update(RowFilterResultMsg{Panel: 0, Text: "!web", Confirm: true, Close: true})
	if app.modalManager.IsModalVisible() {
		t.Fatalf("the Filter dialog stayed open")
	}
	if got := folder.RowFilter().Text(); got != "!web" {
		t.Fatalf("filter text = %q, want !web", got)
	}

	app.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if f := folder.RowFilter(); !f.FaultsOnly || f.Text() != "!web" {
		t.Fatalf("C-z: filter = %q faults=%v, want !web with faults", f.Text(), f.FaultsOnly)
	}
	// A new text keeps the faults toggle.
	app.Update(RowFilterResultMsg{Panel: 0, Text: "a", Confirm: true, Close: true})
	if f := folder.RowFilter(); !f.FaultsOnly || f.Text() != "a" {
		t.Fatalf("filter = %q faults=%v, want a with faults", f.Text(), f.FaultsOnly)
	}
	app.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if folder.RowFilter().FaultsOnly {
		t.Fatalf("second C-z did not turn faults off")
	}

	app.clearRowFilter(0)
	if folder.RowFilter().Active() {
		t.Fatalf("clearRowFilter left %q", folder.RowFilter().Label())
	}
}

func TestAppRowFilterIgnoresFoldersThatCannotFilter(t *testing.T) {
	ctx := t.Context()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app.leftPanel.UseFolder(ctx, true)
	app.leftPanel.SetFolder(ctx, &namesFolder{}, false)
	app.activePanel = 0

	app.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt})
	if app.modalManager.IsModalVisible() {
		t.Fatalf("M-f opened the Filter dialog on a list that cannot filter")
	}
}
