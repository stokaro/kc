package ui

// White-box like the other App tests: the hotlist, the jump and the panel
// state behind them are unexported.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/hotlist"
	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
)

// viewFolder is a filterable list across namespaces that refuses filter
// terms on a "gpu" column, as an ObjectsFolder without one does.
type viewFolder struct {
	*filterFolder
	group bool
}

func (f *viewFolder) ApplyNamespaceGrouping(on bool)        { f.group = on }
func (f *viewFolder) NamespaceGrouping() (on, applies bool) { return f.group, true }
func (f *viewFolder) ValidateRowFilter(rf models.RowFilter) error {
	if strings.Contains(rf.Text(), "gpu") {
		return errors.New(`this list has no "gpu" column`)
	}
	return nil
}

// newViewApp returns an App whose left panel shows root -> pods, with the
// hotlist file at path.
func newViewApp(t *testing.T, path string) (*App, *viewFolder) {
	t.Helper()
	pods := &viewFolder{filterFolder: newFilterFolder("api", "web-1", "web-2"), group: true}
	root := modeltesting.NewStaticFolder("/", nil, []table.Row{
		modeltesting.NewEnterableItem("pods", []string{"pods"}, []string{"pods"}, func() (models.Folder, error) { return pods, nil }, models.WhiteStyle()),
	})
	a := NewApp()
	t.Cleanup(a.cancel)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	a.hotlistPath = path
	if cmd := a.loadHotlist(); cmd != nil {
		t.Fatalf("loadHotlist reported %v", cmd())
	}
	a.leftNav = navui.NewNavigator(root)
	a.rightNav = navui.NewNavigator(modeltesting.NewStaticFolder("/", nil, nil))
	a.leftPanel.UseFolder(t.Context(), true)
	a.rightPanel.UseFolder(t.Context(), true)
	a.syncPanelWithNavigator(0)
	a.syncPanelWithNavigator(1)
	a.activePanel = 0
	return a, pods
}

func TestSavedViewRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), hotlist.FileName)
	a, _ := newViewApp(t, path)
	enter(t, a, "pods")
	filter := mustFilter(t, "web")
	filter.FaultsOnly = true
	a.setPanelRowFilter(a.leftPanel, filter)
	a.leftPanel.SetNamespaceGrouping(t.Context(), false)
	a.leftPanel.SetColumnsMode(t.Context(), "wide")

	a.Update(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	hotlistPicker(t, a).Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if m := a.modalManager.modals[viewNameModalKey]; m == nil || !m.IsVisible() {
		t.Fatalf("F2 did not ask for a name")
	}
	a.Update(viewNameMsg{Panel: 0, Entry: -1, Name: "payments failures", Confirm: true, Close: true})

	saved, err := hotlist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Name != "payments failures" || saved[0].Path != "/pods" || saved[0].View == nil {
		t.Fatalf("saved = %+v", saved)
	}
	v := saved[0].View
	if v.Filter != "web" || !v.FaultsOnly || v.Columns != "wide" || v.GroupByNamespace == nil || *v.GroupByNamespace {
		t.Fatalf("saved view = %+v", *v)
	}

	// A restarted kc reopens the view with its settings.
	b, pods2 := newViewApp(t, path)
	b.Update(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	_, cmd := hotlistPicker(t, b).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runJump(t, b, cmd)
	if got := b.leftNav.Steps(); len(got) != 1 || got[0] != "pods" {
		t.Fatalf("steps = %v, want [pods]", got)
	}
	if f := pods2.RowFilter(); f.Text() != "web" || !f.FaultsOnly {
		t.Fatalf("filter = %q faults=%v", f.Text(), f.FaultsOnly)
	}
	if pods2.group {
		t.Fatalf("grouping was not restored")
	}
	if got := b.leftPanel.ColumnsMode(); got != "wide" {
		t.Fatalf("columns = %q", got)
	}

	// Browsing does not change the view; F4 does, and only on a view.
	b.setPanelRowFilter(b.leftPanel, mustFilter(t, "api"))
	if saved, _ := hotlist.Load(path); saved[0].View.Filter != "web" {
		t.Fatalf("browsing changed the saved view")
	}
	b.Update(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	hotlistPicker(t, b).Update(tea.KeyPressMsg{Code: tea.KeyF4})
	if saved, _ := hotlist.Load(path); saved[0].View.Filter != "api" || saved[0].Name != "payments failures" {
		t.Fatalf("F4 did not update the view: %+v", saved[0])
	}
	hotlistPicker(t, b).Update(tea.KeyPressMsg{Code: tea.KeyInsert})
	saved, _ = hotlist.Load(path)
	if len(saved) != 2 || saved[1].View != nil {
		t.Fatalf("Ins did not add a plain bookmark: %+v", saved)
	}
	if _, err := b.updateSavedView(0, 1); err == nil {
		t.Fatalf("F4 turned a bookmark into a view")
	}

	// F6 renames.
	b.Update(viewNameMsg{Panel: 0, Entry: 0, Name: "api pods", Confirm: true, Close: true})
	if saved, _ := hotlist.Load(path); saved[0].Name != "api pods" {
		t.Fatalf("rename: %+v", saved[0])
	}
}

func TestApplySavedViewReportsWhatItCannotRestore(t *testing.T) {
	t.Parallel()
	a, pods := newViewApp(t, filepath.Join(t.TempDir(), hotlist.FileName))
	enter(t, a, "pods")

	if cmd := a.applySavedView(0, hotlist.View{Filter: "web(", FaultsOnly: true}); cmd == nil {
		t.Fatalf("an invalid saved filter was applied without a word")
	}
	if f := pods.RowFilter(); f.Text() != "" || !f.FaultsOnly {
		t.Fatalf("filter = %q faults=%v, want only faults", f.Text(), f.FaultsOnly)
	}

	if cmd := a.applySavedView(0, hotlist.View{Filter: "gpu>1"}); cmd == nil {
		t.Fatalf("a filter on a missing column gave no warning")
	}
	if f := pods.RowFilter(); f.Text() != "gpu>1" {
		t.Fatalf("filter = %q; the saved meaning must stay", f.Text())
	}

	if cmd := a.applySavedView(0, hotlist.View{Filter: "web"}); cmd != nil {
		t.Fatalf("a valid view warned: %v", cmd())
	}
}
