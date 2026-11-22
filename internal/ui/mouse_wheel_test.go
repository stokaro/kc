package ui

// White-box tests: the list dialogs keep their selection in unexported
// fields, and the App routes the wheel through unexported state.

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/access"
	"github.com/stokaro/kc/pkg/appconfig"
)

var (
	wheelDown = tea.MouseWheelMsg{Button: tea.MouseWheelDown}
	wheelUp   = tea.MouseWheelMsg{Button: tea.MouseWheelUp}
)

// spin sends the wheel events to m and fails when one returns a command:
// the wheel only moves the selection, it never runs an entry.
func spin(t *testing.T, m tea.Model, msgs ...tea.Msg) {
	t.Helper()
	for _, msg := range msgs {
		if _, cmd := m.Update(msg); cmd != nil {
			t.Fatalf("%v returned a command; the wheel must not run anything", msg)
		}
	}
}

func repeat(msg tea.Msg, n int) []tea.Msg {
	out := make([]tea.Msg, n)
	for i := range out {
		out[i] = msg
	}
	return out
}

func TestWheelStep(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
		want int
	}{
		{name: "up", msg: wheelUp, want: -1},
		{name: "down", msg: wheelDown, want: 1},
		{name: "left", msg: tea.MouseWheelMsg{Button: tea.MouseWheelLeft}, want: 0},
		{name: "right", msg: tea.MouseWheelMsg{Button: tea.MouseWheelRight}, want: 0},
		{name: "click", msg: tea.MouseClickMsg{Button: tea.MouseLeft}, want: 0},
		{name: "key", msg: tea.KeyPressMsg{Code: tea.KeyDown}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wheelStep(tc.msg); got != tc.want {
				t.Fatalf("wheelStep = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestModalWheelOnlyOverTheWindow(t *testing.T) {
	content := &capturingModel{}
	modal := NewModal("List", content)
	modal.SetDimensions(80, 24)
	modal.SetWindowed(20, 6, "")
	modal.SetWindowOffset(10, 5)
	modal.Show()
	modal.View()

	tests := []struct {
		name   string
		x, y   int
		passed bool
	}{
		{name: "inside", x: 15, y: 7, passed: true},
		{name: "on the frame", x: 10, y: 5, passed: true},
		{name: "last cell", x: 29, y: 10, passed: true},
		{name: "left of the window", x: 9, y: 7},
		{name: "right of the window", x: 30, y: 7},
		{name: "above the window", x: 15, y: 4},
		{name: "below the window", x: 15, y: 11},
		{name: "on the footer", x: 15, y: 23},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content.last = nil
			modal.Update(tea.MouseWheelMsg{X: tc.x, Y: tc.y, Button: tea.MouseWheelDown})
			got, ok := content.last.(tea.MouseWheelMsg)
			if ok != tc.passed {
				t.Fatalf("content got %T, want passed=%t", content.last, tc.passed)
			}
			// The content sees its own coordinates: (0, 0) is the first
			// cell inside the frame.
			if ok && (got.X != tc.x-11 || got.Y != tc.y-6) {
				t.Fatalf("content got (%d, %d), want (%d, %d)", got.X, got.Y, tc.x-11, tc.y-6)
			}
		})
	}
}

func TestFullscreenModalTakesEveryWheel(t *testing.T) {
	content := &capturingModel{}
	modal := NewModal("Viewer", content)
	modal.SetMode(ModalModeFullscreen)
	modal.SetDimensions(80, 24)
	modal.Show()
	modal.View()
	modal.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
	if _, ok := content.last.(tea.MouseWheelMsg); !ok {
		t.Fatalf("content got %T, want the wheel event", content.last)
	}
}

func TestCommandSelectorWheel(t *testing.T) {
	commands := []appconfig.CommandConfig{
		{Name: "g1", Type: appconfig.CommandTypeGlobal},
		{Name: "g2", Type: appconfig.CommandTypeGlobal},
		{Name: "n1", Type: appconfig.CommandTypeNamespace},
	}
	ran := false
	m := NewCommandSelectorModel(commands, "left", "default", false, func(appconfig.CommandConfig) tea.Cmd {
		ran = true
		return func() tea.Msg { return nil }
	}, nil)
	// Entries: header, g1, g2, header, n1. Two rows: the list scrolls.
	m.SetDimensions(30, 2)

	spin(t, m, wheelDown)
	if m.selected != 2 {
		t.Fatalf("selected %d, want g2 (2)", m.selected)
	}
	spin(t, m, wheelDown)
	if m.selected != 4 || m.scroll != 3 {
		t.Fatalf("selected %d scroll %d, want n1 (4) past the section header, scrolled to 3", m.selected, m.scroll)
	}
	spin(t, m, wheelDown)
	if m.selected != 4 {
		t.Fatalf("selected %d, want to stay on the last entry", m.selected)
	}
	spin(t, m, repeat(wheelUp, 5)...)
	if m.selected != 1 || m.scroll != 1 {
		t.Fatalf("selected %d scroll %d, want g1 (1) visible", m.selected, m.scroll)
	}
	if ran {
		t.Fatal("the wheel ran a command")
	}
}

func TestNodeActionMenuWheel(t *testing.T) {
	m := newNodeActionMenu(nodeTarget{nodes: []string{"a"}})
	spin(t, m, wheelUp)
	if m.selected != 0 {
		t.Fatalf("selected %d; up on the first entry must not wrap", m.selected)
	}
	spin(t, m, repeat(wheelDown, 5)...)
	if m.selected != len(nodeActionLabels)-1 {
		t.Fatalf("selected %d; down must stop on the last entry", m.selected)
	}
	spin(t, m, wheelUp)
	if m.selected != len(nodeActionLabels)-2 {
		t.Fatalf("selected %d after up", m.selected)
	}
}

func TestResourceSelectorWheel(t *testing.T) {
	rs := NewResourceSelector([]schema.GroupVersionKind{{Version: "v1", Kind: "Pod"}})
	rs.SetDimensions(40, 5) // two list rows
	spin(t, rs, repeat(wheelDown, 3)...)
	if rs.selected != 3 || rs.scrollTop != 2 {
		t.Fatalf("selected %d scrollTop %d, want 3 visible at 2", rs.selected, rs.scrollTop)
	}
	spin(t, rs, repeat(wheelUp, 10)...)
	if rs.selected != 0 || rs.scrollTop != 0 {
		t.Fatalf("selected %d scrollTop %d, want the top", rs.selected, rs.scrollTop)
	}
}

func TestPanelModeModelWheel(t *testing.T) {
	modes := []PanelViewMode{PanelModeList, PanelModeManifest}
	m := NewPanelModeModel(0, modes, PanelModeList)
	spin(t, m, repeat(wheelDown, 3)...)
	if m.cursor != 1 {
		t.Fatalf("cursor %d, want the last mode", m.cursor)
	}
	spin(t, m, repeat(wheelUp, 3)...)
	if m.cursor != 0 {
		t.Fatalf("cursor %d, want the first mode", m.cursor)
	}
}

func TestObjectOptionsWheel(t *testing.T) {
	m := NewObjectOptionsModel("scroll", "normal", "name")
	spin(t, m, repeat(wheelDown, 4)...)
	if m.focus != 2 {
		t.Fatalf("focus %d, want the last row", m.focus)
	}
	if m.modeIdx != 0 || m.columnsIdx != 0 || m.orderIdx != 0 {
		t.Fatal("the wheel changed a value")
	}
	spin(t, m, repeat(wheelUp, 4)...)
	if m.focus != 0 {
		t.Fatalf("focus %d, want the first row", m.focus)
	}
}

func TestResourcesOptionsWheel(t *testing.T) {
	m := NewResourcesOptionsModel(true, true, "scroll", true, "name")
	spin(t, m, repeat(wheelDown, 4)...)
	if m.focus != len(m.options)-1 {
		t.Fatalf("focus %d, want the last row", m.focus)
	}
	spin(t, m, repeat(wheelUp, 4)...)
	if m.focus != 0 {
		t.Fatalf("focus %d, want the first row", m.focus)
	}
}

func TestViewOptionsWheel(t *testing.T) {
	m := NewViewOptionsModel(ViewOptionsConfig{
		PanelIndex:        0,
		PanelModes:        []PanelViewMode{PanelModeList, PanelModeManifest},
		ActivePanelMode:   PanelModeList,
		PanelWidthPercent: 50,
		TableMode:         "scroll",
	})
	m.SetDimensions(40, 2)
	first := m.focus
	keys := NewViewOptionsModel(ViewOptionsConfig{
		PanelIndex:        0,
		PanelModes:        []PanelViewMode{PanelModeList, PanelModeManifest},
		ActivePanelMode:   PanelModeList,
		PanelWidthPercent: 50,
		TableMode:         "scroll",
	})
	keys.SetDimensions(40, 2)
	keys.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	spin(t, m, wheelDown)
	if m.focus != keys.focus || m.focus == first {
		t.Fatalf("focus %d, want %d as with Down", m.focus, keys.focus)
	}
	spin(t, m, repeat(wheelDown, len(m.entries))...)
	if m.focus < m.scroll || m.focus >= m.scroll+2 {
		t.Fatalf("focus %d not visible from scroll %d", m.focus, m.scroll)
	}
	spin(t, m, repeat(wheelUp, len(m.entries))...)
	if m.focus != first || m.scroll > m.focus {
		t.Fatalf("focus %d scroll %d, want back on %d", m.focus, m.scroll, first)
	}
	before := m.commit(true, false)
	spin(t, m, wheelDown, wheelUp)
	if after := m.commit(true, false); after != before {
		t.Fatal("the wheel changed an option value")
	}
}

func TestAccessViewWheel(t *testing.T) {
	target := access.Target{Resource: podsResource, Namespace: "ns", Name: "p"}
	m := newAccessViewModel(target, access.Identity{Context: "dev"}, access.Policy{}, nil)
	copied := false
	m.onCopy = func(string) tea.Cmd { copied = true; return nil }
	m.SetDimensions(90, accessHeaderLines+accessFooterLines+2) // two list rows
	if len(m.checks) < 3 {
		t.Fatalf("need at least three checks, have %d", len(m.checks))
	}
	spin(t, m, repeat(wheelDown, 2)...)
	if m.selected != 2 || m.scroll != 1 {
		t.Fatalf("selected %d scroll %d, want 2 visible at 1", m.selected, m.scroll)
	}
	spin(t, m, repeat(wheelUp, 5)...)
	if m.selected != 0 || m.scroll != 0 || copied {
		t.Fatalf("selected %d scroll %d copied %t", m.selected, m.scroll, copied)
	}
}

func TestScopePickerWheelDoesNotPick(t *testing.T) {
	picked := false
	m := NewScopePickerModel(namespaceChoices([]string{"a", "b"}, "a"), "a", func(scopeChoice) tea.Cmd {
		picked = true
		return func() tea.Msg { return nil }
	}, nil)
	m.SetDimensions(20, 5)
	spin(t, m, repeat(wheelDown, 5)...)
	if m.selected != len(m.visible)-1 || picked {
		t.Fatalf("selected %d picked %t", m.selected, picked)
	}
	spin(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelRight})
	if m.selected != len(m.visible)-1 {
		t.Fatalf("a horizontal wheel moved the selection to %d", m.selected)
	}
}

