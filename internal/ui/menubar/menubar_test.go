package menubar_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/menubar"
)

// recorder builds menus whose items record the label they ran.
type recorder struct{ ran []string }

func (r *recorder) item(label, shortcut string) menubar.Item {
	return menubar.Item{Label: label, Shortcut: shortcut, Run: func() tea.Cmd {
		r.ran = append(r.ran, label)
		return nil
	}}
}

func (r *recorder) menus() []menubar.Menu {
	return []menubar.Menu{
		{Title: "&Left", Items: []menubar.Item{
			r.item("&List", ""),
			r.item("&Describe", ""),
			menubar.Separator(),
			r.item("&Reread", "C-r"),
		}},
		{Title: "&File", Items: []menubar.Item{
			{Label: "&View", Shortcut: "F3", Disabled: true, Run: func() tea.Cmd { return nil }},
			r.item("&Edit", "F4"),
			{Label: "&Nothing"}, // no Run: not selectable
			r.item("E&xit", "F10"),
		}},
		{Title: "&Command", Items: []menubar.Item{
			r.item("&Swap panels", "C-u"),
			r.item("Switch panels &on/off", "C-o"),
		}},
	}
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "f10":
		return tea.KeyPressMsg{Code: tea.KeyF10}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func TestNavigation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		keys     []string
		wantOpen bool
		wantCur  int
		wantSel  int
	}{
		{name: "opens on first menu and item", wantOpen: true, wantCur: 0, wantSel: 0},
		{name: "down moves", keys: []string{"down"}, wantOpen: true, wantCur: 0, wantSel: 1},
		{name: "down skips separator", keys: []string{"down", "down"}, wantOpen: true, wantCur: 0, wantSel: 3},
		{name: "down wraps", keys: []string{"down", "down", "down"}, wantOpen: true, wantCur: 0, wantSel: 0},
		{name: "up wraps", keys: []string{"up"}, wantOpen: true, wantCur: 0, wantSel: 3},
		{name: "end and home", keys: []string{"end", "home"}, wantOpen: true, wantCur: 0, wantSel: 0},
		{name: "right moves to next menu, first selectable item", keys: []string{"right"}, wantOpen: true, wantCur: 1, wantSel: 1},
		{name: "down skips disabled and items without Run", keys: []string{"right", "down"}, wantOpen: true, wantCur: 1, wantSel: 3},
		{name: "left wraps to last menu", keys: []string{"left"}, wantOpen: true, wantCur: 2, wantSel: 0},
		{name: "right wraps to first menu", keys: []string{"right", "right", "right"}, wantOpen: true, wantCur: 0, wantSel: 0},
		{name: "esc closes", keys: []string{"esc"}, wantOpen: false, wantCur: 0, wantSel: 0},
		{name: "f10 closes", keys: []string{"right", "f10"}, wantOpen: false, wantCur: 1, wantSel: 1},
		{name: "unknown key is ignored", keys: []string{"9"}, wantOpen: true, wantCur: 0, wantSel: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &recorder{}
			var b menubar.Bar
			b.OpenAt(r.menus(), 80, 0)
			for _, k := range tc.keys {
				b.HandleKey(key(k))
			}
			if b.IsOpen() != tc.wantOpen || b.Current() != tc.wantCur || b.Selected() != tc.wantSel {
				t.Fatalf("open=%t cur=%d sel=%d, want open=%t cur=%d sel=%d",
					b.IsOpen(), b.Current(), b.Selected(), tc.wantOpen, tc.wantCur, tc.wantSel)
			}
			if len(r.ran) != 0 {
				t.Fatalf("navigation ran %v", r.ran)
			}
		})
	}
}

func TestOpenRemembersLastMenu(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	var b menubar.Bar
	b.OpenAt(r.menus(), 80, 0)
	b.HandleKey(key("right"))
	b.HandleKey(key("esc"))
	b.Open(r.menus(), 80)
	if b.Current() != 1 || b.Dropped() {
		t.Fatalf("reopened on menu %d dropped=%t, want menu 1 not dropped", b.Current(), b.Dropped())
	}
}

