package dialog

import (
	"strings"
	"unicode"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Item is one row of a Form.
type Item interface{ item() }

// Text is a line of text, left-aligned after a one-column margin and cut
// to the dialog width. Empty text is a blank line.
type Text struct {
	Text   string
	Center bool
	Bold   bool
	Error  bool // drawn in the skin's error color
}

// Rule is a separator line across the whole dialog.
type Rule struct{}

// Input is an input line. The Form focuses and blurs Model; the owner
// reads its value.
type Input struct {
	Model *textinput.Model
}

// Check is a check box. The Form flips On when the user toggles it.
type Check struct {
	ID    string
	Label Label
	On    *bool
}

// Buttons is a centered row of buttons; each one is a focus stop.
type Buttons []Button

func (Text) item()    {}
func (Rule) item()    {}
func (Input) item()   {}
func (Check) item()   {}
func (Buttons) item() {}

// Event reports what an Update did.
type Event struct {
	// Pressed is the ID of the button the user pressed.
	Pressed string
	// Toggled is the ID of the check box the user toggled.
	Toggled string
	// Edited is set when a key changed an input line's value.
	Edited bool
}

// Form lays out Items and handles the keys and clicks mc dialogs share:
// Tab and Shift-Tab (and Up and Down) move the focus, Left and Right move
// it along a button row, Space toggles a check box or presses a button,
// Enter presses the focused button or else the default one, M-<hotkey>
// picks the widget with that hotkey (the plain letter does too while no
// input line has the focus), and a click focuses, toggles or presses.
// Esc is left to the Modal.
//
// The owner may replace Items between calls (to show progress or an
// error); the focus stays on the same stop index.
type Form struct {
	Skin  Skin
	Items []Item
	// AltOnly makes hotkeys need Meta even when no input line has the
	// focus, so a stray letter cannot press a button. Red dialogs set it.
	AltOnly bool

	focus int
	// rects are the focus stops' places in the last View, by stop index.
	rects []Rect
	rules []int
}

type stop struct {
	item   int
	button int // index in a Buttons row, or -1
}

func (f *Form) stops() []stop {
	var out []stop
	for i, it := range f.Items {
		switch it := it.(type) {
		case Input, Check:
			out = append(out, stop{item: i, button: -1})
		case Buttons:
			for j := range it {
				out = append(out, stop{item: i, button: j})
			}
		}
	}
	return out
}

func (f *Form) current() (stop, bool) {
	stops := f.stops()
	if len(stops) == 0 {
		return stop{}, false
	}
	f.focus = min(max(f.focus, 0), len(stops)-1)
	return stops[f.focus], true
}

// setFocus moves the focus to stop index i and focuses or blurs the input
// lines to match.
func (f *Form) setFocus(i int) tea.Cmd {
	stops := f.stops()
	if len(stops) == 0 {
		return nil
	}
	f.focus = (i%len(stops) + len(stops)) % len(stops)
	var cmd tea.Cmd
	for si, s := range stops {
		in, ok := f.Items[s.item].(Input)
		if !ok || in.Model == nil {
			continue
		}
		if si == f.focus {
			cmd = in.Model.Focus()
		} else {
			in.Model.Blur()
		}
	}
	return cmd
}

// Refocus applies the focus to the input lines again, for an owner that
// replaced Items. Use it after building the first Items.
func (f *Form) Refocus() tea.Cmd { return f.setFocus(f.focus) }

// FocusButton moves the focus to the button with id.
func (f *Form) FocusButton(id string) tea.Cmd {
	for i, s := range f.stops() {
		if b, ok := f.Items[s.item].(Buttons); ok && b[s.button].ID == id {
			return f.setFocus(i)
		}
	}
	return nil
}

// FocusFirst moves the focus to the first stop.
func (f *Form) FocusFirst() tea.Cmd { return f.setFocus(0) }

// FocusedButton is the ID of the focused button, or "".
func (f *Form) FocusedButton() string {
	s, ok := f.current()
	if !ok || s.button < 0 {
		return ""
	}
	return f.Items[s.item].(Buttons)[s.button].ID
}

// FocusedInput reports whether an input line has the focus.
func (f *Form) FocusedInput() bool {
	s, ok := f.current()
	if !ok {
		return false
	}
	_, in := f.Items[s.item].(Input)
	return in
}

func (f *Form) defaultButton() string {
	for _, it := range f.Items {
		if b, ok := it.(Buttons); ok {
			for _, btn := range b {
				if btn.Default {
					return btn.ID
				}
			}
		}
	}
	return ""
}

// Update handles a key or mouse message.
func (f *Form) Update(msg tea.Msg) (Event, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return f.key(msg)
	case tea.MouseClickMsg:
		return f.click(msg.Mouse(), false)
	case tea.MouseReleaseMsg:
		return f.click(msg.Mouse(), true)
	}
	return Event{}, nil
}

