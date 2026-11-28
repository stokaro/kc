package ui

// These tests are white-box like the rest of the package: the App's key
// routing is unexported, and the shell's input is observed at the PTY
// writer of the real Terminal and bubbleterm model.

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/taigrr/bubbleterm"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

// ptyInput records what kc writes to the shell's terminal. bubbleterm's
// response loop may write from another goroutine, hence the lock.
type ptyInput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (p *ptyInput) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.Write(b)
}

func (p *ptyInput) Close() error { return nil }

// take returns the bytes written since the last call.
func (p *ptyInput) take() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.buf.String()
	p.buf.Reset()
	return s
}

// attachShell gives app's terminal a bubbleterm model on pipes instead of
// a shell and returns what the shell would read. Terminal.Update writes
// each key at once (writeNow), so the bytes are there when Update returns.
func attachShell(t *testing.T, app *App) *ptyInput {
	t.Helper()
	outR, outW := io.Pipe()
	in := &ptyInput{}
	term, err := bubbleterm.NewWithPipes(80, 2, outR, in)
	if err != nil {
		t.Fatalf("bubbleterm: %v", err)
	}
	t.Cleanup(func() {
		_ = term.Close()
		_ = outW.Close()
	})
	app.terminal.terminal = term
	return in
}

// routingState is what a key taken by kc instead of the shell would
// change: focus, the panels' places, modes and widths, dialogs and
// pending prefixes.
type routingState struct {
	active                int
	left, right           *Panel
	leftMode, rightMode   PanelViewMode
	leftPath, rightPath   string
	leftRow               string
	leftWidth, rightWidth int
	fullScreen            bool
	modal                 string
	menuBar               bool
	quickSearch           bool
	escPrefix, ctrlX      bool
}

func routingStateOf(ctx context.Context, a *App) routingState {
	return routingState{
		active: a.activePanel,
		left:   a.leftPanel, right: a.rightPanel,
		leftMode: a.leftPanel.Mode(), rightMode: a.rightPanel.Mode(),
		leftPath: a.leftPanel.GetCurrentPath(), rightPath: a.rightPanel.GetCurrentPath(),
		leftRow:   a.leftPanel.currentSelectionID(ctx),
		leftWidth: a.leftPanelWidthPercent, rightWidth: a.rightPanelWidthPercent,
		fullScreen:  a.showTerminal,
		modal:       a.modalManager.TopName(),
		menuBar:     a.menuBar.IsOpen(),
		quickSearch: a.leftPanel.QuickSearchActive() || a.rightPanel.QuickSearchActive(),
		escPrefix:   a.escPressed, ctrlX: a.ctrlXPending,
	}
}

// pressKeys sends keys through App.Update and returns the messages their
// commands produce.
func pressKeys(a *App, keys ...tea.KeyPressMsg) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := a.Update(k)
		out = append(out, collect(cmd)...)
	}
	return out
}

// checkNoPanelAction fails when kc ran an action of its own: a container
// session, quit, or a toast.
func checkNoPanelAction(t *testing.T, msgs []tea.Msg) {
	t.Helper()
	for _, m := range msgs {
		switch m.(type) {
		case execResolvedMsg, tea.QuitMsg, showToastMsg:
			t.Errorf("kc acted on the key: %T %+v", m, m)
		}
	}
}

var (
	escKey = tea.KeyPressMsg{Code: tea.KeyEscape}
	tabKey = tea.KeyPressMsg{Code: tea.KeyTab}
)

// shellKeysApp is an App whose left panel lists a container and two plain
// rows, with the cursor on the container, so Esc x, M-x and M-t would open
// a session if kc took them.
func shellKeysApp(t *testing.T) (*App, *ptyInput) {
	t.Helper()
	app := execTestApp(t, containerRow(),
		models.NewSimpleItem("web-0", []string{"web-0"}, nil, models.WhiteStyle()),
		models.NewSimpleItem("web-1", []string{"web-1"}, nil, models.WhiteStyle()))
	t.Cleanup(app.cancel)
	return app, attachShell(t, app)
}

