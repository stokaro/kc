package ui

// White-box like menu_bar_test.go: the layout rows, the menu table and the
// configuration store of the App are unexported.

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/pkg/appconfig"
)

const idleBarPrefix = "  Left     File     Command     Options     Right"

// newLayoutTestApp returns a 100x30 App with layout.menuBarVisible set to
// visible and a configuration store in a temporary directory.
func newLayoutTestApp(t *testing.T, visible bool) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.cfg.Layout.MenuBarVisible = visible
	app.cfgStore = appconfig.NewStore(filepath.Join(t.TempDir(), "config.yaml"))
	model, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return model.(*App)
}

func screenLines(app *App) []string {
	lines := strings.Split(viewString(app.View()), "\n")
	for i, l := range lines {
		lines[i] = ansi.Strip(l)
	}
	return lines
}

func TestMenuBarVisibleLayout(t *testing.T) {
	tests := []struct {
		name     string
		visible  bool
		menuRows int
		// frameTop is the screen row of the top border of the panels.
		frameTop int
	}{
		{name: "bar visible", visible: true, menuRows: 1, frameTop: 1},
		{name: "bar hidden", visible: false, menuRows: 0, frameTop: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := newLayoutTestApp(t, tc.visible)
			if got := app.menuBarRows(); got != tc.menuRows {
				t.Fatalf("menuBarRows() = %d, want %d", got, tc.menuRows)
			}
			want := app.height - 1 - tc.menuRows - app.inlineTerminalHeight() - app.commandLineHeight()
			if got := app.panelsHeight(); got != want {
				t.Fatalf("panelsHeight() = %d, want %d", got, want)
			}
			lines := screenLines(app)
			if len(lines) != app.height {
				t.Fatalf("view has %d lines, want %d", len(lines), app.height)
			}
			if got := strings.HasPrefix(lines[0], idleBarPrefix); got != tc.visible {
				t.Fatalf("top line %q, bar drawn = %t, want %t", lines[0], got, tc.visible)
			}
			if !strings.HasPrefix(lines[tc.frameTop], "┌") {
				t.Fatalf("line %d = %q, want the top border of the panels", tc.frameTop, lines[tc.frameTop])
			}
			bottom := tc.frameTop + app.panelsHeight() - 1
			if !strings.HasPrefix(lines[bottom], "└") {
				t.Fatalf("line %d = %q, want the bottom border of the panels", bottom, lines[bottom])
			}
			if app.menuBar.IsOpen() {
				t.Fatal("the visible bar must not open a menu by itself")
			}
		})
	}
}

func TestMenuBarVisibleFollowsDefaultConfig(t *testing.T) {
	app := newMenuTestApp(t)
	if !app.menuBarVisible() || !strings.HasPrefix(topLine(app), idleBarPrefix) {
		t.Fatalf("default config: visible=%t top=%q, want the bar on the top row", app.menuBarVisible(), topLine(app))
	}
}

func TestMenuBarHiddenInFullscreenTerminal(t *testing.T) {
	app := newLayoutTestApp(t, true)
	app = feedMenu(app, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !app.showTerminal {
		t.Fatal("C-o did not show the terminal")
	}
	if got := app.menuBarRows(); got != 0 {
		t.Fatalf("menuBarRows() = %d with the terminal fullscreen, want 0", got)
	}
	if strings.HasPrefix(topLine(app), idleBarPrefix) {
		t.Fatal("menu bar drawn over the fullscreen terminal")
	}
}

func TestClickOnIdleMenuBarTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  int
	}{
		{name: "Left", title: "Left", want: menuLeft},
		{name: "Command", title: "Command", want: menuCommand},
		{name: "Options", title: "Options", want: menuOptions},
		{name: "Right", title: "Right", want: menuRight},
		{name: "gap between titles", title: "", want: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := newLayoutTestApp(t, true)
			x := 7 // between "Left" and "File"
			if tc.title != "" {
				x = strings.Index(topLine(app), tc.title)
			}
			app = feedMenu(app, tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft},
				tea.MouseReleaseMsg{X: x, Y: 0, Button: tea.MouseLeft})
			if tc.want < 0 {
				if app.menuBar.IsOpen() {
					t.Fatal("click between titles opened the menu bar")
				}
				return
			}
			if !app.menuBar.Dropped() || app.menuBar.Current() != tc.want {
				t.Fatalf("dropped=%t menu=%d, want menu %d dropped down", app.menuBar.Dropped(), app.menuBar.Current(), tc.want)
			}
		})
	}
}

func TestClickOnTopRowWithBarHiddenGoesToPanel(t *testing.T) {
	app := newLayoutTestApp(t, false)
	x := strings.Index(idleBarPrefix, "Options")
	app = feedMenu(app, tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: x, Y: 0, Button: tea.MouseLeft})
	if app.menuBar.IsOpen() {
		t.Fatal("a click on the panel border opened the hidden menu bar")
	}
}