// Open shows only the bar, as mc's F9 does; a title hotkey, Enter or Down
// drops a menu down, and letters then pick items.
func TestOpenWithoutDropDown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		keys        []string
		wantOpen    bool
		wantDropped bool
		wantCur     int
		wantRan     string
	}{
		{name: "open shows the bar only", wantOpen: true, wantCur: 0},
		{name: "right moves the title", keys: []string{"right"}, wantOpen: true, wantCur: 1},
		{name: "left wraps", keys: []string{"left"}, wantOpen: true, wantCur: 2},
		{name: "down drops", keys: []string{"down"}, wantOpen: true, wantDropped: true, wantCur: 0},
		{name: "enter drops", keys: []string{"right", "enter"}, wantOpen: true, wantDropped: true, wantCur: 1},
		{name: "title hotkey drops", keys: []string{"c"}, wantOpen: true, wantDropped: true, wantCur: 2},
		{name: "title then item hotkey runs", keys: []string{"f", "e"}, wantCur: 1, wantRan: "&Edit"},
		{name: "letters match titles, not items", keys: []string{"r"}, wantOpen: true, wantCur: 0},
		{name: "esc closes", keys: []string{"esc"}, wantCur: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &recorder{}
			var b menubar.Bar
			b.Open(r.menus(), 80)
			for _, k := range tc.keys {
				b.HandleKey(key(k))
			}
			if b.IsOpen() != tc.wantOpen || b.Dropped() != tc.wantDropped || b.Current() != tc.wantCur {
				t.Fatalf("open=%t dropped=%t cur=%d, want %t %t %d",
					b.IsOpen(), b.Dropped(), b.Current(), tc.wantOpen, tc.wantDropped, tc.wantCur)
			}
			if got := strings.Join(r.ran, ","); got != tc.wantRan {
				t.Fatalf("ran %q, want %q", got, tc.wantRan)
			}
			if !b.Dropped() && b.RenderDropdown() != "" {
				t.Fatal("drop-down drawn while not dropped")
			}
		})
	}
}

func TestRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		keys     []string
		wantRan  []string
		wantOpen bool
	}{
		{name: "enter runs highlighted item", keys: []string{"down", "enter"}, wantRan: []string{"&Describe"}},
		{name: "item hotkey runs", keys: []string{"r"}, wantRan: []string{"&Reread"}},
		{name: "item hotkey is case-insensitive", keys: []string{"R"}, wantRan: []string{"&Reread"}},
		{name: "title hotkey drops the menu down", keys: []string{"c", "s"}, wantRan: []string{"&Swap panels"}},
		{name: "item hotkey wins over title hotkey", keys: []string{"l"}, wantRan: []string{"&List"}},
		{name: "item hotkey in another menu", keys: []string{"right", "e"}, wantRan: []string{"&Edit"}},
		{name: "hotkey after a non-first letter", keys: []string{"right", "x"}, wantRan: []string{"E&xit"}},
		{name: "disabled item hotkey does not run", keys: []string{"right", "v"}, wantOpen: true},
		{name: "hotkey of an item without Run does not run", keys: []string{"right", "n"}, wantOpen: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &recorder{}
			var b menubar.Bar
			b.OpenAt(r.menus(), 80, 0)
			for _, k := range tc.keys {
				b.HandleKey(key(k))
			}
			if strings.Join(r.ran, ",") != strings.Join(tc.wantRan, ",") {
				t.Fatalf("ran %v, want %v", r.ran, tc.wantRan)
			}
			if b.IsOpen() != tc.wantOpen {
				t.Fatalf("open=%t, want %t", b.IsOpen(), tc.wantOpen)
			}
		})
	}
}

func TestAltLetterSelectsMenu(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	var b menubar.Bar
	b.OpenAt(r.menus(), 80, 0)
	// M-c picks the Command menu; Alt never runs an item.
	b.HandleKey(tea.KeyPressMsg{Code: 'c', Text: "c", Mod: tea.ModAlt})
	if b.Current() != 2 || len(r.ran) != 0 {
		t.Fatalf("cur=%d ran=%v, want menu 2 and nothing run", b.Current(), r.ran)
	}
}

func TestRunClosesBarBeforeAction(t *testing.T) {
	t.Parallel()
	var b menubar.Bar
	openDuringRun := true
	b.OpenAt([]menubar.Menu{{Title: "&A", Items: []menubar.Item{{Label: "&X", Run: func() tea.Cmd {
		openDuringRun = b.IsOpen()
		return nil
	}}}}}, 80, 0)
	b.HandleKey(key("enter"))
	if openDuringRun {
		t.Fatal("bar was still open while the item ran")
	}
}

