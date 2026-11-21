package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A key handled by an open dialog must drop the cached screen; otherwise the
// change shows only with the next unrelated message.
func TestModalKeyInvalidatesView(t *testing.T) {
	choices := namespaceChoices([]string{"a", "b"}, "a")
	picker := NewScopePickerModel(choices, "a", func(scopeChoice) tea.Cmd { return nil }, nil)
	picker.SetDimensions(20, 5)
	mm := NewModalManager(nil)
	mm.Register("picker", NewModal("Namespace", picker))
	mm.Show("picker")

	app := &App{ctx: t.Context(), modalManager: mm}
	app.mainViewCache.set("stale", nil)
	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if _, _, ok := app.mainViewCache.get(); ok {
		t.Fatalf("the cached screen survived a key handled by the dialog")
	}
	if picker.selected != 2 {
		t.Fatalf("selected = %d, want 2", picker.selected)
	}
}
