package dashboard_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/investigation"
	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/internal/ui/menubar"
)

// rangeMenu is the Range menu as the App gets it: each item runs its key
// through the model, as App.dashboardKey does.
func rangeMenu(t *testing.T, m *dashboard.Model) []menubar.Item {
	t.Helper()
	return m.Menus(func(k string) tea.Cmd {
		cmd, _ := m.Key(k)
		return cmd
	})[dashboard.RangeMenu].Items
}

// rangeItem is the Range menu item whose text starts with prefix.
func rangeItem(t *testing.T, m *dashboard.Model, prefix string) menubar.Item {
	t.Helper()
	for _, it := range rangeMenu(t, m) {
		if !it.IsSeparator() && strings.HasPrefix(it.Text(), prefix) {
			return it
		}
	}
	t.Fatalf("the Range menu has no %q item", prefix)
	return menubar.Item{}
}

// choose runs the Range menu item whose text starts with prefix.
func choose(t *testing.T, m *dashboard.Model, prefix string) {
	t.Helper()
	it := rangeItem(t, m, prefix)
	if !it.Selectable() {
		t.Fatalf("%q cannot be chosen", it.Text())
	}
	run(t, m, it.Run())
}

// checked is the text of the checked items among the first ones of the
// Range menu, before its first separator: what the dashboard shows.
func checked(t *testing.T, m *dashboard.Model) []string {
	t.Helper()
	var out []string
	for _, it := range rangeMenu(t, m) {
		if it.IsSeparator() {
			break
		}
		if it.Checked {
			out = append(out, it.Text())
		}
	}
	return out
}

// TestRangeMenuStartsWithWhatTheDashboardShows: the first items of the
// Range menu say whether the dashboard is live, paused or follows the
// investigation window, with that one checked, and switch between them;
// the spans and the zoom follow. Space is on the item it runs.
func TestRangeMenuStartsWithWhatTheDashboardShows(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 150, 42)

	var texts []string
	hot := make(map[rune]string)
	for _, it := range rangeMenu(t, m) {
		texts = append(texts, it.Text())
		if h := it.Hotkey(); h != 0 {
			if other, dup := hot[h]; dup {
				t.Errorf("hotkey %q of %q is also %q's", h, it.Text(), other)
			}
			hot[h] = it.Text()
		}
	}
	want := []string{"Live", "Pause here", "Follow the investigation window", "Set the investigation window to this range", "", "Last 15 minutes"}
	if got := texts[:len(want)]; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Range menu starts %q, want %q", got, want)
	}
	if got := texts[len(texts)-3:]; strings.Join(got, "|") != "|Zoom in|Zoom out" {
		t.Errorf("Range menu ends %q, want the zoom after a separator", got)
	}
	if it := rangeItem(t, m, "Follow"); it.Selectable() {
		t.Error("Follow the investigation window can be chosen with no window set")
	}
	if got := checked(t, m); strings.Join(got, "|") != "Live" {
		t.Errorf("checked %q while live, want Live", got)
	}
	if rangeItem(t, m, "Pause here").Shortcut != "Space" || rangeItem(t, m, "Live").Shortcut != "" {
		t.Error("Space is not on Pause here while live")
	}

	// Pause here freezes the range at now.
	choose(t, m, "Pause here")
	requireText(t, screen(t, m, 150, 42)[:1], "PAUSED at 10:00:00 · 1h")
	if got := checked(t, m); strings.Join(got, "|") != "Paused at 10:00:00" {
		t.Errorf("checked %q while paused, want Paused at 10:00:00", got)
	}
	if rangeItem(t, m, "Live").Shortcut != "Space" {
		t.Error("Space is not on Live while paused")
	}

	// Live goes live again and removes the cursor, as Space does.
	key(t, m, "left")
	choose(t, m, "Live")
	lines := screen(t, m, 150, 42)
	requireText(t, lines[:1], "LIVE · last 1 hour")
	if contains(lines, "(highest)") {
		t.Error("Live kept the cursor")
	}

	// Set the investigation window to this range: the view follows it.
	choose(t, m, "Set the investigation window")
	if f.win != (investigation.Window{Last: time.Hour}) {
		t.Fatalf("the window is %+v, want the live 1h", f.win)
	}
	requireText(t, screen(t, m, 150, 42)[:1], "LIVE window 1h")
	if got := checked(t, m); strings.Join(got, "|") != "Follow the investigation window" {
		t.Errorf("checked %q while following, want the window", got)
	}

	// Live leaves a live window and keeps its span.
	choose(t, m, "Live")
	requireText(t, screen(t, m, 150, 42)[:1], "LIVE · last 1 hour ≠ window")
	if got := checked(t, m); strings.Join(got, "|") != "Live" {
		t.Errorf("checked %q after leaving the window, want Live", got)
	}

	// Follow the investigation window comes back to it, in a drilldown
	// and in the levels above it.
	key(t, m, "enter")
	requireText(t, screen(t, m, 150, 42)[:1], "› pod web-1", "≠ window")
	choose(t, m, "Follow the investigation window")
	requireText(t, screen(t, m, 150, 42)[:1], "LIVE window 1h")
	key(t, m, "esc")
	requireText(t, screen(t, m, 150, 42)[:1], "› Deployment web", "LIVE window 1h")
}

