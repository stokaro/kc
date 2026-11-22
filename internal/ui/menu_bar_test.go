package ui

// These tests drive the App, whose menu table and key routing are unexported;
// the menu bar state machine itself is tested black-box in package menubar.

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func newMenuTestApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	model, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return model.(*App)
}

func feedMenu(app *App, msgs ...tea.Msg) *App {
	for _, msg := range msgs {
		model, _ := app.Update(msg)
		app = model.(*App)
	}
	return app
}

func topLine(app *App) string {
	view := viewString(app.View())
	return ansi.Strip(strings.SplitN(view, "\n", 2)[0])
}

var (
	keyF9  = tea.KeyPressMsg{Code: tea.KeyF9}
	keyEsc = tea.KeyPressMsg{Code: tea.KeyEscape}
)

func TestF9OpensMenuBar(t *testing.T) {
	app := newMenuTestApp(t)
	app = feedMenu(app, keyF9)
	if !app.menuBar.IsOpen() {
		t.Fatal("F9 did not open the menu bar")
	}
	line := topLine(app)
	if !strings.HasPrefix(line, "  Left     File     Command     Options     Right") {
		t.Fatalf("top line = %q", line)
	}
	app = feedMenu(app, keyEsc)
	if app.menuBar.IsOpen() {
		t.Fatal("Esc did not close the menu bar")
	}
	// The visible bar (the default) stays; the hidden one goes away.
	if !strings.HasPrefix(topLine(app), "  Left     File") {
		t.Fatalf("visible menu bar gone after closing: %q", topLine(app))
	}
	app.cfg.Layout.MenuBarVisible = false
	app = feedMenu(app, keyF9)
	if !strings.HasPrefix(topLine(app), "  Left     File") {
		t.Fatalf("F9 did not draw the hidden menu bar: %q", topLine(app))
	}
	app = feedMenu(app, keyEsc)
	if strings.HasPrefix(topLine(app), "  Left     File") {
		t.Fatal("hidden menu bar still drawn after closing")
	}
}

func TestEscNineOpensMenuBar(t *testing.T) {
	app := newMenuTestApp(t)
	app = feedMenu(app, keyEsc, tea.KeyPressMsg{Code: '9', Text: "9"})
	if !app.menuBar.IsOpen() {
		t.Fatal("Esc 9 did not open the menu bar")
	}
}

func TestF9DoesNothingInFullscreenTerminal(t *testing.T) {
	app := newMenuTestApp(t)
	app.showTerminal = true
	app.openMenuBar(-1)
	if app.menuBar.IsOpen() {
		t.Fatal("menu bar opened over the fullscreen terminal")
	}
}

func TestMenuBarTable(t *testing.T) {
	app := newMenuTestApp(t)
	shortcut := regexp.MustCompile(`^(F[0-9]+|(C-x )?[CM]-(F[0-9]+|.)|[+*-])$`)
	menus := app.menuBarMenus()
	titles := make(map[rune]string)
	for _, m := range menus {
		if h := m.Hotkey(); h == 0 {
			t.Errorf("menu %q has no hotkey", m.Title)
		} else if prev, dup := titles[h]; dup {
			t.Errorf("menus %q and %q share hotkey %q", prev, m.Title, h)
		} else {
			titles[h] = m.Title
		}
		hot := make(map[rune]string)
		for _, it := range m.Items {
			if it.IsSeparator() {
				continue
			}
			if it.Run == nil {
				t.Errorf("%s: %q has no action", m.Title, it.Label)
			}
			if it.Shortcut != "" && !shortcut.MatchString(it.Shortcut) {
				t.Errorf("%s: %q shortcut %q is not in mc notation", m.Title, it.Label, it.Shortcut)
			}
			h := it.Hotkey()
			if h == 0 {
				t.Errorf("%s: %q has no hotkey", m.Title, it.Label)
				continue
			}
			if prev, dup := hot[h]; dup {
				t.Errorf("%s: %q and %q share hotkey %q", m.Title, prev, it.Label, h)
			}
			hot[h] = it.Label
		}
	}
}

func TestMenuBarRunsSwapPanels(t *testing.T) {
	app := newMenuTestApp(t)
	left, right := app.leftPanel, app.rightPanel
	// F9, C (Command), S (Swap panels).
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'c', Text: "c"}, tea.KeyPressMsg{Code: 's', Text: "s"})
	if app.menuBar.IsOpen() {
		t.Fatal("menu bar still open after running an entry")
	}
	if app.leftPanel != right || app.rightPanel != left {
		t.Fatal("Swap panels did not swap the panels")
	}
}

func TestMenuBarSetsPanelMode(t *testing.T) {
	app := newMenuTestApp(t)
	// F9, R (Right), M (Manifest).
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'r', Text: "r"}, tea.KeyPressMsg{Code: 'm', Text: "m"})
	if got := app.rightPanel.Mode(); got != PanelModeManifest {
		t.Fatalf("right panel mode = %v, want Manifest", got)
	}
	if got := app.leftPanel.Mode(); got != PanelModeList {
		t.Fatalf("left panel mode = %v, want List", got)
	}
	// The Right menu now checks Manifest.
	for _, it := range app.panelMenuItems(1) {
		if it.Text() == "Manifest" && !it.Checked {
			t.Fatal("Manifest is not checked in the Right menu")
		}
		if it.Text() == "List" && it.Checked {
			t.Fatal("List is still checked in the Right menu")
		}
	}
}

