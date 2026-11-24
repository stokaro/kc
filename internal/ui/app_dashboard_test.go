package ui

// These tests drive the App's routing of the dashboard's top row, which
// is unexported; the dashboard model and the menu bar are tested
// black-box in their packages.

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/dashboard"
)

// newDashboardTestApp is an App showing a dashboard of the namespace shop
// on fake sources, w by h.
func newDashboardTestApp(t *testing.T, w, h int) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	src := dashboard.Sources{
		Context: "kind-test",
		Scope:   "shop",
		Now:     func() time.Time { return now },
		Loc:     time.UTC,
		Tick:    func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil },
		Amounts: func(models.UsageFocus, int) models.UsageAmounts {
			return models.UsageAmounts{
				At: now, ReadAt: now, Found: true, Measured: 1, Running: 1,
				CPU:    models.ResourceAmount{Used: 0.2, UsedKnown: true},
				Memory: models.ResourceAmount{Used: 100 << 20, UsedKnown: true},
			}
		},
	}
	app.dash = &dashboardState{model: dashboard.New(src, dashboard.Target{Kind: dashboard.KindScope, Namespace: "shop"}), open: true}
	app = feedMenu(app, tea.WindowSizeMsg{Width: w, Height: h})
	app.dash.model.Start()
	return app
}

// TestDashboardStateOpensTheRangeMenuUnderIt: a click on the state at the
// right end of the dashboard's top row drops the Range menu down under
// it, right-aligned to the screen, with the state still in view. It works
// as any menu. A click on a title still drops that menu under it.
func TestDashboardStateOpensTheRangeMenuUnderIt(t *testing.T) {
	for _, size := range [][2]int{{150, 42}, {100, 30}} {
		w, h := size[0], size[1]
		app := newDashboardTestApp(t, w, h)
		state := screenLines(app)[0]
		x, sw := app.dash.model.StateCell()
		if x <= 0 || sw <= 0 || !strings.Contains(string([]rune(state)[x:]), "LIVE") {
			t.Fatalf("%dx%d: state cell %d+%d in %q", w, h, x, sw, state)
		}

		app = feedMenu(app, leftClick(x+1, 0), leftRelease(x+1, 0))
		bx, by, bw, bh := app.menuBar.DropdownBounds()
		if !app.menuBar.Anchored() || app.menuBar.Current() != dashboard.RangeMenu || by != 1 || bx+bw != w {
			t.Fatalf("%dx%d: anchored=%t menu=%d bounds %d,%d %dx%d; want the Range menu under the state, ending at column %d",
				w, h, app.menuBar.Anchored(), app.menuBar.Current(), bx, by, bw, bh, w)
		}
		lines := screenLines(app)
		if lines[0] != state {
			t.Errorf("%dx%d: the top row changed under the open pop-up:\n%q\nwant\n%q", w, h, lines[0], state)
		}
		if row := []rune(lines[2]); !strings.Contains(string(row[bx:]), "*Live") || row[w-1] != '│' {
			t.Errorf("%dx%d: the first item row is %q, want *Live in a frame at the right edge", w, h, lines[2])
		}

		// Keys reach the pop-up; Esc closes it and keeps the dashboard.
		app = feedMenu(app, tea.KeyPressMsg{Code: tea.KeyEscape})
		if app.menuBar.IsOpen() || !app.dashboardOpen() {
			t.Fatalf("%dx%d: Esc: menu open=%t dashboard open=%t", w, h, app.menuBar.IsOpen(), app.dashboardOpen())
		}

		// A click on the state again closes the pop-up.
		app = feedMenu(app, leftClick(x+1, 0), leftRelease(x+1, 0), leftClick(x+1, 0), leftRelease(x+1, 0))
		if app.menuBar.IsOpen() || !app.dashboardOpen() {
			t.Fatalf("%dx%d: a second click on the state: menu open=%t dashboard open=%t", w, h, app.menuBar.IsOpen(), app.dashboardOpen())
		}

		// An item chosen with the mouse runs: Pause here.
		app = feedMenu(app, leftClick(x+1, 0), leftRelease(x+1, 0))
		bx, by, _, _ = app.menuBar.DropdownBounds()
		pauseRow := by + 2
		if !strings.Contains(screenLines(app)[pauseRow], "Pause here") {
			t.Fatalf("%dx%d: row %d is %q, want Pause here", w, h, pauseRow, screenLines(app)[pauseRow])
		}
		app = feedMenu(app, leftClick(bx+3, pauseRow), leftRelease(bx+3, pauseRow))
		if app.menuBar.IsOpen() || !strings.Contains(screenLines(app)[0], "PAUSED at 10:00:00") {
			t.Fatalf("%dx%d: Pause here: menu open=%t, top row %q", w, h, app.menuBar.IsOpen(), screenLines(app)[0])
		}

		// The Range title still drops the menu under the title, over the
		// top row as the menu bar does.
		title := strings.Index(state, "Range")
		app = feedMenu(app, leftClick(title, 0), leftRelease(title, 0))
		bx, _, _, _ = app.menuBar.DropdownBounds()
		if !app.menuBar.Dropped() || app.menuBar.Anchored() || app.menuBar.Current() != dashboard.RangeMenu || bx != title-2 {
			t.Fatalf("%dx%d: click on Range: dropped=%t anchored=%t menu=%d x=%d, want under the title at %d",
				w, h, app.menuBar.Dropped(), app.menuBar.Anchored(), app.menuBar.Current(), bx, title-2)
		}
		if strings.Contains(screenLines(app)[0], "PAUSED") {
			t.Errorf("%dx%d: the open menu bar does not cover the top row: %q", w, h, screenLines(app)[0])
		}
		app = feedMenu(app, tea.KeyPressMsg{Code: tea.KeyEscape})

		// A click on the breadcrumb, between the titles and the state,
		// opens nothing.
		x, _ = app.dash.model.StateCell()
		app = feedMenu(app, leftClick(x-2, 0), leftRelease(x-2, 0))
		if app.menuBar.IsOpen() {
			t.Errorf("%dx%d: a click left of the state opened a menu", w, h)
		}
	}
}

func leftClick(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func leftRelease(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
}
