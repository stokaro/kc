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

func TestDeleteConfirmRuneShortcut(t *testing.T) {
	model := NewDeleteConfirmModel()
	model.Configure("pod default/demo", "")
	_, cmd := model.Update(pressKey('y', "y", 0))
	if cmd == nil {
		t.Fatalf("expected command for rune shortcut")
	}
	msg := cmd()
	res, ok := msg.(DeleteConfirmMsg)
	if !ok {
		t.Fatalf("expected DeleteConfirmMsg, got %T", msg)
	}
	if !res.Confirm {
		t.Fatalf("expected confirm true via shortcut")
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