func TestItemHotkeyAndText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		label    string
		wantText string
		wantHot  rune
	}{
		{"&View", "View", 'v'},
		{"E&xit", "Exit", 'x'},
		{"Switch panels &on/off", "Switch panels on/off", 'o'},
		{"Copy && paste", "Copy & paste", 0},
		{"Plain", "Plain", 0},
	}
	for _, tc := range tests {
		it := menubar.Item{Label: tc.label}
		if it.Text() != tc.wantText || it.Hotkey() != tc.wantHot {
			t.Errorf("%q: text=%q hot=%q, want %q %q", tc.label, it.Text(), it.Hotkey(), tc.wantText, tc.wantHot)
		}
	}
}

func TestRenderBar(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	var b menubar.Bar
	b.Open(r.menus(), 60)
	got := ansi.Strip(b.RenderBar(60))
	want := "  Left     File     Command "
	if !strings.HasPrefix(got, want) {
		t.Fatalf("bar = %q, want prefix %q", got, want)
	}
	if w := ansi.StringWidth(b.RenderBar(60)); w != 60 {
		t.Fatalf("bar width = %d, want 60", w)
	}
	// A narrow screen drops titles that do not fit but keeps the width.
	if got := ansi.Strip(b.RenderBar(12)); got != "  Left      " {
		t.Fatalf("narrow bar = %q", got)
	}
}