// TestAppMenuBarWheel checks the App gives the wheel to the dropped-down
// menu and the menu only moves its highlight.
func TestAppMenuBarWheel(t *testing.T) {
	app := newMenuTestApp(t)
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: tea.KeyDown})
	if !app.menuBar.Dropped() {
		t.Fatal("F9 Down did not drop a menu down")
	}
	start := app.menuBar.Selected()
	bx, by, _, _ := app.menuBar.DropdownBounds()
	app = feedMenu(app, tea.MouseWheelMsg{X: bx + 2, Y: by + 1, Button: tea.MouseWheelDown})
	if app.menuBar.Selected() <= start || !app.menuBar.Dropped() {
		t.Fatalf("selected %d (was %d) dropped=%t", app.menuBar.Selected(), start, app.menuBar.Dropped())
	}
	app = feedMenu(app, tea.MouseWheelMsg{X: bx + 2, Y: by + 1, Button: tea.MouseWheelUp})
	if app.menuBar.Selected() != start || !app.menuBar.IsOpen() {
		t.Fatalf("selected %d, want %d back", app.menuBar.Selected(), start)
	}
}

// TestAppDialogWheel opens Options > Left panel options and checks the
// wheel reaches the dialog over its window and is dropped elsewhere.
func TestAppDialogWheel(t *testing.T) {
	app := newMenuTestApp(t)
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'o', Text: "o"}, tea.KeyPressMsg{Code: 'l', Text: "l"})
	modal := app.modalManager.GetActiveModal()
	if modal == nil {
		t.Fatal("no dialog open")
	}
	opts, ok := modal.content.(*ViewOptionsModel)
	if !ok {
		t.Fatalf("dialog content is %T", modal.content)
	}
	app.View() // lays the window out
	win := modal.drawn
	if win.Width == 0 {
		t.Fatal("the dialog is not windowed")
	}
	start := opts.focus
	app = feedMenu(app, tea.MouseWheelMsg{X: win.X + win.Width + 1, Y: win.Y + 1, Button: tea.MouseWheelDown})
	if opts.focus != start {
		t.Fatalf("a wheel outside the dialog moved the focus to %d", opts.focus)
	}
	app = feedMenu(app, tea.MouseWheelMsg{X: win.X + 2, Y: win.Y + 2, Button: tea.MouseWheelDown})
	if opts.focus == start {
		t.Fatal("a wheel over the dialog did not move the focus")
	}
	if app.modalManager.GetActiveModal() != modal {
		t.Fatal("the wheel closed the dialog")
	}
}
