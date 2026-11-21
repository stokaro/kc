package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sttts/kc/internal/overlay"
	uistyles "github.com/sttts/kc/internal/ui/styles"
)

// View renders the application
func (a *App) View() tea.View {
	if view, cursor, ok := a.mainViewCache.get(); ok {
		v := viewWithCursor(view, cursor)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}
	// In fullscreen terminal mode, only show terminal
	if a.showTerminal {
		terminalView, terminalCursor := a.renderTerminalView()
		a.mainViewCache.set(terminalView, terminalCursor)
		v := viewWithCursor(terminalView, terminalCursor)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// In normal mode, show main view
	mainView, mainCursor := a.renderMainView()

	// Overlay modal if visible
	if a.modalManager.IsModalVisible() {
		modalView := a.modalManager.View()
		modalCursor := modalView.Cursor
		modalContent := viewString(modalView)

		// If command menu is active, draw an external arrow toward the target panel namespace.
		if a.activeCommandSelector != nil && a.modalManager.GetActiveModal() != nil {
			if winX, winY, winW, _, ok := a.modalManager.GetActiveModal().WindowPosition(); ok {
				if a.activeCommandSelector.SelectedIsNamespaced() {
					row := a.activeCommandSelector.SelectedRow()
					leftArrow := "\u25c0"  // ◀
					rightArrow := "\u25b6" // ▶
					arrowHead := rightArrow
					arrowText := fmt.Sprintf("\u2500\u2500\u2500\u2500\u2500\u2500\u2500(ns: %s)\u2500\u2500\u2500\u2500\u2500\u2500\u2500%s", a.activeCommandSelector.TargetNamespace(), arrowHead)
					arrowView := lipgloss.NewStyle().
						Background(lipgloss.Blue).
						Foreground(lipgloss.Color(uistyles.ColorOrange)).
						Bold(true).
						Render(arrowText)
					// Position: to the right of the window, aligned with the selected row.
					dx := winX + winW
					if a.activeCommandSelector.TargetLeft() {
						arrowHead = leftArrow
						arrowText = fmt.Sprintf("%s\u2500\u2500\u2500\u2500\u2500\u2500\u2500(ns: %s)\u2500\u2500\u2500\u2500\u2500\u2500\u2500", arrowHead, a.activeCommandSelector.TargetNamespace())
						arrowView = lipgloss.NewStyle().
							Background(lipgloss.Blue).
							Foreground(lipgloss.Color(uistyles.ColorOrange)).
							Bold(true).
							Render(arrowText)
						dx = winX - lipgloss.Width(arrowText) - 1
					}
					dy := winY + 1 + row
					if dx < a.width && dx >= 0 {
						modalContent = overlay.Composite(arrowView, modalContent, overlay.Top, overlay.Left, dx, dy)
					}
				}
			}
		}

		a.mainViewCache.set(modalContent, modalCursor)
		v := viewWithCursor(modalContent, modalCursor)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	a.mainViewCache.set(mainView, mainCursor)
	v := viewWithCursor(mainView, mainCursor)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// renderMainView renders the main two-panel view
func (a *App) renderMainView() (string, *tea.Cursor) {
	// Calculate dimensions
	terminalHeight := a.inlineTerminalHeight()
	// Reserve space for the inline terminal (if visible) and function keys (1)
	commandHeight := a.commandLineHeight()
	reserved := terminalHeight + commandHeight + 1
	panelHeight := a.height - reserved
	if panelHeight < 3 {
		panelHeight = 3
	}
	leftPanelWidth, rightPanelWidth := a.panelWidthsFor(a.width)

	var (
		panelCursor *tea.Cursor
		panelOrigin int
	)

	renderPanel := func(panel *Panel, width int, focused bool) string {
		if width <= 0 {
			return ""
		}
		if cached, ok := panel.CachedFrame(width, panelHeight, focused); ok {
			return cached
		}
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		defer cancel()
		view := panel.Render(ctx, width, panelHeight, focused)
		if panel == a.activePanelRef() && panel.HasCommandFocus() {
			panelOrigin = 0
			if panel == a.rightPanel && leftPanelWidth > 0 {
				panelOrigin = leftPanelWidth
			}
			if cur := panel.widgetCursor(ctx); cur != nil {
				panelCursor = cur
			}
		}
		return view
	}

	leftPanel := ""
	if leftPanelWidth > 0 {
		leftPanel = renderPanel(a.leftPanel, leftPanelWidth, a.activePanel == 0)
	}
	rightPanel := ""
	if rightPanelWidth > 0 {
		rightPanel = renderPanel(a.rightPanel, rightPanelWidth, a.activePanel == 1)
	}

	var panels string
	switch {
	case leftPanelWidth <= 0:
		panels = rightPanel
	case rightPanelWidth <= 0:
		panels = leftPanel
	default:
		panels = lipgloss.JoinHorizontal(
			lipgloss.Top,
			leftPanel,
			rightPanel,
		)
	}

	// Add the inline shell line
	terminalView := ""
	var terminalCursor *tea.Cursor
	if terminalHeight > 0 {
		terminalView, terminalCursor = a.renderTerminalArea()
	}

	// Add function key bar
	functionKeys := a.renderFunctionKeys()
	parts := []string{panels}
	if commandHeight > 0 {
		parts = append(parts, a.renderCommandLine())
	}
	if terminalHeight > 0 {
		parts = append(parts, terminalView)
	}
	parts = append(parts, functionKeys)
	combinedView := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// Busy overlay: show a small 2x2 ASCII animation centered over the main view
	if a.busyActive {
		ov := a.renderBusyOverlay()
		if ov != "" {
			combinedView = overlay.Composite(ov, combinedView, overlay.Center, overlay.Center, 0, 0)
		}
	}

	// Adjust cursor position for the combined view.
	// Prefer panel cursor when focused; fall back to terminal cursor.
	if panelCursor != nil {
		offsetX := panelOrigin + 1
		offsetY := 1
		adjusted := tea.NewCursor(panelCursor.X+offsetX, panelCursor.Y+offsetY)
		adjusted.Blink = panelCursor.Blink
		adjusted.Color = panelCursor.Color
		adjusted.Shape = panelCursor.Shape
		return combinedView, adjusted
	}
	if terminalCursor != nil {
		// Calculate the offset: panels height
		offsetY := panelHeight + commandHeight
		adjustedCursor := tea.NewCursor(terminalCursor.X, terminalCursor.Y+offsetY)
		adjustedCursor.Blink = terminalCursor.Blink
		adjustedCursor.Color = terminalCursor.Color
		adjustedCursor.Shape = terminalCursor.Shape
		return combinedView, adjustedCursor
	}

	return combinedView, nil
}

// renderBusyOverlay returns a small 2x2 ASCII animation based on busyFrame.
func (a *App) renderBusyOverlay() string {
	// 2x2 ASCII frames: cross and bar alternation
	frames := []string{
		"\\/\n/\\", // star
		"||\n||",
		"/\\\n\\/",
		"--\n--",
	}
	f := frames[a.busyFrame%len(frames)]
	// Add a faint box/spacing around for visibility (optional)
	st := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.White).Background(lipgloss.Color("238")).Padding(0, 1)
	return st.Render(f)
}

// renderTerminalArea renders the inline shell area in the main view
func (a *App) renderTerminalArea() (string, *tea.Cursor) {
	if view, cursor, ok := a.terminalAreaCache.get(); ok {
		if a.interactiveCommandActive() {
			return view, nil
		}
		return view, cursor
	}
	terminalView := a.terminal.View()
	terminalContent := viewString(terminalView)
	cur := terminalView.Cursor
	if a.interactiveCommandActive() {
		cur = nil
	}
	a.terminalAreaCache.set(terminalContent, cur)
	view, cursor, _ := a.terminalAreaCache.get()
	return view, cursor
}

// renderTerminalView renders the full-screen terminal view
func (a *App) renderTerminalView() (string, *tea.Cursor) {
	// Get terminal view
	tv := a.terminal.View()
	terminalView := viewString(tv)
	terminalCursor := tv.Cursor
	if a.interactiveCommandActive() {
		terminalCursor = nil
	}

	// Compose with a one-line toggle message at the bottom. To ensure it's visible,
	// clamp the terminal content to a.height-1 lines.
	toggleMsg := a.renderToggleMessage()
	lines := strings.Split(terminalView, "\n")
	maxTerm := a.height - 1
	if maxTerm < 1 {
		maxTerm = 1
	}
	if len(lines) > maxTerm {
		lines = lines[:maxTerm]
	} else if len(lines) < maxTerm {
		// pad with empty lines to keep layout stable
		pad := make([]string, maxTerm-len(lines))
		lines = append(lines, pad...)
	}
	clamped := strings.Join(lines, "\n")
	combinedView := lipgloss.JoinVertical(lipgloss.Left, clamped, toggleMsg)

	// Adjust cursor position so it never overlaps the toggle message
	if terminalCursor != nil {
		cy := terminalCursor.Y
		if cy >= maxTerm {
			cy = maxTerm - 1
		}
		if cy < 0 {
			cy = 0
		}
		adjusted := tea.NewCursor(terminalCursor.X, cy)
		adjusted.Blink = terminalCursor.Blink
		adjusted.Color = terminalCursor.Color
		adjusted.Shape = terminalCursor.Shape
		return combinedView, adjusted
	}
	return combinedView, nil
}

// refreshFoldersAfterViewChange reapplies the current folders to panels so that
// folder population re-reads the latest panel config.
func (a *App) refreshFoldersAfterViewChange() {
	if a.leftNav != nil {
		cur := a.leftNav.Current()
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		a.setPanelFolder(ctx, 0, cur, a.leftNav.HasBack())
		a.leftPanel.SetCurrentPath(a.navigatorPath(a.leftNav))
		a.leftPanel.RefreshFolder(ctx)
		cancel()
	}
	if a.rightNav != nil {
		cur := a.rightNav.Current()
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		a.setPanelFolder(ctx, 1, cur, a.rightNav.HasBack())
		a.rightPanel.SetCurrentPath(a.navigatorPath(a.rightNav))
		a.rightPanel.RefreshFolder(ctx)
		cancel()
	}
}

// renderFunctionKeys renders the function key bar
func (a *App) renderFunctionKeys() string {
	if a.functionBar == nil {
		return ""
	}
	return a.functionBar.Render(a.functionBarState())
}

// functionBarState captures what the function bar shows. Rendering and click
// handling share it so the slots they compute always match.
func (a *App) functionBarState() functionBarState {
	state := functionBarState{
		Width:        a.width,
		ToastActive:  a.toastActive,
		ToastText:    a.toastText,
		ToastInfo:    a.toastInfo,
		ShowTerminal: a.showTerminal,
		ActivePanel:  a.activePanel,
	}
	if panel := a.activePanelRef(); panel != nil {
		state.PanelHasCommandFocus = panel.HasCommandFocus()
		state.PanelInteractive = panel.HasInteractiveCommand()
		state.Capabilities = a.capabilitiesForPanel(panel)
		state.PanelMode = panel.Mode()
	}
	if a.terminal != nil {
		state.TerminalHasInput = a.terminal.HasInput()
	}
	return state
}

// handleFunctionKeyClick maps an x coordinate on the function key bar to a key action.
func (a *App) handleFunctionKeyClick(x int) tea.Cmd {
	if a.toastActive {
		return nil
	}
	if !a.showTerminal {
		if p := a.activePanelRef(); p != nil && p.HasCommandFocus() {
			return nil
		}
	}
	if a.showTerminal {
		if functionKeyAt(len(compactFunctionKeys(functionBarState{ShowTerminal: true})), a.width, x) < 0 {
			return nil
		}
		a.showTerminal = false
		a.terminal.SetShowPanels(true)
		a.invalidateView("function bar return to panels")
		a.invalidateFunctionBar("terminal exit")
		a.invalidateTerminalArea("terminal exit")
		return nil
	}

	state := a.functionBarState()
	keys := panelFunctionKeys(state)
	i := functionKeyAt(len(keys), state.Width, x)
	if i < 0 || !keys[i].Enabled {
		return nil
	}
	panel := a.activePanelRef()
	if panel == nil {
		return nil
	}
	invoke := func(action PanelAction) tea.Cmd {
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		defer cancel()
		return panel.invokeActionIfAllowed(ctx, action)
	}
	switch keys[i].ID {
	case functionKeyHelp:
		return invoke(PanelActionHelp)
	case functionKeyOptions:
		return invoke(PanelActionOptions)
	case functionKeyView:
		return invoke(PanelActionView)
	case functionKeyEdit:
		return invoke(PanelActionEdit)
	case functionKeyCopy:
		return invoke(PanelActionCopy)
	case functionKeyRenameMove:
		return a.renameMoveItem()
	case functionKeyNamespace:
		return invoke(PanelActionCreateNamespace)
	case functionKeyDelete:
		return invoke(PanelActionDelete)
	case functionKeyCommands:
		return invoke(PanelActionMenu)
	case functionKeyQuit:
		return tea.Quit
	case functionKeyFocus:
		if a.terminal == nil || a.terminal.HasInput() {
			return nil
		}
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		defer cancel()
		return panel.FocusCommand(ctx)
	}
	return nil
}

// renderToggleMessage renders the key bar shown under the fullscreen terminal.
func (a *App) renderToggleMessage() string {
	return renderFunctionKeySlots(compactFunctionKeys(functionBarState{ShowTerminal: true}), a.width)
}
