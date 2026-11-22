package ui

// White-box tests: the hotkey dispatch, the prompt dialog and the red
// confirmation are wired into App's key handling and modal stack, which
// have no exported surface.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/pkg/appconfig"
)

func TestHotkeyOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  tea.Key
		want string
	}{
		{name: "alt digit", key: tea.Key{Code: '1', Mod: tea.ModAlt}, want: "M-1"},
		{name: "alt shift letter", key: tea.Key{Code: 'h', Mod: tea.ModAlt | tea.ModShift}, want: "M-H"},
		{name: "alt upper letter", key: tea.Key{Code: 'H', Mod: tea.ModAlt}, want: "M-H"},
		{name: "alt shifted code", key: tea.Key{Code: '1', ShiftedCode: '!', Mod: tea.ModAlt | tea.ModShift}, want: "M-!"},
		{name: "ctrl x", key: tea.Key{Code: 'x', Mod: tea.ModCtrl}, want: "C-x"},
		{name: "ctrl alt", key: tea.Key{Code: 'x', Mod: tea.ModCtrl | tea.ModAlt}, want: "C-M-x"},
		{name: "plain", key: tea.Key{Code: 'a', Text: "a"}, want: "a"},
		{name: "f11", key: tea.Key{Code: tea.KeyF11}, want: "F11"},
		{name: "shift f11", key: tea.Key{Code: tea.KeyF11, Mod: tea.ModShift}, want: ""},
		{name: "enter", key: tea.Key{Code: tea.KeyEnter}, want: ""},
		{name: "space", key: tea.Key{Code: ' ', Mod: tea.ModAlt}, want: ""},
		{name: "ctrl digit", key: tea.Key{Code: '1', Mod: tea.ModCtrl}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hotkeyOf(tea.KeyPressMsg(tc.key)); got != tc.want {
				t.Fatalf("hotkeyOf(%+v) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
	// Every key ParseHotkey accepts must be one hotkeyOf can produce.
	for _, k := range []tea.Key{{Code: '1', Mod: tea.ModAlt}, {Code: 'x', Mod: tea.ModCtrl}, {Code: tea.KeyF12}} {
		got := hotkeyOf(tea.KeyPressMsg(k))
		if norm, err := appconfig.ParseHotkey(got); err == nil && norm != got {
			t.Errorf("ParseHotkey(%q) = %q, notations differ", got, norm)
		}
	}
}

func newHotkeyApp(t *testing.T, cmds ...appconfig.CommandConfig) *App {
	t.Helper()
	app := NewApp()
	app.ctx = t.Context()
	app.width, app.height = 120, 40
	app.cfg.Commands = cmds
	if model, _ := app.leftPanel.Update(tea.WindowSizeMsg{Width: 60, Height: 30}); model != nil {
		app.leftPanel = model.(*Panel)
	}
	app.loadCommandHotkeys()
	return app
}

func pressHotkeyApp(t *testing.T, app *App, k tea.Key) tea.Cmd {
	t.Helper()
	if app.modalManager.IsModalVisible() {
		_, cmd := app.Update(tea.KeyPressMsg(k))
		return cmd
	}
	_, cmd := app.handleKeyMsg(tea.KeyPressMsg(k), nil)
	return cmd
}

func modalText(t *testing.T, app *App, name string) string {
	t.Helper()
	m := app.modalManager.GetActiveModal()
	if m == nil || app.modalManager.modals[name] != m {
		t.Fatalf("modal %q is not on top", name)
	}
	return ansi.Strip(viewString(m.content.(tea.Model).(interface{ View() tea.View }).View()))
}

func TestCommandHotkeyConfirmsWithRenderedCommand(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t, appconfig.CommandConfig{
		Name: "Drain", Command: "echo drain-all-nodes", Type: appconfig.CommandTypeGlobal,
		Hotkey: "M-1", Confirm: appconfig.CommandConfirm{Enabled: true, Message: "Drain everything?"},
	})
	pressHotkeyApp(t, app, tea.Key{Code: '1', Mod: tea.ModAlt})
	view := modalText(t, app, commandConfirmModal)
	for _, want := range []string{"Drain everything?", "echo drain-all-nodes", "[ Run ]", "[ Cancel ]"} {
		if !strings.Contains(view, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, view)
		}
	}
	if !app.modalManager.modals[commandConfirmModal].danger {
		t.Errorf("the confirmation must be the red dialog")
	}
	// Enter on the default button cancels; nothing runs.
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyEnter})
	if app.modalManager.IsModalVisible() || app.leftPanel.Mode() == PanelModeCommand {
		t.Fatalf("cancel must close the dialog without running the command")
	}
	// Tab to Run, Enter: the command starts in the panel.
	pressHotkeyApp(t, app, tea.Key{Code: '1', Mod: tea.ModAlt})
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyTab})
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyEnter})
	if app.modalManager.IsModalVisible() {
		t.Fatalf("the dialog must close after Run")
	}
	if app.leftPanel.Mode() != PanelModeCommand {
		t.Fatalf("panel mode = %v, want the command running", app.leftPanel.Mode())
	}
}