// In the full-screen terminal every key but C-o goes to the shell as it
// arrived (#376). kc used to switch panels on Tab, swap them on C-u, eat a
// lone Esc and run Esc digit and Esc letter actions on the hidden panels.
func TestFullScreenTerminalSendsEveryKeyToTheShell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		keys []tea.KeyPressMsg
		want string
	}{
		{"Tab", []tea.KeyPressMsg{tabKey}, "\t"},
		{"Esc", []tea.KeyPressMsg{escKey}, "\x1b"},
		{"C-u", []tea.KeyPressMsg{ctrlKey('u')}, "\x15"},
		{"Esc 4", []tea.KeyPressMsg{escKey, textKey('4')}, "\x1b4"},
		{"Esc x", []tea.KeyPressMsg{escKey, textKey('x')}, "\x1bx"},
		{"Esc :", []tea.KeyPressMsg{escKey, textKey(':')}, "\x1b:"},
		{"Esc 1", []tea.KeyPressMsg{escKey, textKey('1')}, "\x1b1"},
		{"Esc w", []tea.KeyPressMsg{escKey, textKey('w')}, "\x1bw"},
		{"Esc 4j in vi", []tea.KeyPressMsg{escKey, textKey('4'), textKey('j')}, "\x1b4j"},
		{"M-x", []tea.KeyPressMsg{altKey('x', false)}, "\x1bx"},
		{"M-l", []tea.KeyPressMsg{altKey('l', false)}, "\x1bl"},
		{"M-w", []tea.KeyPressMsg{altKey('w', false)}, "\x1bw"},
		{"C-r", []tea.KeyPressMsg{ctrlKey('r')}, "\x12"},
		{"C-2", []tea.KeyPressMsg{{Code: '2', Text: "2", Mod: tea.ModCtrl}}, "2"},
		{"F9", []tea.KeyPressMsg{{Code: tea.KeyF9}}, "\x1b[20~"},
		{"F10", []tea.KeyPressMsg{{Code: tea.KeyF10}}, "\x1b[21~"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, shell := shellKeysApp(t)
			pressKeys(app, ctrlKey('o'))
			if !app.showTerminal {
				t.Fatal("C-o did not show the full-screen terminal")
			}
			shell.take()
			before := routingStateOf(t.Context(), app)

			msgs := pressKeys(app, tc.keys...)

			if got := shell.take(); got != tc.want {
				t.Errorf("the shell read %q, want %q", got, tc.want)
			}
			if after := routingStateOf(t.Context(), app); after != before {
				t.Errorf("panel state changed:\n got %+v\nwant %+v", after, before)
			}
			checkNoPanelAction(t, msgs)
		})
	}
}

// C-o returns to the panels and is the one key the shell does not get.
func TestFullScreenTerminalKeepsCtrlO(t *testing.T) {
	t.Parallel()
	app, shell := shellKeysApp(t)
	pressKeys(app, ctrlKey('o'))
	pressKeys(app, ctrlKey('o'))
	if app.showTerminal {
		t.Fatal("the second C-o did not return to the panels")
	}
	if !app.terminal.showPanels {
		t.Fatal("the terminal still draws itself full screen")
	}
	if got := shell.take(); got != "" {
		t.Fatalf("the shell read %q from C-o", got)
	}
}

// An Esc pressed at the panels does not make the first key after C-o an
// Esc action.
func TestEscBeforeCtrlODoesNotCarryOver(t *testing.T) {
	t.Parallel()
	app, shell := shellKeysApp(t)
	for _, k := range []tea.KeyPressMsg{escKey, ctrlKey('o'), ctrlKey('o')} {
		app.Update(k) // the Esc timer is not run
	}
	msgs := pressKeys(app, textKey('x'))
	checkNoPanelAction(t, msgs)
	if got := shell.take(); got != "x" {
		t.Fatalf("the shell read %q, want x on the shell line", got)
	}
}