func (f *Form) key(msg tea.KeyPressMsg) (Event, tea.Cmd) {
	s, ok := f.current()
	if !ok {
		return Event{}, nil
	}
	switch msg.String() {
	case "tab", "down":
		return Event{}, f.setFocus(f.focus + 1)
	case "shift+tab", "up":
		return Event{}, f.setFocus(f.focus - 1)
	case "enter":
		if s.button >= 0 {
			return Event{Pressed: f.Items[s.item].(Buttons)[s.button].ID}, nil
		}
		return Event{Pressed: f.defaultButton()}, nil
	}
	if ev, cmd, ok := f.hotkey(msg); ok {
		return ev, cmd
	}
	switch it := f.Items[s.item].(type) {
	case Input:
		if it.Model == nil {
			return Event{}, nil
		}
		before := it.Model.Value()
		var cmd tea.Cmd
		*it.Model, cmd = it.Model.Update(msg)
		return Event{Edited: it.Model.Value() != before}, cmd
	case Check:
		if msg.String() == "space" || msg.Text == " " {
			*it.On = !*it.On
			return Event{Toggled: it.ID}, nil
		}
	case Buttons:
		switch msg.String() {
		case "left":
			if s.button > 0 {
				return Event{}, f.setFocus(f.focus - 1)
			}
		case "right":
			if s.button < len(it)-1 {
				return Event{}, f.setFocus(f.focus + 1)
			}
		case "space":
			return Event{Pressed: it[s.button].ID}, nil
		}
	}
	return Event{}, nil
}

// hotkey picks the widget whose hotkey msg names: M-<letter> always, the
// plain letter while no input line has the focus.
func (f *Form) hotkey(msg tea.KeyPressMsg) (Event, tea.Cmd, bool) {
	key := msg.Key()
	var r rune
	switch {
	case key.Mod == tea.ModAlt && unicode.IsLetter(key.Code):
		r = key.Code
	case !f.AltOnly && key.Mod&^tea.ModShift == 0 && !f.FocusedInput() && len([]rune(key.Text)) == 1:
		r = []rune(key.Text)[0]
	default:
		return Event{}, nil, false
	}
	r = unicode.ToLower(r)
	if r == 0 || r == ' ' {
		return Event{}, nil, false
	}
	for i, s := range f.stops() {
		switch it := f.Items[s.item].(type) {
		case Check:
			if it.Label.Hotkey() == r {
				cmd := f.setFocus(i)
				*it.On = !*it.On
				return Event{Toggled: it.ID}, cmd, true
			}
		case Buttons:
			if it[s.button].Label.Hotkey() == r {
				cmd := f.setFocus(i)
				return Event{Pressed: it[s.button].ID}, cmd, true
			}
		}
	}
	return Event{}, nil, false
}

// click handles a left click: the press focuses the widget under it (and
// toggles a check box), the release over a button presses it.
func (f *Form) click(m tea.Mouse, release bool) (Event, tea.Cmd) {
	if m.Button != tea.MouseLeft {
		return Event{}, nil
	}
	stops := f.stops()
	for i, r := range f.rects {
		if i >= len(stops) || !r.Contains(m.X, m.Y) {
			continue
		}
		s := stops[i]
		switch it := f.Items[s.item].(type) {
		case Input:
			if release {
				return Event{}, nil
			}
			return Event{}, f.setFocus(i)
		case Check:
			if release {
				return Event{}, nil
			}
			cmd := f.setFocus(i)
			*it.On = !*it.On
			return Event{Toggled: it.ID}, cmd
		case Buttons:
			cmd := f.setFocus(i)
			if release {
				return Event{Pressed: it[s.button].ID}, cmd
			}
			return Event{}, cmd
		}
	}
	return Event{}, nil
}

