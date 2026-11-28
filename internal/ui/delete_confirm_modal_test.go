package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func pressKey(code rune, text string, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: text, Mod: mod}
}

func TestDeleteConfirmDefaultNo(t *testing.T) {
	model := NewDeleteConfirmModel()
	model.Configure("pod default/demo", "")
	_, cmd := model.Update(pressKey(tea.KeyEnter, "", 0))
	if cmd == nil {
		t.Fatalf("expected command on enter")
	}
	msg := cmd()
	res, ok := msg.(DeleteConfirmMsg)
	if !ok {
		t.Fatalf("expected DeleteConfirmMsg, got %T", msg)
	}
	if res.Confirm {
		t.Fatalf("expected confirmation false by default")
	}
}

func TestDeleteConfirmYesSelection(t *testing.T) {
	model := NewDeleteConfirmModel()
	model.Configure("pod default/demo", "")
	m, _ := model.Update(pressKey(tea.KeyLeft, "", 0))
	model = m.(*DeleteConfirmModel)
	_, cmd := model.Update(pressKey(tea.KeyEnter, "", 0))
	if cmd == nil {
		t.Fatalf("expected command after moving focus")
	}
	msg := cmd()
	res, ok := msg.(DeleteConfirmMsg)
	if !ok {
		t.Fatalf("expected DeleteConfirmMsg, got %T", msg)
	}
	if !res.Confirm {
		t.Fatalf("expected confirmation true when selecting yes")
	}
}

