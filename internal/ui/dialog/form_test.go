package dialog_test

import (
	"strings"
	"testing"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/dialog"
)

func TestLabelHotkey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		label dialog.Label
		text  string
		hot   rune
	}{
		{"&OK", "OK", 'o'},
		{"Over&write", "Overwrite", 'w'},
		{"Cancel", "Cancel", 0},
		{"A && B", "A & B", 0},
		{"&Save && &quit", "Save & quit", 's'},
	}
	for _, tc := range cases {
		t.Run(string(tc.label), func(t *testing.T) {
			t.Parallel()
			if got := tc.label.Text(); got != tc.text {
				t.Errorf("Text() = %q, want %q", got, tc.text)
			}
			if got := tc.label.Hotkey(); got != tc.hot {
				t.Errorf("Hotkey() = %q, want %q", got, tc.hot)
			}
		})
	}
}

func TestButtonRender(t *testing.T) {
	t.Parallel()
	s := dialog.Normal()
	cases := []struct {
		name    string
		button  dialog.Button
		focused bool
		want    string
	}{
		{name: "default", button: dialog.Button{Label: "&OK", Default: true}, want: "[< OK >]"},
		{name: "plain", button: dialog.Button{Label: "&Cancel"}, want: "[ Cancel ]"},
		{name: "focused", button: dialog.Button{Label: "Background"}, focused: true, want: "[ Background ]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.button.Render(s, tc.focused)
			if plain := ansi.Strip(got); plain != tc.want {
				t.Fatalf("Render = %q, want %q", plain, tc.want)
			}
			if w := tc.button.Width(); w != len(tc.want) {
				t.Fatalf("Width = %d, want %d", w, len(tc.want))
			}
		})
	}
}

// The hotkey letter is drawn in the hotkey color, the rest in the base
// color; the focused button takes the focus colors. No filled blocks: an
// unfocused button has the dialog background.
func TestButtonColors(t *testing.T) {
	t.Parallel()
	s := dialog.Normal()
	b := dialog.Button{Label: "&OK", Default: true}
	want := s.Normal.Render("[< ") + s.Hot.Render("O") + s.Normal.Render("K >]")
	if got := b.Render(s, false); got != want {
		t.Fatalf("unfocused = %q, want %q", got, want)
	}
	want = s.Focus.Render("[< ") + s.HotFocus.Render("O") + s.Focus.Render("K >]")
	if got := b.Render(s, true); got != want {
		t.Fatalf("focused = %q, want %q", got, want)
	}
	if s.Normal.GetBackground() != lipgloss.Color("7") || s.Input.GetBackground() != lipgloss.Cyan || s.Hot.GetForeground() != lipgloss.Blue {
		t.Fatalf("skin is not mc's dialog skin: %+v", s)
	}
}

func TestRenderCheckbox(t *testing.T) {
	t.Parallel()
	s := dialog.Normal()
	if got := ansi.Strip(dialog.RenderCheckbox(s, "&Follow links", true, false)); got != "[x] Follow links" {
		t.Fatalf("checked = %q", got)
	}
	if got := ansi.Strip(dialog.RenderCheckbox(s, "&Follow links", false, true)); got != "[ ] Follow links" {
		t.Fatalf("unchecked = %q", got)
	}
}

type fixture struct {
	form  *dialog.Form
	input *textinput.Model
	on    *bool
}

func newForm(t *testing.T) fixture {
	t.Helper()
	ti := dialog.NewInput(dialog.Normal(), "/tmp")
	on := false
	f := &dialog.Form{Skin: dialog.Normal(), Items: []dialog.Item{
		dialog.Text{Text: "Copy to:"},
		dialog.Input{Model: &ti},
		dialog.Rule{},
		dialog.Check{ID: "over", Label: "Over&write", On: &on},
		dialog.Rule{},
		dialog.Buttons{
			{ID: "ok", Label: "&OK", Default: true},
			{ID: "bg", Label: "&Background"},
			{ID: "cancel", Label: "&Cancel"},
		},
	}}
	f.FocusFirst()
	return fixture{form: f, input: &ti, on: &on}
}

func TestFormView(t *testing.T) {
	t.Parallel()
	fx := newForm(t)
	content, cursor, rules := fx.form.View(40)
	want := strings.Join([]string{
		" Copy to:                               ",
		" /tmp                                   ",
		"────────────────────────────────────────",
		" [ ] Overwrite                          ",
		"────────────────────────────────────────",
		"   [< OK >] [ Background ] [ Cancel ]   ",
	}, "\n")
	if got := ansi.Strip(content); got != want {
		t.Fatalf("view:\n%s\nwant:\n%s", got, want)
	}
	for i, l := range strings.Split(content, "\n") {
		if w := ansi.StringWidth(l); w != 40 {
			t.Fatalf("line %d is %d wide", i, w)
		}
	}
	if len(rules) != 2 || rules[0] != 2 || rules[1] != 4 {
		t.Fatalf("rules = %v", rules)
	}
	if cursor == nil || cursor.Position.X != 1+len("/tmp") || cursor.Position.Y != 1 {
		t.Fatalf("cursor = %+v, want after the text on line 1", cursor)
	}
}

