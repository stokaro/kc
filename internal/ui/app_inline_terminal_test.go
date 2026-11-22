package ui

import "testing"

func TestInlineTerminalVisibleInCommandMode(t *testing.T) {
	app := NewApp()
	app.width = 80
	app.height = 24

	ctx := t.Context()
	app.leftPanel.SetMode(ctx, PanelModeCommand)

	if h := app.inlineTerminalHeight(); h != inlineTerminalLines {
		t.Fatalf("expected inline terminal height %d in command mode, got %d", inlineTerminalLines, h)
	}

	panelHeight := app.panelsHeight()
	expectedPanelHeight := app.height - (1 + app.inlineTerminalHeight() + app.commandLineHeight())
	if panelHeight != expectedPanelHeight {
		t.Fatalf("unexpected panel height with inline terminal visible: got %d, want %d", panelHeight, expectedPanelHeight)
	}
}