// The delete dialog is red: a plain letter never answers it, so a "y"
// meant for the shell line cannot delete (#373). Enter runs the focused
// button, No when the dialog opens; M-y and M-n pick a button by its
// highlighted letter.
func TestDeleteConfirmKeys(t *testing.T) {
	t.Parallel()
	answered := func(confirm bool) *DeleteConfirmMsg { return &DeleteConfirmMsg{Confirm: confirm, Close: true} }
	for _, tc := range []struct {
		name string
		keys []tea.KeyPressMsg
		want *DeleteConfirmMsg // nil: no answer
	}{
		{name: "y alone does nothing", keys: []tea.KeyPressMsg{pressKey('y', "y", 0)}},
		{name: "Y alone does nothing", keys: []tea.KeyPressMsg{pressKey('y', "Y", tea.ModShift)}},
		{name: "n alone does nothing", keys: []tea.KeyPressMsg{pressKey('n', "n", 0)}},
		{name: "y then Enter keeps the objects", keys: []tea.KeyPressMsg{pressKey('y', "y", 0), pressKey(tea.KeyEnter, "", 0)}, want: answered(false)},
		{name: "Enter keeps the objects", keys: []tea.KeyPressMsg{pressKey(tea.KeyEnter, "", 0)}, want: answered(false)},
		{name: "M-y deletes", keys: []tea.KeyPressMsg{pressKey('y', "", tea.ModAlt)}, want: answered(true)},
		{name: "M-n cancels", keys: []tea.KeyPressMsg{pressKey('n', "", tea.ModAlt)}, want: answered(false)},
		{name: "Left then Enter deletes", keys: []tea.KeyPressMsg{pressKey(tea.KeyLeft, "", 0), pressKey(tea.KeyEnter, "", 0)}, want: answered(true)},
		{name: "Tab then Enter deletes", keys: []tea.KeyPressMsg{pressKey(tea.KeyTab, "", 0), pressKey(tea.KeyEnter, "", 0)}, want: answered(true)},
		{name: "Tab twice then Enter keeps the objects", keys: []tea.KeyPressMsg{pressKey(tea.KeyTab, "", 0), pressKey(tea.KeyTab, "", 0), pressKey(tea.KeyEnter, "", 0)}, want: answered(false)},
		{name: "Esc cancels", keys: []tea.KeyPressMsg{pressKey(tea.KeyEscape, "", 0)}, want: answered(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := NewDeleteConfirmModel()
			model.Configure("pod default/demo", "kind-dev")
			var got *DeleteConfirmMsg
			for _, k := range tc.keys {
				_, cmd := model.Update(k)
				if cmd == nil {
					continue
				}
				res, ok := cmd().(DeleteConfirmMsg)
				if !ok {
					t.Fatalf("key %q answered with %T", k.String(), cmd())
				}
				if got != nil {
					t.Fatalf("key %q answered again after %+v", k.String(), *got)
				}
				got = &res
			}
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("answer %+v, want none", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("no answer, want %+v", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("answer %+v, want %+v", *got, *tc.want)
			}
		})
	}
}

// A marked set shows its count and kind in the question, the context, the
// first names and how many more there are.
// The key bar must not say that Enter confirms: Enter runs the focused
// button, and No has the focus when the dialog opens.
func TestDeleteConfirmHintsLeaveEnterOut(t *testing.T) {
	model := NewDeleteConfirmModel()
	model.Configure("pod default/demo", "")
	hints := model.FooterHints()
	if len(hints) == 0 {
		t.Fatal("the dialog shows no key hints")
	}
	for _, h := range hints {
		if strings.EqualFold(h.Key, "enter") {
			t.Fatalf("hint %s %q: Enter answers No when the dialog opens", h.Key, h.Label)
		}
	}
}

func TestDeleteConfirmListsTheSet(t *testing.T) {
	t.Parallel()
	model := NewDeleteConfirmModel()
	model.ConfigureSet("Delete 7 marked configmaps?", "kind-dev", []string{"shop/ux-a", "shop/ux-b", "shop/ux-c", "shop/ux-d", "shop/ux-e"}, 2)
	model.SetDimensions(60, 20)
	view := ansi.Strip(model.View().Content)
	var last int
	for _, want := range []string{"Delete 7 marked configmaps?", "Context: kind-dev", "shop/ux-a", "shop/ux-e", "and 2 more", "[ Yes ]", "[< No >]"} {
		i := strings.Index(view, want)
		if i < last {
			t.Fatalf("view has no %q after the previous line:\n%s", want, view)
		}
		last = i
	}
	// Title, context, blank, five names, "and 2 more", blank, buttons.
	if got := model.Height(60); got != 11 {
		t.Fatalf("Height = %d, want 11:\n%s", got, view)
	}
	// Configure for one object drops the list of the set before it.
	model.Configure("pod shop/web-1", "kind-dev")
	if view := ansi.Strip(model.View().Content); strings.Contains(view, "ux-a") || strings.Contains(view, "more") {
		t.Fatalf("the single delete still lists the set:\n%s", view)
	}
}

// TestDeleteConfirmHeightFitsContent checks that the dialog asks for no
// more lines than it draws (#323): title, context, a blank line and the
// buttons, plus a line per wrap of a long title. The name is wrapped,
// never cut.
func TestDeleteConfirmHeightFitsContent(t *testing.T) {
	t.Parallel()
	model := NewDeleteConfirmModel()
	model.Configure("pod shop/web-1", "kind-dev")
	if got := model.Height(60); got != 4 {
		t.Fatalf("Height with context = %d, want 4", got)
	}
	model.Configure("pod shop/web-1", "")
	if got := model.Height(60); got != 3 {
		t.Fatalf("Height without context = %d, want 3", got)
	}
	long := "pod fix-models-aae040/web-5c897445dd-nvlzz-with-a-much-longer-name"
	model.Configure(long, "kind-dev")
	if got := model.Height(40); got <= 4 {
		t.Fatalf("Height of a wrapped title = %d, want more than 4", got)
	}
	model.SetDimensions(40, 10)
	view := ansi.Strip(model.View().Content)
	if flat := strings.Join(strings.Fields(view), ""); !strings.Contains(flat, "web-5c897445dd-nvlzz-with-a-much-longer-name") {
		t.Fatalf("the wrapped title lost part of the name:\n%s", view)
	}
	for _, want := range []string{"[ Yes ]", "[< No >]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view has no %q button:\n%s", want, view)
		}
	}
}