// With typed text on the shell line, the shell's line editor gets its keys
// (#376). kc used to switch panels on Tab, swap them on C-u, reread on C-r,
// open related objects on M-b and M-d, and attach on M-t.
func TestTypedShellLineSendsEditingKeysToTheShell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		keys []tea.KeyPressMsg
		want string
	}{
		{"Tab", []tea.KeyPressMsg{tabKey}, "\t"},
		{"C-u", []tea.KeyPressMsg{ctrlKey('u')}, "\x15"},
		{"C-w", []tea.KeyPressMsg{ctrlKey('w')}, "\x17"},
		{"C-t", []tea.KeyPressMsg{ctrlKey('t')}, "\x14"},
		{"C-r", []tea.KeyPressMsg{ctrlKey('r')}, "\x12"},
		{"C-s", []tea.KeyPressMsg{ctrlKey('s')}, "\x13"},
		{"C-x", []tea.KeyPressMsg{ctrlKey('x')}, "\x18"},
		{`C-\`, []tea.KeyPressMsg{ctrlKey('\\')}, "\x1c"},
		{"M-b", []tea.KeyPressMsg{altKey('b', false)}, "\x1bb"},
		{"M-d", []tea.KeyPressMsg{altKey('d', false)}, "\x1bd"},
		{"M-t", []tea.KeyPressMsg{altKey('t', false)}, "\x1bt"},
		{"M-f", []tea.KeyPressMsg{altKey('f', false)}, "\x1bf"},
		{"M-c", []tea.KeyPressMsg{altKey('c', false)}, "\x1bc"},
		{"M-l", []tea.KeyPressMsg{altKey('l', false)}, "\x1bl"},
		{"Esc", []tea.KeyPressMsg{escKey}, "\x1b"},
		{"Esc b", []tea.KeyPressMsg{escKey, textKey('b')}, "\x1bb"},
		{"Esc 4", []tea.KeyPressMsg{escKey, textKey('4')}, "\x1b4"},
		{"Home", []tea.KeyPressMsg{{Code: tea.KeyHome}}, "\x1b[H"},
		{"Left", []tea.KeyPressMsg{{Code: tea.KeyLeft}}, "\x1b[D"},
		{"+", []tea.KeyPressMsg{textKey('+')}, "+"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, shell := shellKeysApp(t)
			pressKeys(app, textKey('l'), textKey('s'))
			if line, _ := app.terminal.ShellLine(); line != "ls" || !app.terminal.HasInput() {
				t.Fatalf("shell line = %q, want ls", line)
			}
			shell.take()
			before := routingStateOf(t.Context(), app)

			msgs := pressKeys(app, tc.keys...)

			if got := shell.take(); got != tc.want {
				t.Errorf("the shell read %q, want %q", got, tc.want)
			}
			if after := routingStateOf(t.Context(), app); after != before {
				t.Errorf("panel state changed:\n got %+v\nwant %+v", after, before)
			}
			checkNoPanelAction(t, msgs)
		})
	}
}

// With typed text, kc keeps the function keys, C-o and the panel cursor
// keys; Enter and C-c reach the shell and give the keys back to the panels.
func TestTypedShellLineLeavesPanelKeys(t *testing.T) {
	t.Parallel()
	typed := func(t *testing.T) (*App, *ptyInput) {
		t.Helper()
		app, shell := shellKeysApp(t)
		pressKeys(app, textKey('l'), textKey('s'))
		shell.take()
		return app, shell
	}
	down, up := tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Code: tea.KeyUp}
	pgDown, pgUp := tea.KeyPressMsg{Code: tea.KeyPgDown}, tea.KeyPressMsg{Code: tea.KeyPgUp}
	for _, tc := range []struct {
		name  string
		setup []tea.KeyPressMsg
		key   tea.KeyPressMsg
	}{
		{"Down moves the cursor", nil, down},
		{"Up moves the cursor", []tea.KeyPressMsg{down}, up},
		{"PgDn moves the cursor", nil, pgDown},
		{"PgUp moves the cursor", []tea.KeyPressMsg{pgDown}, pgUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, shell := typed(t)
			pressKeys(app, tc.setup...)
			before := app.leftPanel.currentSelectionID(t.Context())
			pressKeys(app, tc.key)
			if after := app.leftPanel.currentSelectionID(t.Context()); after == before {
				t.Errorf("the cursor stayed on %q", after)
			}
			if got := shell.take(); got != "" {
				t.Errorf("the shell read %q from the cursor keys", got)
			}
		})
	}
	t.Run("M-F1 opens the view options", func(t *testing.T) {
		t.Parallel()
		app, shell := typed(t)
		pressKeys(app, tea.KeyPressMsg{Code: tea.KeyF1, Mod: tea.ModAlt})
		if app.modalManager.TopName() == "" {
			t.Error("no dialog opened")
		}
		if got := shell.take(); got != "" {
			t.Errorf("the shell read %q from M-F1", got)
		}
	})
	t.Run("F9 opens the menu bar", func(t *testing.T) {
		t.Parallel()
		app, shell := typed(t)
		pressKeys(app, tea.KeyPressMsg{Code: tea.KeyF9})
		if !app.menuBar.IsOpen() {
			t.Error("the menu bar is closed")
		}
		if got := shell.take(); got != "" {
			t.Errorf("the shell read %q from F9", got)
		}
	})
	t.Run("C-o shows the full-screen terminal", func(t *testing.T) {
		t.Parallel()
		app, shell := typed(t)
		pressKeys(app, ctrlKey('o'))
		if !app.showTerminal {
			t.Error("the terminal is not full screen")
		}
		if got := shell.take(); got != "" {
			t.Errorf("the shell read %q from C-o", got)
		}
	})
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{"Enter runs the line", tea.KeyPressMsg{Code: tea.KeyEnter}, "\r"},
		{"C-c cancels the line", ctrlKey('c'), "\x03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, shell := typed(t)
			pressKeys(app, tc.key)
			if got := shell.take(); got != tc.want {
				t.Errorf("the shell read %q, want %q", got, tc.want)
			}
			if app.terminal.HasInput() {
				t.Fatal("the shell line still holds the keyboard")
			}
			// The keys are the panels' again: Tab switches panels.
			active := app.activePanel
			pressKeys(app, tabKey)
			if app.activePanel == active {
				t.Error("Tab did not switch panels after the line ended")
			}
			if got := shell.take(); got != "" {
				t.Errorf("the shell read %q from Tab", got)
			}
		})
	}
}

// Keys that put no text on an empty shell line leave it empty, so the
// panels keep Tab; a blank line counts as empty too. Delete makes the line
// count as typed: zsh with no binding types a "~" for it.
func TestShellLineStaysEmptyAfterKeysThatAddNoText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		keys      []tea.KeyPressMsg
		read      string
		tabToPane bool
	}{
		{"C-l", []tea.KeyPressMsg{ctrlKey('l')}, "\f", true},
		{"C-e C-k", []tea.KeyPressMsg{ctrlKey('e'), ctrlKey('k')}, "\x05\x0b", true},
		{"Space", []tea.KeyPressMsg{{Code: tea.KeySpace, Text: " "}}, " ", true},
		{"Delete", []tea.KeyPressMsg{{Code: tea.KeyDelete}}, "\x1b[3~", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, shell := shellKeysApp(t)
			pressKeys(app, tc.keys...)
			if got := shell.take(); got != tc.read {
				t.Fatalf("the shell read %q, want %q", got, tc.read)
			}
			active := app.activePanel
			pressKeys(app, tabKey)
			got := shell.take()
			if tc.tabToPane && (app.activePanel == active || got != "") {
				t.Errorf("Tab did not switch panels (shell read %q)", got)
			}
			if !tc.tabToPane && (app.activePanel != active || got != "\t") {
				t.Errorf("Tab did not reach the shell (shell read %q)", got)
			}
		})
	}
}

// Text typed under the panels is still on the shell line in the
// full-screen terminal: C-u or C-c there empties it, and back at the
// panels Tab switches panels again. After Enter in the full-screen
// terminal the keys may go to another program, so they do not count as
// typed text.
func TestFullScreenTerminalEditsTheTypedShellLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		input     []tea.Msg
		line      string
		known     bool
		tabToPane bool
	}{
		{"C-u", []tea.Msg{ctrlKey('u')}, "", true, true},
		{"C-c", []tea.Msg{ctrlKey('c')}, "", true, true},
		{"Enter, then q to a program", []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnter}, textKey('q')}, "", false, true},
		{"Enter, text, C-c", []tea.Msg{tea.KeyPressMsg{Code: tea.KeyEnter}, textKey('q'), ctrlKey('c')}, "", true, true},
		{"more text", []tea.Msg{textKey('x')}, "lsx", true, false},
		{"a paste", []tea.Msg{tea.PasteMsg{Content: " -l"}}, "ls -l", true, false},
		{"Tab completion", []tea.Msg{tabKey}, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, shell := shellKeysApp(t)
			pressKeys(app, textKey('l'), textKey('s'), ctrlKey('o'))
			for _, m := range tc.input {
				app.Update(m)
			}
			pressKeys(app, ctrlKey('o'))
			if app.showTerminal {
				t.Fatal("C-o did not return to the panels")
			}
			if line, known := app.terminal.ShellLine(); line != tc.line || known != tc.known {
				t.Errorf("shell line = %q, %v; want %q, %v", line, known, tc.line, tc.known)
			}
			shell.take()
			active := app.activePanel
			pressKeys(app, tabKey)
			got := shell.take()
			if tc.tabToPane && (app.activePanel == active || got != "") {
				t.Errorf("Tab did not switch panels (shell read %q)", got)
			}
			if !tc.tabToPane && (app.activePanel != active || got != "\t") {
				t.Errorf("Tab did not reach the shell (shell read %q)", got)
			}
		})
	}
}

// An Esc pressed on an empty shell line and followed by a paste ends when
// the shell line takes the next key, so a key after the line is erased is
// text, not an Esc action.
func TestShellLineKeyEndsEscPrefix(t *testing.T) {
	t.Parallel()
	app, shell := shellKeysApp(t)
	app.Update(escKey) // the Esc timer is not run
	app.Update(tea.PasteMsg{Content: "ls"})
	msgs := pressKeys(app, textKey('x'), ctrlKey('u'), textKey('4'))
	checkNoPanelAction(t, msgs)
	if got := shell.take(); got != "lsx\x154" {
		t.Errorf("the shell read %q, want %q", got, "lsx\x154")
	}
	if app.escPressed || app.modalManager.TopName() != "" {
		t.Errorf("kc took a key: Esc prefix %v, dialog %q", app.escPressed, app.modalManager.TopName())
	}
}

// The typed shell line comes before the custom command hotkeys: M-1 bound
// to a command goes to the shell while the line holds text.
func TestTypedShellLineComesBeforeCommandHotkeys(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t, appconfig.CommandConfig{
		Name: "Drain", Command: "echo drain", Type: appconfig.CommandTypeGlobal,
		Hotkey: "M-1", Confirm: appconfig.CommandConfirm{Enabled: true},
	})
	shell := attachShell(t, app)
	pressKeys(app, textKey('l'), textKey('s'), altKey('1', false))
	if got := shell.take(); got != "ls\x1b1" {
		t.Errorf("the shell read %q, want %q", got, "ls\x1b1")
	}
	if name := app.modalManager.TopName(); name != "" {
		t.Errorf("the hotkey opened %q", name)
	}
	// With an empty line the hotkey runs.
	pressKeys(app, ctrlKey('c'), altKey('1', false))
	if name := app.modalManager.TopName(); name != commandConfirmModal {
		t.Errorf("the hotkey on an empty line opened %q, want the confirmation", name)
	}
}