func TestCommandPromptsBindIntoConfirmationAndRun(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t, appconfig.CommandConfig{
		Name: "Scale", Command: "echo scale --replicas={{.Input.n}} --mode={{.Input.mode}}", Type: appconfig.CommandTypeGlobal,
		Hotkey: "C-x 1", Confirm: appconfig.CommandConfirm{Enabled: true},
		Prompts: []appconfig.CommandPrompt{
			{Name: "n", Label: "Replicas", Default: "3"},
			{Name: "mode", Choices: []string{"fast", "safe"}, Default: "safe"},
		},
	})
	pressHotkeyApp(t, app, tea.Key{Code: 'x', Mod: tea.ModCtrl})
	if app.modalManager.IsModalVisible() {
		t.Fatalf("C-x alone must only arm the chord")
	}
	pressHotkeyApp(t, app, tea.Key{Code: '1', Text: "1"})
	view := modalText(t, app, commandPromptModal)
	for _, want := range []string{"Replicas:", "mode:", "< safe >", "[ OK ]"} {
		if !strings.Contains(view, want) {
			t.Errorf("prompt dialog lacks %q:\n%s", want, view)
		}
	}
	pressHotkeyApp(t, app, tea.Key{Code: '5', Text: "5"}) // replicas "35"
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyTab})     // to mode
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyRight})   // safe -> fast
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyEnter})   // answer
	view = modalText(t, app, commandConfirmModal)
	if !strings.Contains(view, "echo scale --replicas=35 --mode=fast") {
		t.Fatalf("confirmation must show the command with the answers:\n%s", view)
	}
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyTab})
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyEnter})
	if app.leftPanel.Mode() != PanelModeCommand {
		t.Fatalf("the command must run after the confirmation")
	}
	if got := app.leftPanel.CommandConfig(); got == nil || got.Command != "echo scale --replicas=35 --mode=fast" || len(got.Prompts) != 0 {
		t.Fatalf("the panel must keep the bound command for restarts, got %+v", got)
	}
}

func TestCommandPromptCancel(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t, appconfig.CommandConfig{
		Name: "Ask", Command: "echo {{.Input.v}}", Type: appconfig.CommandTypeGlobal, Hotkey: "M-q",
		Prompts: []appconfig.CommandPrompt{{Name: "v"}},
	})
	// Esc q is the Meta fallback for M-q.
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyEscape})
	pressHotkeyApp(t, app, tea.Key{Code: 'q', Text: "q"})
	_ = modalText(t, app, commandPromptModal)
	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_ = cmd
	if app.modalManager.IsModalVisible() || app.leftPanel.Mode() == PanelModeCommand {
		t.Fatalf("Esc must drop the command")
	}
}

