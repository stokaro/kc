package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/models"
)

func (a *App) handleKeyMsg(msg tea.KeyMsg, currentCmds []tea.Cmd) (tea.Model, tea.Cmd) {
	cmds := currentCmds
	press, isPress := msg.(tea.KeyPressMsg)
	if !isPress {
		return a, nil
	}
	// The key after C-x completes or cancels the prefix, whatever it is.
	ctrlX := a.ctrlXPending
	a.ctrlXPending = false
	// The open menu bar takes every key until it closes.
	if a.menuBar.IsOpen() {
		return a, a.handleMenuBarKey(press)
	}
	// Interactive command focus: route all keys to the active panel widget first.
	if a.interactiveCommandActive() {
		if a.activePanel == 0 {
			model, cmd := a.leftPanel.Update(msg)
			a.leftPanel = model.(*Panel)
			cmds = append(cmds, cmd)
		} else {
			model, cmd := a.rightPanel.Update(msg)
			a.rightPanel = model.(*Panel)
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	}
	// Quick search takes every key while it runs; a key it does not use ends
	// it and is handled as usual.
	if p := a.activePanelRef(); p != nil && !a.showTerminal && p.QuickSearchActive() {
		handled := p.QuickSearchKey(press)
		a.invalidateView("quick search")
		if handled {
			return a, nil
		}
	}
	if ctrlX {
		return a, a.handleCtrlXKey(press)
	}
	// Prefer modifier-aware matching to cover both KeyMsg and KeyPressMsg forms.
	if key := press.Key(); key.Mod.Contains(tea.ModCtrl) {
		switch key.Code {
		case '1':
			if cmd := a.cyclePanelModeIfVisible(0); cmd != nil {
				return a, cmd
			}
		case '2':
			if cmd := a.cyclePanelModeIfVisible(1); cmd != nil {
				return a, cmd
			}
		}
	}
	// Hotkeys of custom commands; they never shadow the keys below.
	if cmd, ok := a.handleCommandHotkey(press); ok {
		return a, cmd
	}
	// Handle global shortcuts first
	switch press.String() {
	case "alt+f1":
		leftWidth, _, _, _ := a.panelAreaMetrics()
		if leftWidth <= 0 {
			return a, nil
		}
		return a, a.showViewOptionsModalForPanel(a.panelByIndex(0))
	case "alt+f2":
		_, rightWidth, _, _ := a.panelAreaMetrics()
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
		if !a.showTerminal {
			a.openMenuBar(-1)
			return a, nil
		}

	case "ctrl+u":
		a.swapPanels()
		return a, nil

	case "alt+l":
		return a, a.toggleLocalPanel()
	case hotlistKey:
		if !a.showTerminal {
			return a, a.showHotlist()
		}
	case "alt+y":
		if !a.showTerminal {
			return a, a.historyBack()
		}
	case "alt+u":
		if !a.showTerminal {
			return a, a.historyForward()
		}
	case "alt+H", "alt+shift+h", "alt+shift+H":
		if !a.showTerminal {
			return a, a.showHistoryList()
		}

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
		if a.interactiveCommandActive() {
			break
		}
		leftWidth, rightWidth, _, _ := a.panelAreaMetrics()
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
		// F10 only quits kc when not in fullscreen mode
		// In fullscreen mode, F10 should go to terminal (for shell commands)
		if !a.showTerminal && !a.interactiveCommandActive() {
			return a, tea.Quit
		}
		// In fullscreen mode, don't handle F10 here - let it go to terminal
	}

	// Handle Esc+number escape sequences (Esc then number)
	keyStr := msg.String()
	if keyStr == "esc" && !a.interactiveCommandActive() {
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
		if keyStr == "n" && !a.showTerminal {
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
			// Esc 0 = F10, only quit when not in fullscreen mode
			if !a.showTerminal {
				return a, tea.Quit
			}
			// In fullscreen mode, let Esc+0 go to terminal
		case "1":
			a.escPressed = false
			if caps.HasHelp {
				return a, a.showHelp() // Esc 1 = F1
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
			if panel != nil && (caps.CanEdit || a.readOnly()) {
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
			if panel != nil && (caps.CanCreateNS || a.readOnly()) {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				cmd := panel.invokeActionIfAllowed(ctx, PanelActionCreateNamespace)
				cancel()
				return a, cmd
			}
			return a, nil
		case "8":
			a.escPressed = false
			if panel != nil && (caps.CanDelete || a.readOnly()) {
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
			if !a.showTerminal {
				return a, a.openRelated(models.RelatedOwners)
			}
		case "b":
			a.escPressed = false
			if !a.showTerminal {
				return a, a.openRelated(models.RelatedUsedBy)
			}
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
			if a.showTerminal {
				break
			}
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

	// In terminal mode, all input goes to terminal except C-o to return
	if a.showTerminal {
		// Only handle C-o to return to panel mode
		if msg.String() == "ctrl+o" {
			a.showTerminal = false
			a.invalidateView("ctrl+o exit terminal")
			a.invalidateFunctionBar("terminal exit")
			a.invalidateTerminalArea("terminal exit")
			if resizeCmds := a.resizePanelsForCurrentLayout(); len(resizeCmds) > 0 {
				return a, tea.Batch(resizeCmds...)
			}
			return a, nil
		}
		// Everything else goes to the terminal
		if cmd := a.updateTerminal(msg, "terminal key"); cmd != nil {
			cmds = append(cmds, cmd)
		}
	} else {
		// In panel mode, use smart key routing based on terminal state
		// If user typed in the 2-line terminal, Enter and C-c must be SENT to the terminal,
		// then reset typed state to return focus to the panels.
		if (msg.String() == "enter" || msg.String() == "ctrl+c") && a.terminal != nil && a.terminal.HasInput() && !a.interactiveCommandActive() {
			if msg.String() == "enter" {
				if cmd, ok := a.interceptShellCd(); ok {
					return a, cmd
				}
			}
			cmd := a.updateTerminal(msg, "panel forwarded to terminal")
			a.terminal.ClearTyped() // reset typed; next keys route to panels
			return a, cmd
		}
		if a.interactiveCommandActive() {
			// Route all keys to the active panel/command widget when it holds focus.
			if a.activePanel == 0 {
				model, cmd := a.leftPanel.Update(msg)
				a.leftPanel = model.(*Panel)
				cmds = append(cmds, cmd)
			} else {
				model, cmd := a.rightPanel.Update(msg)
				a.rightPanel = model.(*Panel)
				cmds = append(cmds, cmd)
			}
			return a, tea.Batch(cmds...)
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
		// C-x starts a two-key command, as in mc. While the shell line has
		// typed text, C-x stays with the shell.
		if msg.String() == ctrlXPrefixKey && (a.terminal == nil || !a.terminal.HasInput()) {
			a.ctrlXPending = true
			return a, nil
		}
		if msg.String() == portForwardKey {
			return a, a.portForwardForPanel(a.activePanelRef())
		}
		switch msg.String() {
		case ownersKey:
			return a, a.openRelated(models.RelatedOwners)
		case usedByKey:
			return a, a.openRelated(models.RelatedUsedBy)
		case containerShellKey:
			return a, a.openContainerSession(models.ExecShell)
		case containerAttachKey:
			return a, a.openContainerSession(models.ExecAttach)
		}
		if a.shouldRouteToPanel(msg.String()) {
			// Handle panel-specific keys
			if a.activePanel == 0 {
				model, cmd := a.leftPanel.Update(msg)
				a.leftPanel = model.(*Panel)
				cmds = append(cmds, cmd)
			} else {
				model, cmd := a.rightPanel.Update(msg)
				a.rightPanel = model.(*Panel)
				cmds = append(cmds, cmd)
			}
		} else {
			// Route to terminal
			if cmd := a.updateTerminal(msg, "panel routed to terminal"); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	return a, tea.Batch(cmds...)
}

// shouldRouteToPanel determines if a key should be routed to the panel based on terminal state
// toggleTerminal switches between the panels and the fullscreen terminal
// (C-o, mc's "Switch panels on/off").
func (a *App) toggleTerminal() tea.Cmd {
	a.showTerminal = !a.showTerminal
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

func (a *App) shouldRouteToPanel(key string) bool {
	// Always route these keys to terminal
	terminalKeys := []string{
		" ",     // space bar (Bubble Tea reports as literal space)
		"space", // fallback just in case
	}

	for _, termKey := range terminalKeys {
		if key == termKey {
			return false
		}
	}

	// If the user has typed into the terminal buffer while panels are visible,
	// allow some navigation keys to keep flowing to the terminal so shell
	// editing shortcuts remain usable.
	if a.terminal != nil && a.terminal.HasInput() {
		switch key {
		case "tab", "ctrl+a", "ctrl+e":
			return false
		}
	}

	// Always route these keys to panels (others handled below)
	panelKeys := []string{
		// Navigation keys
		"up", "down", // Navigate items (left/right handled conditionally below)
		"home", "end", // Navigate to beginning/end
		"pgup", "pgdown", // Page up/down
		// Panel control keys
		"tab",    // Switch panels
		"ctrl+o", // Toggle fullscreen
		// Selection keys
		"ctrl+t", "insert", // Toggle selection
		"*",      // Invert selection
		"ctrl+a", // Select all
		"ctrl+w",
		// Function keys (F10 handled separately for fullscreen vs panel mode)
		"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f11", "f12",
		// Other panel actions
		"ctrl+r", // Refresh
		"ctrl+s", // Search
		"esc",    // Cancel
	}

	for _, panelKey := range panelKeys {
		if key == panelKey {
			return true
		}
	}

	// Special handling for Enter key
	if key == "enter" {
		// Check if terminal has non-empty input
		if a.terminal != nil && a.terminal.HasInput() {
			return false // Route Enter to terminal if user is typing
		}
		return true // Route Enter to panels if terminal is empty
	}

	// Special handling for Left/Right: route to panels only when terminal input is empty
	if key == "left" || key == "right" {
		if a.terminal != nil && a.terminal.HasInput() {
			return false // typing → keep in terminal
		}
		return true
	}

	// Special handling for + and - keys (glob patterns)
	if key == "+" || key == "-" {
		// Only route to panels if terminal is empty
		if a.terminal != nil && a.terminal.HasInput() {
			return false // Route to terminal if user is typing
		}
		return true // Route to panels if terminal is empty
	}

	// Special handling for F10 key
	if key == "f10" {
		// In fullscreen mode, F10 goes to terminal (for shell commands)
		// In panel mode, F10 quits kc (handled in main switch statement)
		return false // Always route to terminal
	}

	// Default: route to terminal for typing
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

func (a *App) hasInteractiveCommandOnPanel() bool {
	if a.showTerminal {
		return false
	}
	if p := a.activePanelRef(); p != nil && p.HasInteractiveCommand() {
		return true
	}
	return false
}
