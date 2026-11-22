package menubar

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Colors follow mc's default skin ([menu] in default.ini): white on cyan
// for titles, entries, shortcuts, the frame and the separators; yellow
// hotkey letters; a black highlight bar with white text; gray for entries
// that cannot be chosen. While the bar is closed, mc draws the titles in
// black on cyan with no hotkey letters, so the idle bar stays quiet.
var (
	idleStyle     = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black)
	normalStyle   = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.BrightWhite)
	hotStyle      = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.BrightYellow)
	selStyle      = lipgloss.NewStyle().Background(lipgloss.Black).Foreground(lipgloss.BrightWhite)
	selHotStyle   = lipgloss.NewStyle().Background(lipgloss.Black).Foreground(lipgloss.BrightYellow)
	disabledStyle = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.BrightBlack)
)

// RenderTitles draws the bar row for menus while the bar is closed: every
// title in the bar colors, none highlighted. It is the row mc keeps on top
// of the screen when the menu bar is visible.
func RenderTitles(menus []Menu, width int) string {
	b := Bar{menus: menus}
	return b.RenderBar(width)
}

// TitleIndex returns the index of the title of menus under column x of the
// bar row, or -1. It matches what RenderTitles draws.
func TitleIndex(menus []Menu, x int) int {
	b := Bar{menus: menus}
	return b.TitleAt(x)
}

// RenderBar draws the bar row, width columns wide. The selected title is
// highlighted while the bar is open.
func (b *Bar) RenderBar(width int) string {
	if width <= 0 {
		return ""
	}
	var out strings.Builder
	col := 0
	pad := func(to int) {
		if to > col {
			fill := normalStyle
			if !b.open {
				fill = idleStyle
			}
			out.WriteString(fill.Render(strings.Repeat(" ", to-col)))
			col = to
		}
	}
	for i, m := range b.menus {
		start, w := b.titleCell(i)
		if start+w > width {
			break
		}
		pad(start)
		base, hot := normalStyle, hotStyle
		if !b.open {
			base, hot = idleStyle, idleStyle
		}
		if b.open && i == b.cur {
			base, hot = selStyle, selHotStyle
		}
		out.WriteString(renderLabel(" "+m.Title+" ", base, hot))
		col += w
	}
	pad(width)
	return out.String()
}

// RenderDropdown draws the drop-down of the current menu, frame included,
// as lines of the width DropdownBounds reports. It returns "" when no menu
// is dropped down.
func (b *Bar) RenderDropdown() string {
	if !b.Dropped() {
		return ""
	}
	w, _, shortW := b.dropdownSize()
	inner := w - 2
	lines := make([]string, 0, len(b.items())+2)
	lines = append(lines, normalStyle.Render("┌"+strings.Repeat("─", inner)+"┐"))
	for i, it := range b.items() {
		if it.IsSeparator() {
			lines = append(lines, normalStyle.Render("├"+strings.Repeat("─", inner)+"┤"))
			continue
		}
		base, hot := normalStyle, hotStyle
		switch {
		case !it.Selectable():
			base, hot = disabledStyle, disabledStyle
		case i == b.sel:
			base, hot = selStyle, selHotStyle
		}
		mark := " "
		if it.Checked {
			mark = "*"
		}
		text := it.Text()
		var row strings.Builder
		row.WriteString(base.Render(mark))
		row.WriteString(renderLabel(it.Label, base, hot))
		fill := inner - 1 - ansi.StringWidth(text) - shortW - 1
		row.WriteString(base.Render(strings.Repeat(" ", max(0, fill))))
		row.WriteString(base.Render(padLeft(it.Shortcut, shortW) + " "))
		lines = append(lines, normalStyle.Render("│")+row.String()+normalStyle.Render("│"))
	}
	lines = append(lines, normalStyle.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(lines, "\n")
}

func padLeft(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// renderLabel draws label in base with its hotkey letter in hot.
func renderLabel(label string, base, hot lipgloss.Style) string {
	text, i := splitLabel(label)
	if i < 0 {
		return base.Render(text)
	}
	r := []rune(text)
	return base.Render(string(r[:i])) + hot.Render(string(r[i])) + base.Render(string(r[i+1:]))
}