func TestPanelRowClickWithMenuBar(t *testing.T) {
	for _, visible := range []bool{true, false} {
		name := "bar hidden"
		if visible {
			name = "bar visible"
		}
		t.Run(name, func(t *testing.T) {
			app := newLayoutTestApp(t, visible)
			ctx := t.Context()
			app.leftPanel.UseFolder(ctx, true)
			app.setPanelFolder(ctx, 0, newFilterFolder("alpha", "bravo", "charlie", "delta"), false)
			for _, target := range []string{"charlie", "alpha", "delta"} {
				lines := screenLines(app)
				row := -1
				for y, l := range lines {
					leftHalf := string([]rune(l)[:min(len([]rune(l)), app.width/2)])
					if strings.Contains(leftHalf, target) {
						row = y
						break
					}
				}
				if row < 0 {
					t.Fatalf("%s not drawn in the left panel:\n%s", target, strings.Join(lines, "\n"))
				}
				app = feedMenu(app, tea.MouseClickMsg{X: 4, Y: row, Button: tea.MouseLeft},
					tea.MouseReleaseMsg{X: 4, Y: row, Button: tea.MouseLeft})
				if got := app.leftPanel.currentSelectionID(ctx); got != target {
					t.Fatalf("click on screen row %d selected %q, want %q", row, got, target)
				}
			}
		})
	}
}

func TestScopeLabelRowFollowsMenuBar(t *testing.T) {
	for _, visible := range []bool{true, false} {
		app := newLayoutTestApp(t, visible)
		bottom := app.menuBarRows() + app.panelsHeight() - 1
		lines := screenLines(app)
		if !strings.HasPrefix(lines[bottom], "└") {
			t.Fatalf("visible=%t: line %d = %q, want the bottom border", visible, bottom, lines[bottom])
		}
		// Every column of another row misses the scope labels.
		for x := range app.width {
			if _, hit := app.scopeHitAt(tea.Mouse{X: x, Y: bottom - 1}); hit != "" {
				t.Fatalf("visible=%t: scope label %q hit on row %d, above the bottom border", visible, hit, bottom-1)
			}
		}
	}
}

func TestPanelModalStaysUnderMenuBar(t *testing.T) {
	for _, visible := range []bool{true, false} {
		app := newLayoutTestApp(t, visible)
		app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'o', Text: "o"}, tea.KeyPressMsg{Code: 'l', Text: "l"})
		modal := app.modalManager.GetActiveModal()
		if modal == nil {
			t.Fatalf("visible=%t: Left panel options did not open a dialog", visible)
		}
		win, ok := modal.WindowPosition()
		if !ok {
			t.Fatalf("visible=%t: dialog is not windowed", visible)
		}
		if minY := app.menuBarRows() + panelHeaderOffset; win.Y < minY {
			t.Fatalf("visible=%t: dialog at row %d, want at least %d", visible, win.Y, minY)
		}
		lines := screenLines(app)
		if got := strings.HasPrefix(lines[0], idleBarPrefix); got != visible {
			t.Fatalf("visible=%t: dialog screen top line %q", visible, lines[0])
		}
	}
}

func TestOptionsMenuTogglesMenuBarVisible(t *testing.T) {
	app := newLayoutTestApp(t, true)
	heightWithBar := app.panelsHeight()

	found := false
	for _, it := range app.optionsMenuItems() {
		if it.Text() == "Menu bar visible" {
			found = true
			if !it.Checked {
				t.Fatal("Menu bar visible is not checked while the bar is visible")
			}
		}
	}
	if !found {
		t.Fatal("no Menu bar visible entry in the Options menu")
	}

	// F9, Options, Menu bar visible.
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'o', Text: "o"})
	cmd := app.handleMenuBarKey(tea.KeyPressMsg{Code: 'm', Text: "m"})
	if app.menuBar.IsOpen() {
		t.Fatal("menu bar still open after the entry ran")
	}
	if app.menuBarVisible() || app.cfg.Layout.MenuBarVisible {
		t.Fatal("the entry did not turn the bar off")
	}
	if got := app.panelsHeight(); got != heightWithBar+1 {
		t.Fatalf("panelsHeight() = %d with the bar off, want %d", got, heightWithBar+1)
	}
	if strings.HasPrefix(topLine(app), idleBarPrefix) {
		t.Fatal("bar still drawn after turning it off")
	}

	runUntilSaved(t, app, cmd)
	saved, err := app.configStore().Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.Layout.MenuBarVisible {
		t.Fatal("layout.menuBarVisible not saved as false")
	}

	// F9 still opens the hidden bar; the entry turns it back on.
	app = feedMenu(app, keyF9, tea.KeyPressMsg{Code: 'o', Text: "o"})
	for _, it := range app.menuBar.Items() {
		if it.Text() == "Menu bar visible" && it.Checked {
			t.Fatal("Menu bar visible checked while the bar is off")
		}
	}
	cmd = app.handleMenuBarKey(tea.KeyPressMsg{Code: 'm', Text: "m"})
	if !app.menuBarVisible() || app.panelsHeight() != heightWithBar {
		t.Fatalf("visible=%t height=%d, want the bar back and height %d", app.menuBarVisible(), app.panelsHeight(), heightWithBar)
	}
	runUntilSaved(t, app, cmd)
	if saved, _ := app.configStore().Load(); !saved.Layout.MenuBarVisible {
		t.Fatal("layout.menuBarVisible not saved as true")
	}
}

// runUntilSaved runs cmd and the commands it batches until the save of the
// configuration reports back, and fails when it reports an error.
func runUntilSaved(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case configSavedMsg:
			if msg.err != nil {
				t.Fatalf("saving the setting: %v", msg.err)
			}
			return
		}
	}
	t.Fatal("no configuration save was run")
}