// An input value wider than the form scrolls inside its one line, with
// the cursor in view, so the rows under it stay where the frame joins them.
func TestFormViewLongInput(t *testing.T) {
	t.Parallel()
	long := "/tmp/" + strings.Repeat("a-very-long-directory-name/", 4) + "end"
	for _, tc := range []struct {
		name   string
		cursor func(m *textinput.Model)
		shows  string
	}{
		{name: "cursor at the end", cursor: func(m *textinput.Model) { m.CursorEnd() }, shows: "name/end"},
		{name: "cursor at the start", cursor: func(m *textinput.Model) { m.CursorStart() }, shows: "/tmp/a-very"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := dialog.NewInput(dialog.Normal(), long)
			tc.cursor(&in)
			form := dialog.Form{Skin: dialog.Normal(), Items: []dialog.Item{
				dialog.Text{Text: "to:"},
				dialog.Input{Model: &in},
				dialog.Rule{},
				dialog.Text{Text: "below"},
			}}
			form.FocusFirst()
			content, cursor, rules := form.View(40)
			lines := strings.Split(content, "\n")
			if len(lines) != form.Height() {
				t.Fatalf("%d lines, want %d:\n%s", len(lines), form.Height(), ansi.Strip(content))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != 40 {
					t.Fatalf("line %d is %d wide", i, w)
				}
			}
			if field := ansi.Strip(lines[1]); !strings.Contains(field, tc.shows) {
				t.Fatalf("input line %q does not show %q", field, tc.shows)
			}
			if len(rules) != 1 || rules[0] != 2 {
				t.Fatalf("rules = %v, want [2]", rules)
			}
			if cursor == nil || cursor.Position.Y != 1 || cursor.Position.X < 1 || cursor.Position.X > 38 {
				t.Fatalf("cursor = %+v, want inside the input line", cursor)
			}
		})
	}
}

func press(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func typed(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestFormKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		keys     []tea.KeyPressMsg
		pressed  string
		checked  bool
		value    string
		focusBtn string
	}{
		{name: "enter in the input presses the default", keys: []tea.KeyPressMsg{press(tea.KeyEnter, 0)}, pressed: "ok", value: "/tmp"},
		{name: "letters edit the input", keys: []tea.KeyPressMsg{typed('x'), typed('o')}, value: "/tmpxo"},
		{name: "M-w toggles from the input", keys: []tea.KeyPressMsg{press('w', tea.ModAlt)}, checked: true, value: "/tmp"},
		{name: "tab and space toggle", keys: []tea.KeyPressMsg{press(tea.KeyTab, 0), press(tea.KeySpace, 0)}, checked: true, value: "/tmp"},
		{name: "enter on the check box presses the default", keys: []tea.KeyPressMsg{press(tea.KeyTab, 0), press(tea.KeyEnter, 0)}, pressed: "ok", value: "/tmp"},
		{name: "plain hotkey off the input", keys: []tea.KeyPressMsg{press(tea.KeyTab, 0), typed('b')}, pressed: "bg", value: "/tmp", focusBtn: "bg"},
		{name: "right then enter presses the next button", keys: []tea.KeyPressMsg{press(tea.KeyTab, 0), press(tea.KeyTab, 0), press(tea.KeyRight, 0), press(tea.KeyEnter, 0)}, pressed: "bg", value: "/tmp", focusBtn: "bg"},
		{name: "shift-tab wraps to the last button", keys: []tea.KeyPressMsg{press(tea.KeyTab, tea.ModShift), press(tea.KeyEnter, 0)}, pressed: "cancel", value: "/tmp", focusBtn: "cancel"},
		{name: "M-c presses cancel", keys: []tea.KeyPressMsg{press('c', tea.ModAlt)}, pressed: "cancel", value: "/tmp", focusBtn: "cancel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newForm(t)
			var last dialog.Event
			for _, k := range tc.keys {
				last, _ = fx.form.Update(k)
			}
			if last.Pressed != tc.pressed {
				t.Errorf("pressed = %q, want %q", last.Pressed, tc.pressed)
			}
			if *fx.on != tc.checked {
				t.Errorf("checked = %v, want %v", *fx.on, tc.checked)
			}
			if fx.input.Value() != tc.value {
				t.Errorf("value = %q, want %q", fx.input.Value(), tc.value)
			}
			if got := fx.form.FocusedButton(); got != tc.focusBtn {
				t.Errorf("focused button = %q, want %q", got, tc.focusBtn)
			}
		})
	}
}

func TestFormAltOnly(t *testing.T) {
	t.Parallel()
	fx := newForm(t)
	fx.form.AltOnly = true
	fx.form.FocusButton("cancel")
	if ev, _ := fx.form.Update(typed('o')); ev.Pressed != "" {
		t.Fatalf("a plain letter pressed %q in an AltOnly form", ev.Pressed)
	}
	if ev, _ := fx.form.Update(press('o', tea.ModAlt)); ev.Pressed != "ok" {
		t.Fatalf("M-o pressed %q", ev.Pressed)
	}
}

func TestFormMouse(t *testing.T) {
	t.Parallel()
	fx := newForm(t)
	fx.form.View(40)
	click := func(x, y int) tea.MouseClickMsg {
		return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
	}
	release := func(x, y int) tea.MouseReleaseMsg {
		return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
	}
	if ev, _ := fx.form.Update(click(2, 3)); ev.Toggled != "over" || !*fx.on {
		t.Fatalf("a click on the check box did not toggle it: %+v", ev)
	}
	if fx.form.FocusedInput() {
		t.Fatal("the click left the focus on the input")
	}
	fx.form.Update(click(5, 1))
	if !fx.form.FocusedInput() {
		t.Fatal("a click on the input did not focus it")
	}
	// "   [< OK >] [ Background ] [ Cancel ]": Background starts at 12.
	if ev, _ := fx.form.Update(click(12, 5)); ev.Pressed != "" {
		t.Fatalf("the press already ran the button: %+v", ev)
	}
	if ev, _ := fx.form.Update(release(12, 5)); ev.Pressed != "bg" {
		t.Fatalf("release pressed %q, want bg", ev.Pressed)
	}
	if ev, _ := fx.form.Update(release(0, 5)); ev.Pressed != "" {
		t.Fatalf("a release beside the buttons pressed %q", ev.Pressed)
	}
}
