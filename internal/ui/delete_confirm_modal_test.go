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