// typedDeleteModel asks about namespace shop and waits for its name and for
// count 7.
func typedDeleteModel() *DeleteConfirmModel {
	model := NewDeleteConfirmModel()
	model.Configure("namespace shop", "kind-dev")
	model.RequireName("shop", 7, "Counting what is in it…")
	model.SetDimensions(60, 20)
	return model
}

// A dialog that asks for the name confirms only once the input holds the
// name exactly. Letters go to the input, never to a button; Enter runs
// the focused button, No until the name is typed and the focus moved; a
// change that breaks the name moves the focus back to No (#374).
func TestDeleteConfirmTypedName(t *testing.T) {
	t.Parallel()
	answered := func(confirm bool) *DeleteConfirmMsg { return &DeleteConfirmMsg{Confirm: confirm, Close: true} }
	typed := func(s string) []tea.Msg {
		var out []tea.Msg
		for _, r := range s {
			out = append(out, pressKey(r, string(r), 0))
		}
		return out
	}
	keys := func(msgs ...any) []tea.Msg {
		var out []tea.Msg
		for _, m := range msgs {
			switch m := m.(type) {
			case []tea.Msg:
				out = append(out, m...)
			case tea.Msg:
				out = append(out, m)
			}
		}
		return out
	}
	altY := pressKey('y', "", tea.ModAlt)
	enter := pressKey(tea.KeyEnter, "", 0)
	tab := pressKey(tea.KeyTab, "", 0)
	left := pressKey(tea.KeyLeft, "", 0)
	backspace := pressKey(tea.KeyBackspace, "", 0)
	for _, tc := range []struct {
		name  string
		msgs  []tea.Msg
		want  *DeleteConfirmMsg // nil: no answer
		input string
	}{
		{name: "Enter right away keeps it", msgs: keys(enter), want: answered(false)},
		{name: "M-y right away does nothing", msgs: keys(altY)},
		{name: "Tab cannot reach the disabled Yes", msgs: keys(tab, enter), want: answered(false)},
		{name: "Left cannot reach the disabled Yes", msgs: keys(left, enter), want: answered(false)},
		{name: "y goes to the input", msgs: keys(pressKey('y', "y", 0)), input: "y"},
		{name: "part of the name: M-y does nothing", msgs: keys(typed("sho"), altY), input: "sho"},
		{name: "the name and more: M-y does nothing", msgs: keys(typed("shopx"), altY), input: "shopx"},
		{name: "another case: M-y does nothing", msgs: keys(typed("SHOP"), altY), input: "SHOP"},
		{name: "a space after it: M-y does nothing", msgs: keys(typed("shop "), altY), input: "shop "},
		{name: "the name, M-y deletes", msgs: keys(typed("shop"), altY), want: answered(true), input: "shop"},
		{name: "the name, Enter keeps it: No has the focus", msgs: keys(typed("shop"), enter), want: answered(false), input: "shop"},
		{name: "the name, Tab, Enter deletes", msgs: keys(typed("shop"), tab, enter), want: answered(true), input: "shop"},
		{name: "the name, Left, Enter deletes", msgs: keys(typed("shop"), left, enter), want: answered(true), input: "shop"},
		{name: "Tab to Yes, then a backspace: Enter keeps it", msgs: keys(typed("shop"), tab, backspace, enter), want: answered(false), input: "sho"},
		{name: "a typo fixed: M-y deletes", msgs: keys(typed("shp"), backspace, typed("op"), altY), want: answered(true), input: "shop"},
		{name: "a pasted name: M-y deletes", msgs: keys(tea.PasteMsg{Content: "shop"}, altY), want: answered(true), input: "shop"},
		{name: "M-n cancels", msgs: keys(typed("shop"), pressKey('n', "", tea.ModAlt)), want: answered(false), input: "shop"},
		{name: "Esc cancels", msgs: keys(typed("shop"), pressKey(tea.KeyEscape, "", 0)), want: answered(false), input: "shop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := typedDeleteModel()
			var got *DeleteConfirmMsg
			for _, msg := range tc.msgs {
				_, cmd := model.Update(msg)
				if cmd == nil {
					continue
				}
				res, ok := cmd().(DeleteConfirmMsg)
				if !ok {
					continue // the input's cursor blink
				}
				if got != nil {
					t.Fatalf("%v answered again after %+v", msg, *got)
				}
				got = &res
			}
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("answer %+v, want none", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("no answer, want %+v", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("answer %+v, want %+v", *got, *tc.want)
			}
			if v := model.input.Value(); v != tc.input {
				t.Fatalf("input %q, want %q", v, tc.input)
			}
		})
	}
}