// TestMenuBarSetsLogsMode covers #139: Logs mode was reachable only with
// C-1 / C-2, not from the Left and Right menus.
func TestMenuBarSetsLogsMode(t *testing.T) {
	app := newMenuTestApp(t)
	// F9, L (Left), G (Logs).
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'l', Text: "l"}, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if got := app.leftPanel.Mode(); got != PanelModeLogs {
		t.Fatalf("left panel mode = %v, want Logs", got)
	}
	if got := app.rightPanel.Mode(); got != PanelModeList {
		t.Fatalf("right panel mode = %v, want List", got)
	}
	checked := false
	for _, it := range app.panelMenuItems(0) {
		if it.Text() == "Logs" {
			checked = it.Checked
		}
	}
	if !checked {
		t.Fatal("Logs is missing or not checked in the Left menu")
	}
}

func TestMenuBarMouse(t *testing.T) {
	app := newMenuTestApp(t)
	// The PullDn slot of the function key bar opens the menu bar.
	keys := panelFunctionKeys(app.functionBarState())
	pull := -1
	for i, k := range keys {
		if k.ID == functionKeyPullDown {
			pull = i
		}
	}
	if pull < 0 {
		t.Fatal("no PullDn slot in the function key bar")
	}
	x := functionKeySlotStarts(len(keys), app.width)[pull]
	app = feedMenu(app, tea.MouseClickMsg{X: x, Y: app.height - 1, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: x, Y: app.height - 1, Button: tea.MouseLeft})
	if !app.menuBar.IsOpen() {
		t.Fatal("click on PullDn did not open the menu bar")
	}

	// A click on "Command" drops that menu down.
	cmdX := strings.Index(topLine(app), "Command")
	app = feedMenu(app, tea.MouseClickMsg{X: cmdX, Y: 0, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: cmdX, Y: 0, Button: tea.MouseLeft})
	if !app.menuBar.IsOpen() || app.menuBar.Current() != menuCommand {
		t.Fatalf("open=%t menu=%d, want Command open", app.menuBar.IsOpen(), app.menuBar.Current())
	}

	// A click on "Swap panels" runs it.
	left := app.leftPanel
	bx, by, _, _ := app.menuBar.DropdownBounds()
	row := -1
	for i, it := range app.menuBar.Items() {
		if it.Text() == "Swap panels" {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("no Swap panels entry")
	}
	y := by + 1 + row
	app = feedMenu(app, tea.MouseClickMsg{X: bx + 2, Y: y, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: bx + 2, Y: y, Button: tea.MouseLeft})
	if app.menuBar.IsOpen() || app.rightPanel != left {
		t.Fatalf("open=%t swapped=%t, want closed and swapped", app.menuBar.IsOpen(), app.rightPanel == left)
	}

	// A click outside closes the bar and does not reach the panels.
	app = feedMenu(app, keyF9, tea.MouseClickMsg{X: 90, Y: 20, Button: tea.MouseLeft})
	if app.menuBar.IsOpen() {
		t.Fatal("click outside did not close the menu bar")
	}
}

func TestShowModalClosesMenuBar(t *testing.T) {
	app := newMenuTestApp(t)
	app = feedMenu(app, keyF9)
	// Options > Left panel options opens a dialog.
	app = feedMenu(app, tea.KeyPressMsg{Code: 'o', Text: "o"}, tea.KeyPressMsg{Code: 'l', Text: "l"})
	if app.menuBar.IsOpen() {
		t.Fatal("menu bar still open")
	}
	if !app.modalManager.IsModalVisible() {
		t.Fatal("Left panel options did not open a dialog")
	}
}

func TestF2OpensUserMenuNotOptions(t *testing.T) {
	app := newMenuTestApp(t)
	app = feedMenu(app, tea.KeyPressMsg{Code: tea.KeyF2})
	if app.modalManager.IsModalVisible() {
		if _, ok := app.modalManager.GetActiveModal().content.(*ViewOptionsModel); ok {
			t.Fatal("F2 opened panel options; it is the user menu now")
		}
	}
}

func TestMenuBarLocalFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	app := newMenuTestApp(t)
	// F9, R (Right), E (Local files): the right panel lists the local
	// filesystem in List mode and the entry carries the mark.
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'r', Text: "r"}, tea.KeyPressMsg{Code: 'e', Text: "e"})
	if app.menuBar.IsOpen() {
		t.Fatal("menu bar still open after Local files")
	}
	if app.activePanel != 1 {
		t.Fatalf("active panel = %d, want the right panel", app.activePanel)
	}
	if mode := app.panelByIndex(1).Mode(); mode != PanelModeList {
		t.Fatalf("right panel mode = %v, want List", mode)
	}
	if !app.panelShowsLocal(1) {
		t.Fatal("right panel does not show the local filesystem")
	}
	marks := func() (list, local bool) {
		for _, it := range app.panelMenuItems(1) {
			switch it.Label {
			case "&List":
				list = it.Checked
			case "Local fil&es":
				local = it.Checked
			}
		}
		return list, local
	}
	if list, local := marks(); list || !local {
		t.Fatalf("marks list=%v local=%v, want only Local files", list, local)
	}
	// Chosen again, the panel returns to the cluster.
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'r', Text: "r"}, tea.KeyPressMsg{Code: 'e', Text: "e"})
	if app.panelShowsLocal(1) {
		t.Fatal("Local files chosen again did not leave the local filesystem")
	}
	if list, local := marks(); !list || local {
		t.Fatalf("marks list=%v local=%v, want only List", list, local)
	}
}
