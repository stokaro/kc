// Package menubar implements a pull-down menu bar like the one Midnight
// Commander opens with F9: a one-line bar of menu titles on the top screen
// row and a framed drop-down under the selected title.
//
// The bar holds no knowledge of the application. Callers describe menus as
// data (a slice of Menu, each with Items) and attach a Run function to each
// item. The bar handles keys and mouse events, and runs the chosen item.
package menubar

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Item is one entry of a drop-down menu.
//
// An Item with an empty Label is a separator. An Item with Disabled set or
// without Run is drawn dim and cannot be chosen.
type Item struct {
	// Label is the entry text. A '&' marks the next letter as the hotkey,
	// as in mc ("&View" has hotkey 'v'); "&&" is a literal '&'.
	Label string
	// Shortcut is the key that runs the same action outside the menu, in mc
	// notation ("F3", "C-u", "M-c"). It is drawn aligned to the right.
	Shortcut string
	// Checked draws a mark in front of the label, as for the current
	// listing mode.
	Checked bool
	// Disabled dims the entry.
	Disabled bool
	// Run executes the action. The bar is closed before Run is called.
	Run func() tea.Cmd
}

// Separator returns a separator item.
func Separator() Item { return Item{} }

// IsSeparator reports whether the item is a separator.
func (it Item) IsSeparator() bool { return it.Label == "" }

// Selectable reports whether the item can be highlighted and chosen.
func (it Item) Selectable() bool {
	return !it.IsSeparator() && !it.Disabled && it.Run != nil
}

// Hotkey returns the lower-cased hotkey letter of the label, or 0.
func (it Item) Hotkey() rune { return hotkey(it.Label) }

// Text returns the label without hotkey marks.
func (it Item) Text() string { text, _ := splitLabel(it.Label); return text }

// Menu is one title of the bar with its drop-down entries.
type Menu struct {
	// Title is drawn on the bar; '&' marks the hotkey letter.
	Title string
	Items []Item
}

// Hotkey returns the lower-cased hotkey letter of the title, or 0.
func (m Menu) Hotkey() rune { return hotkey(m.Title) }

// Bar is the state of the menu bar. The zero value is a closed bar.
type Bar struct {
	menus []Menu
	open  bool
	// dropped is set while the drop-down of the selected menu is shown.
	dropped bool
	cur     int // index of the selected menu
	sel     int // index of the highlighted item in the current menu, or -1
	// pressed is the item a mouse button went down on; the item runs when
	// the button is released over it.
	pressed int
	width   int
}

// Open shows the bar with menus and highlights the title that was selected
// the last time the bar was open (the first one initially), as mc does.
// No menu is dropped down yet: a title hotkey, Enter or Down does that.
func (b *Bar) Open(menus []Menu, width int) {
	b.open = len(menus) > 0
	b.menus = menus
	b.width = width
	b.dropped = false
	b.pressed = -1
	if b.open {
		b.selectMenu(min(max(b.cur, 0), len(menus)-1))
	}
}

// OpenAt shows the bar with menus and drops down menu i.
func (b *Bar) OpenAt(menus []Menu, width, i int) {
	b.cur = i
	b.Open(menus, width)
	b.dropped = b.open
}

// Close hides the bar. The selected menu is remembered for the next Open.
func (b *Bar) Close() {
	b.open = false
	b.dropped = false
	b.pressed = -1
}

// Dropped reports whether the drop-down of the selected menu is shown.
func (b *Bar) Dropped() bool { return b.open && b.dropped }

// IsOpen reports whether the bar is shown.
func (b *Bar) IsOpen() bool { return b.open }

// SetWidth sets the screen width the bar is laid out for.
func (b *Bar) SetWidth(width int) { b.width = width }

// Current returns the index of the selected menu.
func (b *Bar) Current() int { return b.cur }

// Selected returns the index of the highlighted item, or -1.
func (b *Bar) Selected() int { return b.sel }

func (b *Bar) selectMenu(i int) {
	b.cur = i
	b.sel = b.nextSelectable(-1, 1)
	b.pressed = -1
}

// Items returns the entries of the selected menu, or nil when the bar is
// closed.
func (b *Bar) Items() []Item { return b.items() }