func TestCommandHotkeyDispatch(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t,
		appconfig.CommandConfig{Name: "Selector", Command: "echo {{.Name}}", Type: appconfig.CommandTypeSelector, Hotkey: "M-2", Confirm: appconfig.CommandConfirm{Enabled: true}},
		appconfig.CommandConfig{Name: "Shadow", Command: "echo s", Type: appconfig.CommandTypeGlobal, Hotkey: "M-s", Confirm: appconfig.CommandConfirm{Enabled: true}},
		appconfig.CommandConfig{Name: "Chord", Command: "echo c", Type: appconfig.CommandTypeGlobal, Hotkey: "C-x 1", Confirm: appconfig.CommandConfirm{Enabled: true}},
	)
	if _, ok := app.commandKeys.keys["M-s"]; ok {
		t.Fatalf("a hotkey that shadows a built-in key must not be bound")
	}
	// No object under the cursor: the selector command does not apply.
	if cmd := pressHotkeyApp(t, app, tea.Key{Code: '2', Mod: tea.ModAlt}); cmd == nil || app.modalManager.IsModalVisible() {
		t.Fatalf("an inapplicable hotkey must leave no dialog and report it")
	}
	// An unbound key after C-x is dropped, the chord ends.
	pressHotkeyApp(t, app, tea.Key{Code: 'x', Mod: tea.ModCtrl})
	if cmd, ok := app.handleCommandHotkey(tea.KeyPressMsg{Code: '9', Text: "9"}); !ok || cmd != nil {
		t.Fatalf("the key after C-x must be used up")
	}
	if app.commandKeys.chord {
		t.Fatalf("the chord must end after one key")
	}
	if _, ok := app.handleCommandHotkey(tea.KeyPressMsg{Code: '1', Text: "1"}); ok {
		t.Fatalf("1 without C-x must not run the chord")
	}
	// With text on the shell line, C-x stays with the shell.
	app.terminal.hasTyped = true
	if _, ok := app.handleCommandHotkey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}); ok {
		t.Fatalf("C-x must go to the shell line while it has text")
	}
}

func TestLoadCommandHotkeysReportsProblems(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t)
	app.cfg.Commands = []appconfig.CommandConfig{
		{Name: "a", Command: "x", Hotkey: "M-1"},
		{Name: "b", Command: "x", Hotkey: "M-1"},
	}
	if cmd := app.loadCommandHotkeys(); cmd == nil {
		t.Fatalf("a duplicate hotkey must be reported")
	}
	if len(app.commandKeys.keys) != 1 || app.cfg.Commands[1].Hotkey != "M-1" {
		t.Fatalf("keys %v; the configuration must stay as written", app.commandKeys.keys)
	}
}

func TestGuardRefusesInvalidCommand(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t)
	ran := false
	guard := app.guardCommand(nil, "", func(appconfig.CommandConfig) tea.Cmd { ran = true; return nil })
	cmd := guard(appconfig.CommandConfig{Name: "ns", Command: "x", Type: appconfig.CommandTypeNamespace, Confirm: appconfig.CommandConfirm{Enabled: true}})
	if ran || cmd == nil {
		t.Fatalf("a confirmed namespace command must be refused with a message, ran=%v", ran)
	}
	guard(appconfig.CommandConfig{Name: "plain", Command: "x", Type: appconfig.CommandTypeGlobal})
	if !ran {
		t.Fatalf("a command without prompts and confirmation must run at once")
	}
}

func TestCommandMenuShowsHotkeys(t *testing.T) {
	t.Parallel()
	sel := NewCommandSelectorModel([]appconfig.CommandConfig{
		{Name: "Scale", Type: appconfig.CommandTypeGlobal, Hotkey: "M-C-x", Prompts: []appconfig.CommandPrompt{{Name: "n"}}},
		{Name: "Search", Type: appconfig.CommandTypeGlobal, Hotkey: "M-s"},
	}, "right panel", "default", false, nil, nil)
	w, h := sel.PreferredSize(80, 20)
	sel.SetDimensions(w, h)
	view := ansi.Strip(viewString(sel.View()))
	if !strings.Contains(view, "Scale...") || !strings.Contains(view, "C-M-x") {
		t.Errorf("menu must mark prompts and show the hotkey:\n%s", view)
	}
	if strings.Contains(view, "M-s") {
		t.Errorf("a built-in key is not the command's hotkey:\n%s", view)
	}
}

func TestWrapCommandLine(t *testing.T) {
	t.Parallel()
	got := wrapCommandLine("kubectl scale deployment web --replicas=3 --namespace prod", 30)
	for _, l := range got {
		if len(l) > 30 {
			t.Errorf("line %q is longer than 30", l)
		}
	}
	if joined := strings.Join(strings.Fields(strings.Join(got, " ")), " "); joined != "kubectl scale deployment web --replicas=3 --namespace prod" {
		t.Errorf("wrapping lost text: %q", joined)
	}
}