// margin is the blank column on each side of text and widgets.
const margin = 1

// View renders the form width columns wide. It returns the content, the
// cursor of the focused input line (or nil) and the rows of the Rules,
// for the frame to join.
func (f *Form) View(width int) (string, *tea.Cursor, []int) {
	width = max(width, 2*margin+1)
	stops := f.stops()
	f.current()
	f.rects = make([]Rect, len(stops))
	f.rules = f.rules[:0]
	inner := width - 2*margin
	pad := f.Skin.Normal.Render(strings.Repeat(" ", margin))
	var lines []string
	var cursor *tea.Cursor
	stopAt := 0
	for _, it := range f.Items {
		y := len(lines)
		switch it := it.(type) {
		case Text:
			style := f.Skin.Normal
			if it.Error {
				style = f.Skin.Error
			}
			style = style.Bold(it.Bold)
			text := ansi.Truncate(it.Text, inner, "")
			if it.Center {
				text = strings.Repeat(" ", (inner-lipgloss.Width(text))/2) + text
			}
			text += strings.Repeat(" ", max(0, inner-lipgloss.Width(text)))
			lines = append(lines, pad+style.Render(text)+pad)
		case Rule:
			f.rules = append(f.rules, y)
			lines = append(lines, RenderRule(f.Skin, width))
		case Input:
			if it.Model != nil {
				// One column stays free for the cursor after the text.
				it.Model.SetWidth(max(1, inner-1))
				field := f.Skin.Input.Width(inner).MaxWidth(inner).Render(it.Model.View())
				lines = append(lines, pad+field+pad)
				if stopAt == f.focus {
					if c := it.Model.Cursor(); c != nil {
						c.Position.X += margin
						c.Position.Y = y
						cursor = c
					}
				}
			} else {
				lines = append(lines, pad+f.Skin.Input.Render(strings.Repeat(" ", inner))+pad)
			}
			f.rects[stopAt] = Rect{X: margin, Y: y, W: inner, H: 1}
			stopAt++
		case Check:
			label := ansi.Truncate(RenderCheckbox(f.Skin, it.Label, *it.On, stopAt == f.focus), inner, "")
			w := lipgloss.Width(label)
			f.rects[stopAt] = Rect{X: margin, Y: y, W: w, H: 1}
			stopAt++
			lines = append(lines, pad+label+f.Skin.Normal.Render(strings.Repeat(" ", max(0, inner-w)))+pad)
		case Buttons:
			focused := -1
			if f.focus >= stopAt && f.focus < stopAt+len(it) {
				focused = f.focus - stopAt
			}
			row, rects := RenderButtons(f.Skin, width, it, focused)
			for j, r := range rects {
				r.Y = y
				f.rects[stopAt+j] = r
			}
			stopAt += len(it)
			lines = append(lines, row)
		}
	}
	return strings.Join(lines, "\n"), cursor, append([]int(nil), f.rules...)
}

// Width is the narrowest width that shows every line of the form whole,
// given minInput columns for each input line.
func (f *Form) Width(minInput int) int {
	w := 0
	for _, it := range f.Items {
		switch it := it.(type) {
		case Text:
			w = max(w, lipgloss.Width(it.Text)+2*margin)
		case Input:
			w = max(w, minInput+2*margin)
		case Check:
			w = max(w, lipgloss.Width("[x] "+it.Label.Text())+2*margin)
		case Buttons:
			total := 0
			for i, b := range it {
				total += b.Width()
				if i > 0 {
					total += ButtonGap
				}
			}
			w = max(w, total+2*margin)
		}
	}
	return w
}

// Height is the number of lines the form takes.
func (f *Form) Height() int { return len(f.Items) }