func TestRenderDropdownAlignsShortcuts(t *testing.T) {
	t.Parallel()
	var b menubar.Bar
	run := func() tea.Cmd { return nil }
	b.OpenAt([]menubar.Menu{{Title: "&Command", Items: []menubar.Item{
		{Label: "&Swap panels", Shortcut: "C-u", Run: run},
		{Label: "Switch panels &on/off", Shortcut: "C-o", Run: run},
		menubar.Separator(),
		{Label: "&Find file", Shortcut: "M-?", Run: run},
		{Label: "&Help", Shortcut: "F1", Run: run, Checked: true},
		{Label: "&User menu", Run: run},
	}}}, 80, 0)
	out := b.RenderDropdown()
	lines := strings.Split(ansi.Strip(out), "\n")
	_, _, w, h := b.DropdownBounds()
	if len(lines) != h {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), h, ansi.Strip(out))
	}
	for i, l := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("line %d width %d, want %d: %q", i, got, w, ansi.Strip(l))
		}
	}
	want := []string{
		"┌───────────────────────────┐",
		"│ Swap panels           C-u │",
		"│ Switch panels on/off  C-o │",
		"├───────────────────────────┤",
		"│ Find file             M-? │",
		"│*Help                   F1 │",
		"│ User menu                 │",
		"└───────────────────────────┘",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("dropdown:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestDropdownStaysOnScreen(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	var b menubar.Bar
	b.OpenAt(r.menus(), 30, 2)
	x, y, w, _ := b.DropdownBounds()
	if y != 1 || x+w > 30 || x < 0 {
		t.Fatalf("bounds x=%d y=%d w=%d leave a 30 column screen", x, y, w)
	}
	b.OpenAt(r.menus(), 80, 1)
	if x, _, _, _ := b.DropdownBounds(); x != 9 {
		// "File" cell starts at column 10; the frame sits one to the left.
		t.Fatalf("File drop-down at x=%d, want 9", x)
	}
}

func TestHitTesting(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	var b menubar.Bar
	b.OpenAt(r.menus(), 80, 0)
	// "  Left     File     Command"
	for _, tc := range []struct{ x, want int }{
		{0, -1}, {1, 0}, {6, 0}, {7, -1}, {10, 1}, {15, 1}, {16, -1}, {19, 2}, {27, 2}, {28, -1},
	} {
		if got := b.TitleAt(tc.x); got != tc.want {
			t.Errorf("TitleAt(%d) = %d, want %d", tc.x, got, tc.want)
		}
	}
	x, y, w, h := b.DropdownBounds()
	for _, tc := range []struct {
		name string
		x, y int
		want int
	}{
		{"top frame", x + 1, y, -1},
		{"left frame", x, y + 1, -1},
		{"right frame", x + w - 1, y + 1, -1},
		{"bottom frame", x + 1, y + h - 1, -1},
		{"first item", x + 1, y + 1, 0},
		{"separator", x + 2, y + 3, 2},
		{"last item", x + w - 2, y + 4, 3},
		{"outside", x + w + 3, y + 1, -1},
	} {
		if got := b.ItemAt(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: ItemAt(%d,%d) = %d, want %d", tc.name, tc.x, tc.y, got, tc.want)
		}
	}
}

func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func release(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func TestMouse(t *testing.T) {
	t.Parallel()
	t.Run("click on a title drops it down", func(t *testing.T) {
		t.Parallel()
		r := &recorder{}
		var b menubar.Bar
		b.OpenAt(r.menus(), 80, 0)
		b.HandleMouse(click(20, 0))
		b.HandleMouse(release(20, 0))
		if !b.IsOpen() || b.Current() != 2 {
			t.Fatalf("open=%t cur=%d, want open on menu 2", b.IsOpen(), b.Current())
		}
	})
	t.Run("click on the open title closes", func(t *testing.T) {
		t.Parallel()
		r := &recorder{}
		var b menubar.Bar
		b.OpenAt(r.menus(), 80, 0)
		b.HandleMouse(click(2, 0))
		if b.IsOpen() {
			t.Fatal("bar still open")
		}
	})
	t.Run("click and release on an item runs it", func(t *testing.T) {
		t.Parallel()
		r := &recorder{}
		var b menubar.Bar
		b.OpenAt(r.menus(), 80, 0)
		x, y, _, _ := b.DropdownBounds()
		b.HandleMouse(click(x+2, y+2))
		if len(r.ran) != 0 || b.Selected() != 1 {
			t.Fatalf("press ran %v sel=%d; want only highlight of item 1", r.ran, b.Selected())
		}
		b.HandleMouse(release(x+2, y+2))
		if strings.Join(r.ran, ",") != "&Describe" || b.IsOpen() {
			t.Fatalf("ran %v open=%t, want &Describe and closed", r.ran, b.IsOpen())
		}
	})
	t.Run("release on another item does not run", func(t *testing.T) {
		t.Parallel()
		r := &recorder{}
		var b menubar.Bar
		b.OpenAt(r.menus(), 80, 0)
		x, y, _, _ := b.DropdownBounds()
		b.HandleMouse(click(x+2, y+1))
		b.HandleMouse(release(x+2, y+4))
		if len(r.ran) != 0 || !b.IsOpen() {
			t.Fatalf("ran %v open=%t", r.ran, b.IsOpen())
		}
	})
	t.Run("click on a separator keeps the menu", func(t *testing.T) {
		t.Parallel()
		r := &recorder{}
		var b menubar.Bar
		b.OpenAt(r.menus(), 80, 0)
		x, y, _, _ := b.DropdownBounds()
		b.HandleMouse(click(x+2, y+3))
		b.HandleMouse(release(x+2, y+3))
		if len(r.ran) != 0 || !b.IsOpen() {
			t.Fatalf("ran %v open=%t", r.ran, b.IsOpen())
		}
	})
	t.Run("click outside closes", func(t *testing.T) {
		t.Parallel()
		r := &recorder{}
		var b menubar.Bar
		b.OpenAt(r.menus(), 80, 0)
		b.HandleMouse(click(70, 15))
		if b.IsOpen() {
			t.Fatal("bar still open")
		}
	})
	t.Run("click on empty bar space closes", func(t *testing.T) {
		t.Parallel()
		r := &recorder{}
		var b menubar.Bar
		b.OpenAt(r.menus(), 80, 0)
		b.HandleMouse(click(60, 0))
		if b.IsOpen() {
			t.Fatal("bar still open")
		}
	})
}

func TestClosedBarIgnoresInput(t *testing.T) {
	t.Parallel()
	var b menubar.Bar
	if cmd := b.HandleKey(key("enter")); cmd != nil || b.IsOpen() {
		t.Fatal("closed bar reacted to a key")
	}
	b.HandleMouse(click(2, 0))
	if b.IsOpen() {
		t.Fatal("closed bar opened on a click")
	}
	if b.RenderDropdown() != "" {
		t.Fatal("closed bar drew a drop-down")
	}
	if b.ItemAt(1, 1) != -1 {
		t.Fatal("closed bar reported an item")
	}
}
