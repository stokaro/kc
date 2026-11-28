package ui

import (
	"path/filepath"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/hotlist"
	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

var followPodsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func followObjectRow(ns, name string) *models.ObjectRow {
	return models.NewObjectRow(name, []string{name}, []string{"namespaces", ns, "pods", name}, followPodsGVR, ns, name, models.WhiteStyle())
}

// newFollowFakeApp returns an App without a cluster. The left panel lists
// root: pods a, b and c, then folder sub, which holds pod x. The right
// panel is in Describe mode with a widget that records what it is told.
func newFollowFakeApp(t *testing.T) (*App, *lateFolder, *selectionSpyWidget) {
	t.Helper()
	ctx := t.Context()
	sub := modeltesting.NewStaticFolder("sub", nil, []table.Row{followObjectRow("shop", "x")})
	root := newLateFolder()
	root.add(followObjectRow("shop", "a"), followObjectRow("shop", "b"), followObjectRow("shop", "c"), enterRow("sub", "sub", sub))

	app := NewApp()
	t.Cleanup(app.cancel)
	app.hotlistPath = filepath.Join(t.TempDir(), hotlist.FileName)
	model, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app = model.(*App)
	app.leftNav = navui.NewNavigator(root)
	app.rightNav = navui.NewNavigator(newLateFolder())
	app.leftPanel.UseFolder(ctx, true)
	app.rightPanel.UseFolder(ctx, true)
	app.syncPanelWithNavigator(0)
	app.syncPanelWithNavigator(1)
	app.installFolderNavHandlers()
	app.recordHistory(0)
	app.setActivePanel(0, "test")

	spy := &selectionSpyWidget{}
	app.rightPanel.RegisterMode(PanelModeDescribe, func(*Panel) PanelWidget { return spy })
	settle(t, app, app.setPanelMode(ctx, app.rightPanel, PanelModeDescribe, "test"))
	// The setup moved the left list outside Update; the first message
	// reports it.
	_, cmd := app.Update(followNoopMsg{})
	settle(t, app, cmd)
	return app, root, spy
}

// followedName is the name of the object a follower was told about, ""
// when it was told about a row that is not an object, or about none.
func followedName(item models.Item) string {
	if obj, ok := item.(models.ObjectItem); ok {
		return obj.Name()
	}
	return ""
}

// Every way of moving the left cursor reaches the follower as a cursor key
// does: the keys, a refresh that removed the row, a cd and the history.
// A row that is not an object leaves the follower with nothing to show.
func TestFollowerHearsEveryCursorMove(t *testing.T) {
	app, root, spy := newFollowFakeApp(t)
	ctx := t.Context()
	steps := []struct {
		name       string
		move       func()
		row, path  string
		wantObject string
	}{
		{"start", func() {}, "a", "/", "a"},
		{"cursor key", func() { sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown}) }, "b", "/", "b"},
		{"refresh removed the row", func() {
			root.mu.Lock()
			root.rows = slices.Delete(root.rows, 1, 2)
			root.mu.Unlock()
			root.Refresh()
			_, cmd := app.Update(FolderDirtyMsg{PanelIdx: 0})
			settle(t, app, cmd)
		}, "c", "/", "c"},
		{"cd on the shell line", func() { shellCd(t, app, "cd sub") }, "__back__", "/sub", ""},
		{"cursor key after cd", func() { sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown}) }, "x", "/sub", "x"},
		{"history back", func() { sendKey(t, app, tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt}) }, "sub", "/", ""},
		{"cursor key after the history", func() { sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyUp}) }, "c", "/", "c"},
	}
	for _, step := range steps {
		step.move()
		if row, path := app.leftPanel.currentSelectionID(ctx), app.leftPanel.GetCurrentPath(); row != step.row || path != step.path {
			t.Fatalf("%s: the left cursor is on %q in %s, want %q in %s", step.name, row, path, step.row, step.path)
		}
		// The next step starts from the left cursor, not from what the
		// follower got, so every step reports.
		if len(spy.events) == 0 {
			t.Errorf("%s: the follower was told nothing", step.name)
			continue
		}
		last := spy.events[len(spy.events)-1]
		if got := followedName(last.Item); got != step.wantObject || last.ID != step.row || last.Path != step.path {
			t.Errorf("%s: the follower was last told about %q (row %q in %s), want %q (row %q in %s)",
				step.name, got, last.ID, last.Path, step.wantObject, step.row, step.path)
		}
		if got := followedName(app.rightPanel.CurrentSelection(ctx).Item); got != step.wantObject {
			t.Errorf("%s: the follower's selection is %q, want %q", step.name, got, step.wantObject)
		}
	}
}

type followNoopMsg struct{}

// A follower hears about a move once, not after every message: a load per
// message would reread the object on every tick. An explicit report
// stands until the list moves.
func TestFollowerHearsEachMoveOnce(t *testing.T) {
	app, _, spy := newFollowFakeApp(t)
	spy.events = nil
	for range 3 {
		_, cmd := app.Update(followNoopMsg{})
		settle(t, app, cmd)
	}
	if len(spy.events) != 0 {
		t.Fatalf("the follower was told %d times without a move: %+v", len(spy.events), spy.events)
	}

	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	if len(spy.events) != 1 || followedName(spy.events[0].Item) != "b" {
		t.Fatalf("the follower was told %+v after one move to b, want b once", spy.events)
	}

	spy.events = nil
	_, cmd := app.Update(PanelSelectionChangedMsg{Panel: app.leftPanel, Selection: panelcontent.Selection{ID: "z", Item: followObjectRow("shop", "z")}})
	settle(t, app, cmd)
	_, cmd = app.Update(followNoopMsg{})
	settle(t, app, cmd)
	if len(spy.events) != 1 || followedName(spy.events[0].Item) != "z" {
		t.Fatalf("the follower was told %+v after the report of z, want z once", spy.events)
	}
	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	if last := spy.events[len(spy.events)-1]; followedName(last.Item) != "c" {
		t.Fatalf("the follower was last told about %q after the move to c", followedName(last.Item))
	}
}

