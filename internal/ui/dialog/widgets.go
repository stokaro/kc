package dialog

import (
	"strings"
	"unicode"

	textinput "charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// Label is a widget caption with an optional hotkey. As in mc, an "&"
// marks the letter after it as the hotkey: "&Overwrite" shows "Overwrite"
// with the "O" highlighted, and M-o (or o while no input line has the
// focus) picks the widget. "&&" is a literal "&".
type Label string

// parse returns the caption without the marker, the hotkey (lower case,
// 0 when there is none) and its rune index in the caption.
func (l Label) parse() (text string, hot rune, at int) {
	var b strings.Builder
	at = -1
	runes := []rune(string(l))
	n := 0
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '&' && i+1 < len(runes) {
			i++
			r = runes[i]
			if r != '&' && at < 0 {
				at, hot = n, unicode.ToLower(r)
			}
		}
		b.WriteRune(r)
		n++
	}
	return b.String(), hot, at
}

// Text is the caption without the hotkey marker.
func (l Label) Text() string {
	t, _, _ := l.parse()
	return t
}

// Hotkey is the lower-case hotkey letter, or 0.
func (l Label) Hotkey() rune {
	_, h, _ := l.parse()
	return h
}

// render draws prefix, the caption and suffix in base, with the hotkey
// letter in hot.
func (l Label) render(prefix, suffix string, base, hot lipgloss.Style) string {
	text, _, at := l.parse()
	if at < 0 {
		return base.Render(prefix + text + suffix)
	}
	r := []rune(text)
	return base.Render(prefix+string(r[:at])) + hot.Render(string(r[at])) + base.Render(string(r[at+1:])+suffix)
}

// Button is a push button. The default button, the one Enter presses from
// any widget but another button, is drawn "[< OK >]"; the others "[ OK ]".
// A disabled button is drawn grayed, without its hotkey; the owner keeps
// the focus off it and ignores it.
type Button struct {
	ID       string
	Label    Label
	Default  bool
	Disabled bool
}

func (b Button) ends() (open, closing string) {
	if b.Default {
		return "[< ", " >]"
	}
	return "[ ", " ]"
}

// Width is the number of columns the button takes.
func (b Button) Width() int {
	open, closing := b.ends()
	return lipgloss.Width(open + b.Label.Text() + closing)
}

// Render draws the button, focused or not, in skin s.
func (b Button) Render(s Skin, focused bool) string {
	open, closing := b.ends()
	if b.Disabled {
		return s.Disabled.Render(open + b.Label.Text() + closing)
	}
	if focused {
		return b.Label.render(open, closing, s.Focus, s.HotFocus)
	}
	return b.Label.render(open, closing, s.Normal, s.Hot)
}

// RenderCheckbox draws "[x] label" or "[ ] label" in skin s.
func RenderCheckbox(s Skin, label Label, on, focused bool) string {
	mark := "[ ] "
	if on {
		mark = "[x] "
	}
	if focused {
		return label.render(mark, "", s.Focus, s.HotFocus)
	}
	return label.render(mark, "", s.Normal, s.Hot)
}

// ButtonGap is the space between two buttons of a row, as in mc.
const ButtonGap = 1

// Rect is a widget's place in a rendered dialog, in content cells.
type Rect struct{ X, Y, W, H int }

// Contains reports whether the cell x, y is inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// RenderButtons draws buttons centered on a line width columns wide; focused
// is the index of the focused button, or -1. It returns the line and each
// button's place on it (Y is 0).
func RenderButtons(s Skin, width int, buttons []Button, focused int) (string, []Rect) {
	total := 0
	for i, b := range buttons {
		total += b.Width()
		if i > 0 {
			total += ButtonGap
		}
	}
	x := max(0, (width-total)/2)
	var out strings.Builder
	out.WriteString(s.Normal.Render(strings.Repeat(" ", x)))
	rects := make([]Rect, len(buttons))
	for i, b := range buttons {
		if i > 0 {
			out.WriteString(s.Normal.Render(strings.Repeat(" ", ButtonGap)))
			x += ButtonGap
		}
		rects[i] = Rect{X: x, Y: 0, W: b.Width(), H: 1}
		out.WriteString(b.Render(s, i == focused))
		x += b.Width()
	}
	if rest := width - x; rest > 0 {
		out.WriteString(s.Normal.Render(strings.Repeat(" ", rest)))
	}
	return out.String(), rects
}

// RenderRule draws a separator line width columns wide. A Modal joins it
// to the frame with tees when the content reports its row.
func RenderRule(s Skin, width int) string {
	return s.Normal.Render(strings.Repeat("─", max(0, width)))
}

// InputStyles is the text input look of skin s: the whole line in the
// input color, with the terminal's own cursor.
func InputStyles(s Skin) textinput.Styles {
	st := textinput.DefaultLightStyles()
	st.Focused.Text = s.Input
	st.Focused.Prompt = s.Input
	st.Focused.Placeholder = s.Input.Faint(true)
	st.Focused.Suggestion = s.Input.Faint(true)
	st.Blurred = st.Focused
	st.Cursor.Color = nil
	return st
}

// NewInput returns a text input in skin s that uses the real cursor.
func NewInput(s Skin, value string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 0
	ti.SetVirtualCursor(false)
	ti.SetStyles(InputStyles(s))
	ti.SetValue(value)
	ti.CursorEnd()
	return ti
}
