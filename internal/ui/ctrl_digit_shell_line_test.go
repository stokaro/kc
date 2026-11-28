package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// newPipedTerminalApp returns an App whose 2-line terminal runs on pipes
// instead of a shell, so keys that reach it change the tracked shell line.
func newPipedTerminalApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.width, app.height = 80, 24
	attachShell(t, app)
	return app
}

// TestCtrlDigitStaysOffTheShellLine presses C-2 through every panel mode:
// the key cycles the mode and never reaches the shell line.
func TestCtrlDigitStaysOffTheShellLine(t *testing.T) {
	t.Parallel()
	app := newPipedTerminalApp(t)
	want := []PanelViewMode{PanelModeDescribe, PanelModeManifest, PanelModeLogs, PanelModeInfo}
	for _, mode := range want {
		app.Update(tea.KeyPressMsg{Code: '2', Text: "2", Mod: tea.ModCtrl})
		if got := app.rightPanel.Mode(); got != mode {
			t.Fatalf("right panel mode = %v, want %v", got, mode)
		}
		if line, ok := app.terminal.ShellLine(); !ok || line != "" {
			t.Fatalf("in %v, shell line = %q (known %v), want empty", mode, line, ok)
		}
		if !app.panelKeysFree() {
			t.Fatalf("in %v, panel keys are blocked", mode)
		}
	}
}

// TestErasedShellLineFreesPanelKeys types on the shell line and erases it:
// panel keys such as M-i work again without Enter or C-c.
func TestErasedShellLineFreesPanelKeys(t *testing.T) {
	t.Parallel()
	app := newPipedTerminalApp(t)
	app.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if line, _ := app.terminal.ShellLine(); line != "k" {
		t.Fatalf("shell line = %q, want k", line)
	}
	if app.panelKeysFree() {
		t.Fatal("panel keys must wait while the shell line has text")
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if line, ok := app.terminal.ShellLine(); !ok || line != "" {
		t.Fatalf("after Backspace, shell line = %q (known %v), want empty", line, ok)
	}
	if !app.panelKeysFree() {
		t.Fatal("panel keys are blocked on an empty shell line")
	}
}
