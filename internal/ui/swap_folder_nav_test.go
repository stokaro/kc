package ui

import (
	"slices"
	"testing"
)

// TestSwapPanelsKeepsFolderNavOnItsSide checks that after C-u the panel
// now on the right navigates the right navigator and keeps the focus.
func TestSwapPanelsKeepsFolderNavOnItsSide(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(true)
	a := newJumpApp(t, tr)
	a.installFolderNavHandlers()

	a.swapPanels()
	if a.activePanel != 1 {
		t.Fatalf("after swap, activePanel = %d, want 1", a.activePanel)
	}

	ctx := t.Context()
	a.rightPanel.SelectByRowID(ctx, "a")
	a.rightPanel.Enter(ctx)

	if got := a.rightNav.Steps(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("right steps = %v, want [a]", got)
	}
	if got := a.leftNav.Steps(); len(got) != 0 {
		t.Fatalf("left steps = %v, want root", got)
	}
	if a.activePanel != 1 {
		t.Fatalf("after Enter, activePanel = %d, want 1", a.activePanel)
	}

	a.rightPanel.ResetSelectionTop(ctx)
	a.rightPanel.Enter(ctx) // ".." back to the root
	if got := a.rightNav.Steps(); len(got) != 0 {
		t.Fatalf("after back, right steps = %v, want root", got)
	}
	if a.activePanel != 1 {
		t.Fatalf("after back, activePanel = %d, want 1", a.activePanel)
	}
}