func (b *Bar) items() []Item {
	if !b.open || b.cur < 0 || b.cur >= len(b.menus) {
		return nil
	}
	return b.menus[b.cur].Items
}

// nextSelectable returns the first selectable item after from in direction
// dir (1 or -1), wrapping around, or -1 when there is none.
func (b *Bar) nextSelectable(from, dir int) int {
	items := b.items()
	n := len(items)
	if n == 0 {
		return -1
	}
	i := from
	for range n {
		i = (i + dir + n) % n
		if items[i].Selectable() {
			return i
		}
	}
	return -1
}

// HandleKey processes a key while the bar is open. Every key is consumed.
// The returned command is the result of the item that ran, if any.
func (b *Bar) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	if !b.open {
		return nil
	}
	n := len(b.menus)
	if !b.dropped {
		switch msg.String() {
		case "esc", "f9", "f10", "ctrl+c", "ctrl+g":
			b.Close()
		case "left", "shift+tab":
			b.selectMenu((b.cur - 1 + n) % n)
		case "right", "tab":
			b.selectMenu((b.cur + 1) % n)
		case "down", "enter", "space":
			b.dropped = true
		default:
			if i := b.menuByHotkey(msg); i >= 0 {
				b.selectMenu(i)
				b.dropped = true
			}
		}
		return nil
	}
	switch msg.String() {
	case "esc", "f9", "f10", "ctrl+c", "ctrl+g":
		b.Close()
	case "left", "shift+tab":
		b.selectMenu((b.cur - 1 + n) % n)
	case "right", "tab":
		b.selectMenu((b.cur + 1) % n)
	case "up":
		b.sel = b.nextSelectable(b.sel, -1)
	case "down":
		b.sel = b.nextSelectable(b.sel, 1)
	case "home", "pgup":
		b.sel = b.nextSelectable(-1, 1)
	case "end", "pgdown":
		b.sel = b.nextSelectable(len(b.items()), -1)
	case "enter", "space":
		return b.run(b.sel)
	default:
		return b.hotkey(msg)
	}
	return nil
}

// hotkey picks an item of the dropped-down menu by its hotkey and runs it;
// failing that, or with Meta held, it drops down the menu with that title
// hotkey.
func (b *Bar) hotkey(msg tea.KeyPressMsg) tea.Cmd {
	want, alt := keyLetter(msg)
	if want == 0 {
		return nil
	}
	if !alt {
		for i, it := range b.items() {
			if it.Selectable() && it.Hotkey() == want {
				return b.run(i)
			}
		}
	}
	if i := b.menuByHotkey(msg); i >= 0 {
		b.selectMenu(i)
	}
	return nil
}

// menuByHotkey returns the menu whose title hotkey the key is, or -1.
func (b *Bar) menuByHotkey(msg tea.KeyPressMsg) int {
	want, _ := keyLetter(msg)
	if want == 0 {
		return -1
	}
	for i, m := range b.menus {
		if m.Hotkey() == want {
			return i
		}
	}
	return -1
}

// keyLetter returns the lower-cased character of a plain, shifted or Meta
// key, and whether Meta was held; 0 for other keys.
func keyLetter(msg tea.KeyPressMsg) (rune, bool) {
	key := msg.Key()
	if key.Mod&^(tea.ModShift|tea.ModAlt) != 0 {
		return 0, false
	}
	r := key.Code
	if rs := []rune(key.Text); len(rs) == 1 {
		r = rs[0]
	} else if key.Text != "" {
		return 0, false
	}
	if !unicode.IsPrint(r) || unicode.IsSpace(r) {
		return 0, false
	}
	return unicode.ToLower(r), key.Mod&tea.ModAlt != 0
}

// run closes the bar and runs item i when it can be chosen.
func (b *Bar) run(i int) tea.Cmd {
	items := b.items()
	if i < 0 || i >= len(items) || !items[i].Selectable() {
		return nil
	}
	it := items[i]
	b.Close()
	return it.Run()
}