// A row of the same ID in another namespace or folder is another row: the
// follower hears about it although the cursor's row ID did not change.
func TestFollowerHearsSameRowElsewhere(t *testing.T) {
	for _, tc := range []struct {
		name, ns string
		path     []string
		wantPath string
	}{
		{"another namespace", "batch", nil, "/"},
		{"another folder", "", []string{"configmaps"}, "/configmaps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _, spy := newFollowFakeApp(t)
			ctx := t.Context()
			if id := app.leftPanel.currentSelectionID(ctx); id != "a" {
				t.Fatalf("the left cursor is on %q, want a", id)
			}
			ns := tc.ns
			if ns == "" {
				ns = "shop"
			}
			other := newLateFolder(tc.path...)
			other.add(followObjectRow(ns, "a"))
			app.leftNav = navui.NewNavigator(other)
			app.syncPanelWithNavigator(0)
			// A path jump puts the cursor on the row it names.
			app.leftPanel.SelectByRowID(ctx, "a")
			if tc.ns != "" {
				app.panelScope[0] = tc.ns
			}
			if id, path := app.leftPanel.currentSelectionID(ctx), app.leftPanel.GetCurrentPath(); id != "a" || path != tc.wantPath {
				t.Fatalf("the left cursor is on %q in %s, want a in %s", id, path, tc.wantPath)
			}
			spy.events = nil
			_, cmd := app.Update(followNoopMsg{})
			settle(t, app, cmd)
			if len(spy.events) != 1 {
				t.Fatalf("the follower was told %+v, want one report", spy.events)
			}
			obj, ok := spy.events[0].Item.(models.ObjectItem)
			if !ok || obj.Namespace() != ns || spy.events[0].Path != tc.wantPath {
				t.Fatalf("the follower was told %+v, want %s/a in %s", spy.events[0], ns, tc.wantPath)
			}
		})
	}
}

// A follower that enters Describe while the other panel's cursor is on ".."
// shows nothing and acts on nothing; it does not fall back to the row
// under its own hidden list's cursor. Then it acts on the row it follows.
func TestFollowerIgnoresItsOwnList(t *testing.T) {
	app, _, spy := newFollowFakeApp(t)
	ctx := t.Context()
	settle(t, app, app.setPanelMode(ctx, app.rightPanel, PanelModeList, "test"))
	own := newLateFolder()
	own.add(followObjectRow("other", "own"))
	app.rightNav = navui.NewNavigator(own)
	app.syncPanelWithNavigator(1)
	if item, ok := app.rightPanel.SelectedNavItem(ctx); !ok || followedName(item) != "own" {
		t.Fatalf("the right list's cursor is on %v, want own", item)
	}

	shellCd(t, app, "cd sub")
	spy.events = nil
	settle(t, app, app.setPanelMode(ctx, app.rightPanel, PanelModeDescribe, "test"))
	for _, ev := range spy.events {
		if name := followedName(ev.Item); name != "" {
			t.Fatalf("the follower was told about %q with the other cursor on ..: %+v", name, ev)
		}
	}
	if sel := app.rightPanel.CurrentSelection(ctx); sel.Item != nil {
		t.Fatalf("the follower keeps %v with the other cursor on ..", sel.Item)
	}
	actsOn := func() []string {
		var got []string
		if it := app.rightPanel.GetCurrentItem(); it != nil {
			got = append(got, "GetCurrentItem "+followedName(it.Item))
		}
		if item, _ := app.selectedRow(ctx, app.rightPanel); item != nil {
			got = append(got, "selectedRow "+followedName(item))
		}
		return got
	}
	if got := actsOn(); len(got) != 0 {
		t.Fatalf("the follower acts on %v with the other cursor on ..", got)
	}

	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	want := []string{"GetCurrentItem x", "selectedRow x"}
	if got := actsOn(); !slices.Equal(got, want) {
		t.Fatalf("the follower acts on %v, want %v", got, want)
	}
}

// What a follower last showed does not come back when it is told about a
// row without an object.
func TestFollowerClearsOnRowWithoutObject(t *testing.T) {
	ctx := t.Context()
	panel := NewPanel("test")
	spy := &selectionSpyWidget{}
	panel.RegisterMode(PanelModeDescribe, func(*Panel) PanelWidget { return spy })
	panel.SetMode(ctx, PanelModeDescribe)

	panel.NotifySelection(ctx, panelcontent.Selection{ID: "a", Item: followObjectRow("shop", "a"), Source: "conn-1"})
	panel.NotifySelection(ctx, panelcontent.Selection{ID: "__back__", Path: "/pods", Source: "conn-1"})
	last := spy.events[len(spy.events)-1]
	if last.Item != nil || last.Path != "/pods" {
		t.Fatalf("the widget was told %+v, want no item in /pods", last)
	}
	if sel := panel.CurrentSelection(ctx); sel.Item != nil {
		t.Fatalf("the follower keeps %v after a row without an object", sel.Item)
	}
}
