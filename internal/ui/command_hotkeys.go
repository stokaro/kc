package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/pkg/appconfig"
)

// commandChordPrefix starts the two-key hotkeys of custom commands, as
// mc's C-x prefix does.
const commandChordPrefix = "C-x"

// commandHotkeys maps the hotkeys of custom commands to their index in
// cfg.Commands. C-x chords complete in handleCtrlXKey, which owns the
// prefix.
type commandHotkeys struct {
	keys map[string]int
}

// loadCommandHotkeys validates the custom commands of the loaded
// configuration and builds the hotkey table. Problems go to the log and to
// the command line; the configuration itself stays as the user wrote it.
func (a *App) loadCommandHotkeys() tea.Cmd {
	a.commandKeys = commandHotkeys{}
	if a.cfg == nil {
		return nil
	}
	keys, err := appconfig.ValidateCommands(a.cfg.Commands)
	a.commandKeys.keys = keys
	if err == nil {
		return nil
	}
	ctrllog.FromContext(a.ctx).WithName("commands").Error(err, "custom commands in the configuration have problems")
	first, _, _ := strings.Cut(err.Error(), "\n")
	if n := strings.Count(err.Error(), "\n"); n > 0 {
		first = fmt.Sprintf("%s (and %d more, see the log)", first, n)
	}
	if a.toastLogger != nil {
		return a.toastLogger.Errorf("Config: %s", first)
	}
	return a.ShowToast("Config: "+first, 15*time.Second)
}

// hotkeyOf returns the key in the notation of appconfig.ParseHotkey, or ""
// for a key no hotkey can name.
func hotkeyOf(press tea.KeyPressMsg) string {
	k := press.Key()
	if k.Code >= tea.KeyF1 && k.Code <= tea.KeyF24 {
		if k.Mod != 0 {
			return ""
		}
		return fmt.Sprintf("F%d", k.Code-tea.KeyF1+1)
	}
	ctrl := k.Mod.Contains(tea.ModCtrl)
	alt := k.Mod.Contains(tea.ModAlt)
	r := k.Code
	if k.Mod.Contains(tea.ModShift) {
		switch {
		case k.ShiftedCode != 0:
			r = k.ShiftedCode
		case unicode.IsLetter(r):
			r = unicode.ToUpper(r)
		}
	}
	if r > unicode.MaxRune || !unicode.IsPrint(r) || unicode.IsSpace(r) {
		return ""
	}
	var b strings.Builder
	if ctrl {
		if !unicode.IsLetter(r) {
			return ""
		}
		r = unicode.ToLower(r)
		b.WriteString("C-")
	}
	if alt {
		b.WriteString("M-")
	}
	b.WriteRune(r)
	return b.String()
}

// handleCommandHotkey runs the custom command bound to press. It reports
// whether it used the key. C-x chords are completed by handleCtrlXKey.
func (a *App) handleCommandHotkey(press tea.KeyPressMsg) (tea.Cmd, bool) {
	if a.showTerminal || len(a.commandKeys.keys) == 0 {
		return nil, false
	}
	key := hotkeyOf(press)
	if key == "" || key == commandChordPrefix {
		return nil, false
	}
	if idx, ok := a.commandKeys.keys[key]; ok {
		return a.runCommandHotkey(idx), true
	}
	return nil, false
}

// commandChord runs the custom command bound to C-x followed by press.
func (a *App) commandChord(press tea.KeyPressMsg) tea.Cmd {
	if a.showTerminal {
		return nil
	}
	key := hotkeyOf(press)
	if key == "" {
		return nil
	}
	if idx, ok := a.commandKeys.keys[commandChordPrefix+" "+key]; ok {
		return a.runCommandHotkey(idx)
	}
	return nil
}

// commandHotkeyAfterEsc runs the command bound to M-<key> when key follows
// Esc, the Meta fallback for terminals whose Alt does not send Meta. Esc
// <digit> stays an F-key, so M-<digit> hotkeys have no fallback.
func (a *App) commandHotkeyAfterEsc(press tea.KeyPressMsg) (tea.Cmd, bool) {
	if mod := press.Key().Mod; a.showTerminal || (mod != 0 && mod != tea.ModShift) {
		return nil, false
	}
	key := hotkeyOf(press)
	if key == "" {
		return nil, false
	}
	if idx, ok := a.commandKeys.keys["M-"+key]; ok {
		return a.runCommandHotkey(idx), true
	}
	return nil, false
}

// runCommandHotkey runs cfg.Commands[idx] as if it were picked in the
// commands menu of the active panel: the menu applies the same rules
// (showFor, the selection, the namespace) and passes the same target.
func (a *App) runCommandHotkey(idx int) tea.Cmd {
	if a.cfg == nil || idx < 0 || idx >= len(a.cfg.Commands) {
		return nil
	}
	cmd := a.cfg.Commands[idx]
	panel := a.activePanelRef()
	if panel == nil {
		return nil
	}
	// The menu leaves a dangerous command out in read-only mode; say why
	// rather than "not available here". The command runs for the rows of
	// the active panel, in their context.
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	_, conn := a.selectedRow(ctx, panel)
	cancel()
	if refuse := a.refuseCommand(conn.name(), cmd); refuse != nil {
		return refuse
	}
	ctrllog.FromContext(a.ctx).WithName("commands").V(1).Info("hotkey", "key", cmd.Hotkey, "name", cmd.Name)
	before := a.activeCommandSelector
	a.showContextMenuForPanel(panel)
	sel := a.activeCommandSelector
	if sel != nil && sel != before {
		if run, ok := sel.selectCommand(cmd); ok {
			return run
		}
		a.hideModalName(commandMenuModal)
	}
	return a.ShowToast(fmt.Sprintf("%s: not available here", cmd.Name), 3*time.Second)
}

// selectCommand picks the entry for cmd as Enter would and returns what
// the menu does for it.
func (m *CommandSelectorModel) selectCommand(cmd appconfig.CommandConfig) (tea.Cmd, bool) {
	for i, e := range m.entries {
		c := e.command
		if c == nil || c.Name != cmd.Name || c.Command != cmd.Command || c.Type != cmd.Type || c.Hotkey != cmd.Hotkey {
			continue
		}
		m.selected = i
		if m.onSelect == nil {
			return nil, true
		}
		return m.onSelect(*c), true
	}
	return nil, false
}

// commandHotkeyLabel is the hotkey of cmd as the menu shows it, or "".
func commandHotkeyLabel(cmd *appconfig.CommandConfig) string {
	if cmd == nil || cmd.Hotkey == "" {
		return ""
	}
	key, err := appconfig.ParseHotkey(cmd.Hotkey)
	if err != nil {
		return ""
	}
	if _, builtin := appconfig.BuiltinHotkey(key); builtin {
		return ""
	}
	return key
}