// A click on the disabled Yes does nothing; once the name is typed it
// deletes.
func TestDeleteConfirmTypedNameClick(t *testing.T) {
	t.Parallel()
	model := typedDeleteModel()
	model.View()
	yes := model.buttonRect[0]
	click := func() *DeleteConfirmMsg {
		model.Update(tea.MouseClickMsg{X: yes.x, Y: yes.y, Button: tea.MouseLeft})
		_, cmd := model.Update(tea.MouseReleaseMsg{X: yes.x, Y: yes.y, Button: tea.MouseLeft})
		if cmd == nil {
			return nil
		}
		res, ok := cmd().(DeleteConfirmMsg)
		if !ok {
			return nil
		}
		return &res
	}
	if got := click(); got != nil {
		t.Fatalf("a click on the disabled Yes answered %+v", *got)
	}
	model.Update(tea.PasteMsg{Content: "shop"})
	model.View()
	if got := click(); got == nil || !got.Confirm {
		t.Fatalf("a click on Yes with the name typed answered %+v", got)
	}
}

// The dialog shows "counting" until the count it waits for arrives, then
// the count; a count it does not wait for changes nothing.
func TestDeleteConfirmImpact(t *testing.T) {
	t.Parallel()
	model := typedDeleteModel()
	view := ansi.Strip(model.View().Content)
	for _, want := range []string{"Delete namespace shop?", "Context: kind-dev", "Counting what is in it…", `Type "shop" to enable Yes:`, "[ Yes ]", "[< No >]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view has no %q:\n%s", want, view)
		}
	}
	before := model.Height(60)
	if model.SetImpact(6, deleteImpact{lines: []string{"stale"}}) {
		t.Fatal("the dialog took the result of another count")
	}
	counts := deleteImpact{lines: []string{"It holds 5 deployments, 9 pods, 1 PVC and 1 Helm release.", "Counted: deployments, pods."}}
	if !model.SetImpact(7, counts) {
		t.Fatal("the dialog refused the count it waits for")
	}
	if model.SetImpact(7, deleteImpact{lines: []string{"again"}}) {
		t.Fatal("the dialog took a second result of its count")
	}
	view = ansi.Strip(model.View().Content)
	flat := strings.Join(strings.Fields(view), " ")
	if strings.Contains(view, "Counting") || !strings.Contains(flat, counts.lines[0]) || !strings.Contains(flat, counts.lines[1]) {
		t.Fatalf("view after the count:\n%s", view)
	}
	// "Counting" takes one line; the count three, as its first line wraps.
	if after := model.Height(60); after != before+2 {
		t.Fatalf("Height after the count = %d, want %d", after, before+2)
	}
	// The cursor is in the input line, one column in.
	cur := model.View().Cursor
	lines := strings.Split(view, "\n")
	if cur == nil || cur.Y >= len(lines) || !strings.Contains(lines[cur.Y-1], `Type "shop"`) || cur.X != 1 {
		t.Fatalf("cursor %+v, want the input line under the prompt:\n%s", cur, view)
	}
	// Configure for another object stops waiting: the late count is stale.
	model.Configure("pod shop/web-1", "kind-dev")
	if model.SetImpact(7, counts) {
		t.Fatal("a dialog for another object took the count")
	}
	if view := ansi.Strip(model.View().Content); strings.Contains(view, "Type") || strings.Contains(view, "Counting") {
		t.Fatalf("the pod's dialog asks for a name:\n%s", view)
	}
}