// HandleMouse processes a mouse event while the bar is open. Every event is
// consumed: a click on a title drops that menu down, a click on an item runs
// it when the button is released over it, and a click anywhere else closes
// the bar.
func (b *Bar) HandleMouse(msg tea.MouseMsg) tea.Cmd {
	if !b.open {
		return nil
	}
	m := msg.Mouse()
	if m.Button != tea.MouseLeft {
		return nil
	}
	switch msg.(type) {
	case tea.MouseClickMsg:
		if m.Y == 0 {
			if i := b.TitleAt(m.X); i >= 0 {
				if i == b.cur && b.dropped {
					b.Close()
				} else {
					b.selectMenu(i)
					b.dropped = true
				}
				return nil
			}
			b.Close()
			return nil
		}
		if i := b.ItemAt(m.X, m.Y); i >= 0 {
			if b.items()[i].Selectable() {
				b.sel = i
				b.pressed = i
			}
			return nil
		}
		if x, y, w, h := b.DropdownBounds(); m.X >= x && m.X < x+w && m.Y >= y && m.Y < y+h {
			// The frame or a separator: keep the menu open.
			return nil
		}
		b.Close()
	case tea.MouseReleaseMsg:
		pressed := b.pressed
		b.pressed = -1
		if i := b.ItemAt(m.X, m.Y); i >= 0 && i == pressed {
			return b.run(i)
		}
	}
	return nil
}

// Layout of the bar, as in mc: titles start at column 1, each drawn as
// " Title " with three columns between them.
const (
	barStartX = 1
	barGap    = 3
)

// titleCell returns the start column and width of the cell of title i.
func (b *Bar) titleCell(i int) (x, width int) {
	x = barStartX
	for j := range i {
		x += titleWidth(b.menus[j].Title) + barGap
	}
	return x, titleWidth(b.menus[i].Title)
}

func titleWidth(title string) int {
	text, _ := splitLabel(title)
	return ansi.StringWidth(text) + 2
}

// TitleAt returns the index of the menu title under column x on the bar
// row, or -1.
func (b *Bar) TitleAt(x int) int {
	for i := range b.menus {
		start, w := b.titleCell(i)
		if x >= start && x < start+w {
			return i
		}
	}
	return -1
}

// dropdownSize returns the outer width and height of the drop-down of the
// current menu, frame included.
func (b *Bar) dropdownSize() (w, h, shortW int) {
	items := b.items()
	labelW := 0
	for _, it := range items {
		labelW = max(labelW, ansi.StringWidth(it.Text()))
		shortW = max(shortW, ansi.StringWidth(it.Shortcut))
	}
	gap := 0
	if shortW > 0 {
		gap = 2
	}
	// mark column + label + gap + shortcut + right pad
	inner := 1 + labelW + gap + shortW + 1
	return inner + 2, len(items) + 2, shortW
}

// DropdownBounds returns the screen rectangle of the drop-down, frame
// included. The drop-down hangs under its title and is shifted left when it
// would run past the right edge of the screen.
//
//revive:disable-next-line:function-result-limit a rectangle is four named coordinates
func (b *Bar) DropdownBounds() (x, y, w, h int) {
	if !b.Dropped() || len(b.menus) == 0 {
		return 0, 0, 0, 0
	}
	w, h, _ = b.dropdownSize()
	x, _ = b.titleCell(b.cur)
	x--
	if b.width > 0 && x+w > b.width {
		x = b.width - w
	}
	return max(0, x), 1, w, h
}

// ItemAt returns the index of the drop-down item under (x, y), or -1. Frame
// cells do not count; separators do.
func (b *Bar) ItemAt(x, y int) int {
	if !b.Dropped() {
		return -1
	}
	bx, by, w, h := b.DropdownBounds()
	if x <= bx || x >= bx+w-1 || y <= by || y >= by+h-1 {
		return -1
	}
	return y - by - 1
}

// hotkey returns the lower-cased letter after the first single '&'.
func hotkey(label string) rune {
	_, i := splitLabel(label)
	if i < 0 {
		return 0
	}
	text, _ := splitLabel(label)
	return unicode.ToLower([]rune(text)[i])
}

// splitLabel removes the hotkey marks from label and returns the plain
// text and the rune index of the hotkey letter in it, or -1.
func splitLabel(label string) (string, int) {
	var b strings.Builder
	hot := -1
	runes := []rune(label)
	n := 0
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '&' && i+1 < len(runes) {
			i++
			r = runes[i]
			if r != '&' && hot < 0 {
				hot = n
			}
		}
		b.WriteRune(r)
		n++
	}
	return b.String(), hot
}
