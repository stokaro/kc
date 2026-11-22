package ui

// White-box like the rest of package ui: the dialog entries and the menu
// items are unexported.

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/menubar"
)

// groupingFolder is a namesFolder that lists objects across namespaces when
// spans is set, and records the grouping the panel applies.
type groupingFolder struct {
	namesFolder
	spans   bool
	group   bool
	applied int
}

func (f *groupingFolder) ApplyNamespaceGrouping(on bool) {
	f.group = on
	f.applied++
}

func (f *groupingFolder) NamespaceGrouping() (on, applies bool) { return f.group, f.spans }

var _ models.NamespaceGroupingConfigurable = (*groupingFolder)(nil)

func groupItem(t *testing.T, app *App, idx int) menubar.Item {
	t.Helper()
	for _, it := range app.panelMenuItems(idx) {
		if it.Text() == "Group by namespace" {
			return it
		}
	}
	t.Fatalf("the Left menu has no Group by namespace entry")
	return menubar.Item{}
}

func TestNamespaceGroupingMenuAndOptions(t *testing.T) {
	ctx := t.Context()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	folder := &groupingFolder{spans: true, group: true}
	app.leftPanel.UseFolder(ctx, true)
	app.leftPanel.SetFolder(ctx, folder, false)
	app.activePanel = 0

	if !folder.group || folder.applied == 0 {
		t.Fatalf("SetFolder did not apply the panel's grouping: group=%v applied=%d", folder.group, folder.applied)
	}
	item := groupItem(t, app, 0)
	if item.Disabled || !item.Checked {
		t.Fatalf("grouped list: item disabled=%v checked=%v, want enabled and checked", item.Disabled, item.Checked)
	}
	item.Run()
	if folder.group {
		t.Fatalf("the menu entry did not turn grouping off")
	}
	if on, _ := app.leftPanel.NamespaceGrouping(); on {
		t.Fatalf("panel still reports grouping on")
	}
	if app.ensurePanelConfig(app.leftPanel).Objects.GroupByNamespace {
		t.Fatalf("the panel config did not follow the toggle")
	}
	if item := groupItem(t, app, 0); item.Checked {
		t.Fatalf("item still checked after the toggle")
	}

	// The panel options dialog offers the toggle and applies it.
	commit := func(objects *ViewOptionsObjectsPayload) {
		t.Helper()
		app.showViewOptionsModalForPanel(app.leftPanel)
		if !app.modalManager.IsModalVisible() {
			t.Fatalf("the panel options dialog did not open")
		}
		app.Update(ViewOptionsCommittedMsg{PanelIndex: 0, TableMode: "scroll", Accept: true, Close: true, Objects: objects})
	}
	commit(&ViewOptionsObjectsPayload{Columns: "normal", Order: "-mem", HasGroupByNamespace: true, GroupByNamespace: true})
	if !folder.group {
		t.Fatalf("the options dialog did not turn grouping back on")
	}
	// A dialog that did not offer the toggle leaves it alone.
	commit(&ViewOptionsObjectsPayload{Columns: "normal", Order: "name"})
	if !folder.group {
		t.Fatalf("a dialog without the toggle changed the grouping")
	}

	// A list of one namespace has nothing to group.
	single := &groupingFolder{}
	app.leftPanel.SetFolder(ctx, single, false)
	if item := groupItem(t, app, 0); !item.Disabled {
		t.Fatalf("one-namespace list: item enabled")
	}
	if app.toggleNamespaceGrouping() {
		t.Fatalf("toggle reported a change on a one-namespace list")
	}
}

func TestViewOptionsGroupByNamespace(t *testing.T) {
	t.Parallel()
	m := NewViewOptionsModel(ViewOptionsConfig{
		SkipPanelSection:  true,
		PanelWidthPercent: -1,
		Objects:           &ViewOptionsObjectsConfig{ShowGroupByNamespace: true, GroupByNamespace: true},
	})
	if !regexp.MustCompile(`Group by namespace\s*: Yes`).MatchString(ansi.Strip(m.View().Content)) {
		t.Fatalf("dialog has no Group by namespace row: %q", ansi.Strip(m.View().Content))
	}
	for range len(m.entries) {
		if m.entries[m.focus].option == viewOptionObjectGroupNamespaces {
			break
		}
		m.moveFocus(1)
	}
	if m.entries[m.focus].option != viewOptionObjectGroupNamespaces {
		t.Fatalf("could not focus Group by namespace")
	}
	m.adjustCurrent(0)
	got := m.commit(true, false)
	if got.Objects == nil || !got.Objects.HasGroupByNamespace || got.Objects.GroupByNamespace {
		t.Fatalf("commit objects = %+v, want grouping offered and off", got.Objects)
	}

	plain := NewViewOptionsModel(ViewOptionsConfig{SkipPanelSection: true, Objects: &ViewOptionsObjectsConfig{}})
	if strings.Contains(ansi.Strip(plain.View().Content), "Group by namespace") {
		t.Fatalf("Group by namespace shown for a list of one namespace")
	}
	if got := plain.commit(true, false); got.Objects.HasGroupByNamespace {
		t.Fatalf("commit offers grouping the dialog did not show")
	}
}
