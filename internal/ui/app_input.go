package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
)

//nolint:funlen,gocognit,gocyclo,nestif // the app-wide key dispatch, one branch per key binding; splitting it is a larger refactor than the lint baseline (#128)
func (a *App) handleKeyMsg(msg tea.KeyMsg, currentCmds []tea.Cmd) (tea.Model, tea.Cmd) {
	cmds := currentCmds
	press, isPress := msg.(tea.KeyPressMsg)
	if !isPress {
		return a, nil
	}
	// The key after C-x completes or cancels the prefix, whatever it is.
	ctrlX := a.ctrlXPending
	a.ctrlXPending = false
	// The full-screen terminal (C-o) takes every key but C-o, as mc's
	// subshell does: Tab, C-u, Meta keys and Esc reach the shell as they
	// arrived, and kc has no Esc prefix there (ADR 0376).
	if a.showTerminal {
		return a, tea.Batch(append(cmds, a.fullScreenTerminalKey(press))...)
	}
	// The open menu bar takes every key until it closes.
	if a.menuBar.IsOpen() {
		return a, a.handleMenuBarKey(press)
	}
	// Interactive command focus: route all keys to the active panel widget first.
	if a.interactiveCommandActive() {
		if a.activePanel == 0 {
			model, cmd := a.leftPanel.Update(msg)
			a.leftPanel = updatedModel(a.leftPanel, model)
			cmds = append(cmds, cmd)
		} else {
			model, cmd := a.rightPanel.Update(msg)
			a.rightPanel = updatedModel(a.rightPanel, model)
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	}
	// Quick search takes every key while it runs; a key it does not use ends
	// it and is handled as usual.
	if p := a.activePanelRef(); p != nil && p.QuickSearchActive() {
		if e, ok := p.HiddenTypeMatch(); ok && press.String() == "enter" {
			// The text names a type the list does not show: open it as
			// "other types" would.
			p.StopQuickSearch()
			a.invalidateView("quick search open hidden type")
			return a, a.openHiddenType(a.activePanel, e)
		}
		handled := p.QuickSearchKey(press)
		a.invalidateView("quick search")
		if handled {
			// The search moved the cursor; report it before the next key
			// reads the selection, as a cursor key does.
			return a, p.quickSearchSelectionChanged()
		}
	}
	if ctrlX {
		return a, a.handleCtrlXKey(press)
	}
	// Prefer modifier-aware matching to cover both KeyMsg and KeyPressMsg forms.
	if key := press.Key(); key.Mod.Contains(tea.ModCtrl) {
		switch key.Code {
		case '1':
			if cmd, ok := a.cyclePanelModeIfVisible(0); ok {
				return a, cmd
			}
		case '2':
			if cmd, ok := a.cyclePanelModeIfVisible(1); ok {
				return a, cmd
			}
		}
	}
	// Typed text on the shell line takes every key but a short list, so
	// the shell's line editor gets Tab, C-u, C-w, C-r and the Meta keys.
	if cmd, ok := a.shellLineKey(press); ok {
		return a, tea.Batch(append(cmds, cmd)...)
	}
	// Hotkeys of custom commands; they never shadow the keys below.
	if cmd, ok := a.handleCommandHotkey(press); ok {
		return a, cmd
	}
	// Handle global shortcuts first
	switch press.String() {
	case "alt+f1":
		leftWidth, _ := a.panelWidths()
		if leftWidth <= 0 {
			return a, nil
		}
		return a, a.showViewOptionsModalForPanel(a.panelByIndex(0))
	case "alt+f2":
		_, rightWidth := a.panelWidths()
		if rightWidth <= 0 {
			return a, nil
		}
		return a, a.showViewOptionsModalForPanel(a.panelByIndex(1))
	case accessViewKey:
		return a, a.showAccessView()
	case overviewKey:
		if a.panelKeysFree() {
			return a, a.openOverview()
		}
	case "alt+w":
		if resizeCmds := a.cyclePanelWidth(a.activePanel); len(resizeCmds) > 0 {
			return a, tea.Batch(resizeCmds...)
		}
		return a, nil
	case "ctrl+o":
		return a, a.toggleTerminal()

	case "f9":
		// F9 opens the pull-down menu bar, as in mc. The custom commands
		// menu is on F2 (mc's user menu).
		a.openMenuBar(-1)
		return a, nil

	case "ctrl+u":
		a.swapPanels()
		return a, nil

	case "alt+l":
		return a, a.toggleLocalPanel()
	case hotlistKey:
		return a, a.showHotlist()
	case "alt+y":
		return a, a.historyBack()
	case "alt+u":
		return a, a.historyForward()
	case "alt+H", "alt+shift+h", "alt+shift+H":
		return a, a.showHistoryList()

	case "ctrl+s", "alt+s":
		if a.startQuickSearch() {
			return a, nil
		}

	case rowFilterKey:
		if a.panelKeysFree() {
			return a, a.showRowFilterDialog()
		}

	case faultsOnlyKey:
		if a.panelKeysFree() {
			return a, a.toggleFaultsOnly()
		}

	case sortNextKey:
		if a.changeObjectSort(nextSortOrder) {
			return a, nil
		}

	case sortReverseKey:
		if a.changeObjectSort(reverseSortOrder) {
			return a, nil
		}

	case "tab":
		leftWidth, rightWidth := a.panelWidths()
		if leftWidth <= 0 && rightWidth <= 0 {
			return a, nil
		}
		if leftWidth <= 0 {
			a.setActivePanel(1, "tab switch right-only")
			return a, nil
		}
		if rightWidth <= 0 {
			a.setActivePanel(0, "tab switch left-only")
			return a, nil
		}
		// Switch between panels when both are visible
		a.setActivePanel((a.activePanel+1)%2, "tab key toggle")
		return a, nil

	case "f10":
		// F10 quits; in the full-screen terminal it went to the shell above.
		return a, tea.Quit
	}

	// Handle Esc+number escape sequences (Esc then number)
	keyStr := msg.String()
	if keyStr == "esc" {
		// Esc key pressed - start escape sequence with timeout
		a.escPressed = true
		timeout := a.cfg.Input.EscTimeout.Duration
		if timeout <= 0 {
			timeout = defaultEscTimeout
		}
		return a, tea.Tick(timeout, func(time.Time) tea.Msg {
			return EscTimeoutMsg{}
		})
	} else if a.escPressed {
		// Esc n is the fallback for M-n on a node list; elsewhere n goes on
		// to the shell line as before.
		if keyStr == "n" {
			if cmd, ok := a.openNodeActions(); ok {
				a.escPressed = false
				return a, cmd
			}
		}
		// We're in an escape sequence, check for numbers
		panel := a.activePanelRef()
		caps := a.capabilitiesForPanel(panel)
		switch keyStr {
		case "0":
			a.escPressed = false
			return a, tea.Quit // Esc 0 = F10
		case "1":
			a.escPressed = false
			if caps.HasHelp {
				return a, a.showPanelHelp(panel) // Esc 1 = F1
			}
			return a, nil
		case "2":
			a.escPressed = false
			if panel != nil {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				cmd := panel.invokeActionIfAllowed(ctx, PanelActionMenu)
				cancel()
				return a, cmd
			}
			return a, nil
		case "3":
			a.escPressed = false
			if panel != nil && caps.CanView {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				cmd := panel.invokeActionIfAllowed(ctx, PanelActionView)
				cancel()
				return a, cmd
			}
			return a, nil
		case "4":
			a.escPressed = false
			if panel != nil && (caps.CanEdit || panel.environment().ReadOnly) {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				cmd := panel.invokeActionIfAllowed(ctx, PanelActionEdit)
				cancel()
				return a, cmd
			}
			return a, nil
		case "5":
			a.escPressed = false
			if panel != nil && caps.CanCopy {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				cmd := panel.invokeActionIfAllowed(ctx, PanelActionCopy)
				cancel()
				return a, cmd
			}
			return a, nil
		case "6":
			a.escPressed = false
			return a, a.renameMoveItem() // Esc 6 = F6
		case "7":
			a.escPressed = false
			if panel != nil && (caps.CanCreateNS || panel.environment().ReadOnly) {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				cmd := panel.invokeActionIfAllowed(ctx, PanelActionCreateNamespace)
				cancel()
				return a, cmd
			}
			return a, nil
		case "8":
			a.escPressed = false
			if panel != nil && (caps.CanDelete || panel.environment().ReadOnly) {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				cmd := panel.invokeActionIfAllowed(ctx, PanelActionDelete)
				cancel()
				return a, cmd
			}
			return a, nil
		case "9":
			a.escPressed = false
			a.openMenuBar(-1)
			return a, nil
		case "e":
			// Esc e is the fallback for M-e.
			a.escPressed = false
			return a, a.explainSelection()
		case "c":
			// Esc c is the meta-key fallback for M-c (macOS Option does not
			// send Alt by default).
			a.escPressed = false
			return a, a.copyPanelCommand()
		case "x":
			// Esc x is the fallback for M-x: shell in the selected container.
			a.escPressed = false
			return a, a.openContainerSession(models.ExecShell)
		case "t":
			// Esc t is the fallback for M-t: attach to the selected container.
			a.escPressed = false
			return a, a.openContainerSession(models.ExecAttach)
		case "p":
			// Esc p is the fallback for M-p (port forward).
			a.escPressed = false
			return a, a.portForwardForPanel(a.activePanelRef())
		case "s":
			// Esc s is the fallback for M-s, as in mc.
			a.escPressed = false
			a.startQuickSearch()
			return a, nil
		case "l":
			// Esc l is the fallback for M-l.
			a.escPressed = false
			return a, a.toggleLocalPanel()
		case "k":
			// Esc k is the fallback for M-k (access view).
			a.escPressed = false
			return a, a.showAccessView()
		case "j":
			// Esc j and Esc b are the fallbacks for M-j and M-b.
			a.escPressed = false
			return a, a.openRelated(models.RelatedOwners)
		case "b":
			a.escPressed = false
			return a, a.openRelated(models.RelatedUsedBy)
		case "d":
			// Esc d is the fallback for M-d (depends on).
			a.escPressed = false
			return a, a.openRelated(models.RelatedDependsOn)
		case "f":
			// Esc f is the fallback for M-f.
			a.escPressed = false
			if a.panelKeysFree() {
				return a, a.showRowFilterDialog()
			}
			return a, nil
		case "y", "u", "H":
			// Esc y, Esc u and Esc H are the fallbacks for M-y, M-u and
			// M-H, as in mc.
			a.escPressed = false
			switch keyStr {
			case "y":
				return a, a.historyBack()
			case "u":
				return a, a.historyForward()
			}
			return a, a.showHistoryList()
		case "o":
			// Esc o and Esc r are the fallbacks for M-o and M-r.
			a.escPressed = false
			if a.changeObjectSort(nextSortOrder) {
				return a, nil
			}
		case "r":
			a.escPressed = false
			if a.changeObjectSort(reverseSortOrder) {
				return a, nil
			}
		case "w":
			a.escPressed = false
			if resizeCmds := a.cyclePanelWidth(a.activePanel); len(resizeCmds) > 0 {
				return a, tea.Batch(resizeCmds...)
			}
			return a, nil
		default:
			// Not a number, cancel escape sequence
			a.escPressed = false
			if cmd, ok := a.commandHotkeyAfterEsc(press); ok {
				return a, cmd
			}
			// Continue with normal key handling
		}
	}

	if msg.String() == copyCommandKey {
		return a, a.copyPanelCommand()
	}
	if msg.String() == nodeActionKey {
		if cmd, ok := a.openNodeActions(); ok {
			return a, cmd
		}
	}
	if msg.String() == explainKey {
		return a, a.explainSelection()
	}
	// C-x starts a two-key command, as in mc. With typed text on the shell
	// line, shellLineKey gave it to the shell above.
	if msg.String() == ctrlXPrefixKey {
		a.ctrlXPending = true
		return a, nil
	}
	if msg.String() == portForwardKey {
		return a, a.portForwardForPanel(a.activePanelRef())
	}
	// C-r rereads the panel, as in mc. The shell gets it in the full-screen
	// terminal and while the shell line has typed text (history search).
	if msg.String() == rereadKey {
		return a, a.rereadPanel(a.activePanel)
	}
	switch msg.String() {
	case ownersKey:
		return a, a.openRelated(models.RelatedOwners)
	case usedByKey:
		return a, a.openRelated(models.RelatedUsedBy)
	case dependsOnKey:
		return a, a.openRelated(models.RelatedDependsOn)
	case containerShellKey:
		return a, a.openContainerSession(models.ExecShell)
	case containerAttachKey:
		return a, a.openContainerSession(models.ExecAttach)
	}
	if a.shouldRouteToPanel(msg.String()) {
		if a.activePanel == 0 {
			model, cmd := a.leftPanel.Update(msg)
			a.leftPanel = updatedModel(a.leftPanel, model)
			cmds = append(cmds, cmd)
		} else {
			model, cmd := a.rightPanel.Update(msg)
			a.rightPanel = updatedModel(a.rightPanel, model)
			cmds = append(cmds, cmd)
		}
	} else if cmd := a.updateTerminal(msg, "panel routed to terminal"); cmd != nil {
		// The first key typed at the panels starts the shell line.
		cmds = append(cmds, cmd)
	}

	return a, tea.Batch(cmds...)
}

// fullScreenTerminalKey handles a key while the terminal is full screen:
// C-o returns to the panels and every other key goes to the shell
// unchanged. Esc goes at once; the shell and the programs in it apply
// their own Esc timeouts (ADR 0376).
func (a *App) fullScreenTerminalKey(press tea.KeyPressMsg) tea.Cmd {
	if press.String() == "ctrl+o" {
		return a.toggleTerminal()
	}
	return a.updateTerminal(press, "terminal key")
}

// shellLineTyped reports whether the shell line under the panels holds
// typed text, so the shell's line editor has the keyboard.
func (a *App) shellLineTyped() bool {
	return a.terminal != nil && a.terminal.HasInput()
}

// shellLineKey gives press to the shell while the shell line holds typed
// text, and reports whether it did. kc keeps only the keys
// shellLineKeepsKey names. Esc goes to the shell at once, so kc's Esc
// prefix works only on an empty line. Enter and C-c reach the shell and
// give the keys back to the panels; Enter on a plain cd line moves the
// active panel instead (ADR 0040).
func (a *App) shellLineKey(press tea.KeyPressMsg) (tea.Cmd, bool) {
	if !a.shellLineTyped() || shellLineKeepsKey(press) {
		return nil, false
	}
	// An Esc prefix armed on the empty line before a paste ends here.
	a.escPressed = false
	switch press.String() {
	case "enter":
		if cmd, ok := a.interceptShellCd(); ok {
			return cmd, true
		}
		fallthrough
	case "ctrl+c":
		cmd := a.updateTerminal(press, "shell line ended")
		a.terminal.ClearTyped()
		return cmd, true
	}
	return a.updateTerminal(press, "shell line key"), true
}

// shellLineKeepsKey reports whether kc keeps a key while the shell line
// holds typed text: the function keys, C-o, and Up, Down, PgUp and PgDn,
// which move the panel cursor as in mc. The shell gets every other key
// (ADR 0376).
func shellLineKeepsKey(press tea.KeyPressMsg) bool {
	if k := press.Key(); k.Code >= tea.KeyF1 && k.Code <= tea.KeyF63 {
		return true
	}
	switch press.String() {
	case "ctrl+o", "up", "down", "pgup", "pgdown":
		return true
	}
	return false
}

// toggleTerminal switches between the panels and the fullscreen terminal
// (C-o, mc's "Switch panels on/off").
func (a *App) toggleTerminal() tea.Cmd {
	a.showTerminal = !a.showTerminal
	// An Esc prefix armed at the panels does not carry over.
	a.escPressed = false
	a.terminal.SetShowPanels(!a.showTerminal)
	a.invalidateView("ctrl+o toggle terminal")
	a.invalidateFunctionBar("terminal toggle")
	a.invalidateTerminalArea("terminal toggle")
	// Always keep terminal focused for typing
	a.terminal.Focus()
	if resizeCmds := a.resizePanelsForCurrentLayout(); len(resizeCmds) > 0 {
		return tea.Batch(resizeCmds...)
	}
	return nil
}

// shouldRouteToPanel reports whether a key typed at the panels, with an
// empty shell line, goes to the active panel. Other keys go to the shell
// line and start typing there.
func (a *App) shouldRouteToPanel(key string) bool {
	switch key {
	case "up", "down", "left", "right", "home", "end", "pgup", "pgdown",
		"enter", "tab", "ctrl+o",
		// Marks: Insert and C-t mark a row, C-a every row; +, - and * are
		// select group, unselect group and invert marks, as in mc.
		"ctrl+t", "insert", "ctrl+a", "+", "-", "*",
		"ctrl+w", "ctrl+s",
		"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f11", "f12":
		return true
	}
	return false
}

func (a *App) interactiveCommandActive() bool {
	if a.showTerminal {
		return false
	}
	if p := a.activePanelRef(); p != nil && p.HasCommandFocus() {
		return true
	}
	return false
}