// TestRangeMenuOnAFixedWindow: a fixed window shows paused at its end.
// Paused at leaves the window and stays at that end; Live leaves it for
// now.
func TestRangeMenuOnAFixedWindow(t *testing.T) {
	t.Parallel()
	start, end := t0.Add(-time.Hour), t0.Add(-30*time.Minute)
	f := &fakes{now: t0, amounts: measured(), win: investigation.Window{Start: start, End: end}}
	m := open(t, f, true, web, 150, 42)
	requireText(t, screen(t, m, 150, 42)[:1], "WINDOW 2026-11-23 09:00:00 .. 09:30:00")
	if got := checked(t, m); strings.Join(got, "|") != "Follow the investigation window" {
		t.Errorf("checked %q on a fixed window, want the window", got)
	}
	if it := rangeItem(t, m, "Paused at 09:30:00"); it.Checked || !it.Selectable() {
		t.Errorf("Paused at 09:30:00: checked %t, selectable %t; want an unchecked choice", it.Checked, it.Selectable())
	}
	choose(t, m, "Paused at 09:30:00")
	requireText(t, screen(t, m, 150, 42)[:1], "PAUSED at 09:30:00 · 30m ≠ window")
	if got := checked(t, m); strings.Join(got, "|") != "Paused at 09:30:00" {
		t.Errorf("checked %q, want Paused at 09:30:00", got)
	}
	choose(t, m, "Follow")
	requireText(t, screen(t, m, 150, 42)[:1], "WINDOW 2026-11-23 09:00:00 .. 09:30:00")
	choose(t, m, "Live")
	requireText(t, screen(t, m, 150, 42)[:1], "LIVE · last 30 minutes ≠ window")
}

// TestRangeMenuWithoutAWindow: a dashboard that cannot set the
// investigation window and has none offers no window items.
func TestRangeMenuWithoutAWindow(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	src := f.sources(false)
	src.Window, src.SetWindow = nil, nil
	m := dashboard.New(src, web)
	run(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 100, Height: 30} })
	run(t, m, m.Start())
	for _, it := range rangeMenu(t, m) {
		if strings.Contains(it.Text(), "window") {
			t.Errorf("the Range menu offers %q without a window", it.Text())
		}
	}
	if got := checked(t, m); strings.Join(got, "|") != "Live" {
		t.Errorf("checked %q, want Live", got)
	}
}

// TestStateCell: StateCell is where the top bar draws the state, at the
// right end of the row, at every width.
func TestStateCell(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{150, 42}, {100, 30}, {80, 24}} {
		w, h := size[0], size[1]
		f := &fakes{now: t0, amounts: measured()}
		m := open(t, f, true, web, w, h)
		for _, pause := range []bool{false, true} {
			if pause {
				key(t, m, "space")
			}
			x, sw := m.StateCell()
			top := []rune(screen(t, m, w, h)[0])
			cell := strings.TrimSpace(string(top[x : x+sw]))
			if x+sw != w || (!strings.HasPrefix(cell, "LIVE") && !strings.HasPrefix(cell, "PAUSED")) || string(top[x]) != " " {
				t.Errorf("%dx%d paused=%t: StateCell %d+%d holds %q, want the whole state at the right end", w, h, pause, x, sw, string(top[x:x+sw]))
			}
		}
	}
	m := dashboard.New((&fakes{now: t0, amounts: measured()}).sources(false), web)
	run(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 10, Height: 4} })
	if _, sw := m.StateCell(); sw != 0 {
		t.Errorf("a screen too small for the dashboard has a state cell %d wide", sw)
	}
}
