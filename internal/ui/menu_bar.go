package ui

import (
	"context"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/overlay"
	"github.com/stokaro/kc/internal/ui/menubar"
)

// Menu bar indexes, in the order mc shows them.
const (
	menuLeft = iota
	menuFile
	menuCommand
	menuOptions
	menuRight
)

// menuBarMenus builds the F9 menu bar. It is rebuilt every time the bar
// opens, so enabled states and check marks reflect the current panels.
//
// Every entry runs an action that also has a key or a mouse gesture; the
// shortcut column names that key in mc notation. To add an entry, add one
// line to the matching items function below.
func (a *App) menuBarMenus() []menubar.Menu {
	return []menubar.Menu{
		menuLeft:    {Title: "&Left", Items: a.panelMenuItems(0)},
		menuFile:    {Title: "&File", Items: a.fileMenuItems()},
		menuCommand: {Title: "&Command", Items: a.commandMenuItems()},
		menuOptions: {Title: "&Options", Items: a.optionsMenuItems()},
		menuRight:   {Title: "&Right", Items: a.panelMenuItems(1)},
	}
}

// panelMenuItems lists the Left (idx 0) or Right (idx 1) menu: what the
// panel shows and how.
func (a *App) panelMenuItems(idx int) []menubar.Item {
	panel := a.panelByIndex(idx)
	visible := a.panelVisible(idx)
	list := visible && a.panelListActive(idx)
	optionsKey := "M-F1"
	if idx == 1 {
		optionsKey = "M-F2"
	}
	return []menubar.Item{
		a.panelModeItem(idx, "&List", PanelModeList),
		a.panelModeItem(idx, "&Describe", PanelModeDescribe),
		a.panelModeItem(idx, "&Manifest", PanelModeManifest),
		a.panelModeItem(idx, "Lo&gs", PanelModeLogs),
		a.panelModeItem(idx, "&Info", PanelModeInfo),
		menubar.Separator(),
		{Label: "Panel &options...", Shortcut: optionsKey, Disabled: !visible, Run: func() tea.Cmd {
			return a.showViewOptionsModalForPanel(a.panelByIndex(idx))
		}},
		{Label: "&Columns normal/wide", Shortcut: "C-w", Disabled: !list, Run: a.panelKey(idx, ctrlKey('w'))},
		{Label: "&Reread", Shortcut: "C-r", Disabled: !list, Run: a.panelKey(idx, ctrlKey('r'))},
		{Label: "&Sort by next column", Shortcut: "M-o", Disabled: !list, Run: a.onPanel(idx, func() tea.Cmd {
			a.changeObjectSort(nextSortOrder)
			return nil
		})},
		{Label: "Re&verse sort", Shortcut: "M-r", Disabled: !list, Run: a.onPanel(idx, func() tea.Cmd {
			a.changeObjectSort(reverseSortOrder)
			return nil
		})},
		{Label: "&Filter...", Shortcut: "M-f", Disabled: !list, Run: a.onPanel(idx, a.showRowFilterDialog)},
		{Label: "Faults onl&y", Shortcut: "C-z", Disabled: !list, Run: a.onPanel(idx, a.toggleFaultsOnly)},
		menubar.Separator(),
		{Label: "&Namespace...", Disabled: !visible || a.cl == nil || panel == nil, Run: func() tea.Cmd {
			a.setActivePanel(idx, "menu namespace")
			return a.loadNamespacesForPicker(idx)
		}},
		{Label: "Con&text...", Disabled: a.kubeMgr == nil, Run: func() tea.Cmd {
			a.setActivePanel(idx, "menu context")
			return a.showContextPicker()
		}},
	}
}

// fileMenuItems lists the File menu: actions on the selected objects of the
// active panel, with the same checks as the function key bar.
func (a *App) fileMenuItems() []menubar.Item {
	idx := a.activePanel
	caps := a.capabilitiesForPanel(a.activePanelRef())
	list := a.panelListActive(idx)
	return []menubar.Item{
		{Label: "&View", Shortcut: "F3", Disabled: !caps.CanView, Run: a.panelAction(PanelActionView)},
		{Label: "&Edit", Shortcut: "F4", Disabled: !caps.CanEdit, Run: a.panelAction(PanelActionEdit)},
		{Label: "&Copy", Shortcut: "F5", Disabled: !caps.CanCopy, Run: a.panelAction(PanelActionCopy)},
		{Label: "Create &namespace", Shortcut: "F7", Disabled: !caps.CanCreateNS, Run: a.panelAction(PanelActionCreateNamespace)},
		{Label: "&Delete", Shortcut: "F8", Disabled: !caps.CanDelete, Run: a.panelAction(PanelActionDelete)},
		{Label: "Port &forward...", Shortcut: "M-p", Run: func() tea.Cmd { return a.portForwardForPanel(a.activePanelRef()) }},
		{Label: "&Owners", Shortcut: "M-j", Run: func() tea.Cmd { return a.openRelated(models.RelatedOwners) }},
		{Label: "Used &by", Shortcut: "M-b", Run: func() tea.Cmd { return a.openRelated(models.RelatedUsedBy) }},
		{Label: "Ex&plain", Shortcut: "M-e", Run: a.explainSelection},
		menubar.Separator(),
		{Label: "&Toggle mark", Shortcut: "C-t", Disabled: !list, Run: a.panelKey(idx, ctrlKey('t'))},
		{Label: "&Select group", Shortcut: "+", Disabled: !list, Run: a.panelKey(idx, textKey('+'))},
		{Label: "&Unselect group", Shortcut: "-", Disabled: !list, Run: a.panelKey(idx, textKey('-'))},
		{Label: "&Invert selection", Shortcut: "*", Disabled: !list, Run: a.panelKey(idx, textKey('*'))},
		{Label: "Select &all", Shortcut: "C-a", Disabled: !list, Run: a.panelKey(idx, ctrlKey('a'))},
		menubar.Separator(),
		{Label: "&Quick search", Shortcut: "C-s", Disabled: !list, Run: func() tea.Cmd {
			a.startQuickSearch()
			return nil
		}},
		menubar.Separator(),
		{Label: "E&xit", Shortcut: "F10", Run: func() tea.Cmd { return tea.Quit }},
	}
}

// commandMenuItems lists the Command menu: the user menu of custom
// commands and commands that act on the whole screen.
func (a *App) commandMenuItems() []menubar.Item {
	caps := a.capabilitiesForPanel(a.activePanelRef())
	return []menubar.Item{
		{Label: "&User menu", Shortcut: "F2", Disabled: !caps.HasContextMenu, Run: a.panelAction(PanelActionMenu)},
		menubar.Separator(),
		{Label: "&Swap panels", Shortcut: "C-u", Run: func() tea.Cmd {
			a.swapPanels()
			return nil
		}},
		{Label: "Switch panels &on/off", Shortcut: "C-o", Run: a.toggleTerminal},
		{Label: "Copy &command", Shortcut: "M-c", Run: a.copyPanelCommand},
		{Label: "Compare ob&jects", Shortcut: "C-x C-d", Run: a.compareSelectedObjects},
		menubar.Separator(),
		{Label: "Hot&list", Shortcut: "C-\\", Run: a.showHotlist},
		{Label: "History &back", Shortcut: "M-y", Run: a.historyBack},
		{Label: "History &forward", Shortcut: "M-u", Run: a.historyForward},
		{Label: "History l&ist", Shortcut: "M-H", Run: a.showHistoryList},
		menubar.Separator(),
		{Label: "&Help", Shortcut: "F1", Disabled: !caps.HasHelp, Run: a.panelAction(PanelActionHelp)},
	}
}

// optionsMenuItems lists the Options menu: layout and per-panel settings.
func (a *App) optionsMenuItems() []menubar.Item {
	return []menubar.Item{
		{Label: "Panel &width", Shortcut: "M-w", Run: func() tea.Cmd {
			return tea.Batch(a.cyclePanelWidth(a.activePanel)...)
		}},
		menubar.Separator(),
		{Label: "&Left panel options...", Shortcut: "M-F1", Disabled: !a.panelVisible(0), Run: func() tea.Cmd {
			return a.showViewOptionsModalForPanel(a.panelByIndex(0))
		}},
		{Label: "&Right panel options...", Shortcut: "M-F2", Disabled: !a.panelVisible(1), Run: func() tea.Cmd {
			return a.showViewOptionsModalForPanel(a.panelByIndex(1))
		}},
	}
}

// panelModeItem is a Left/Right entry that switches the panel to mode; the
// current mode is checked.
func (a *App) panelModeItem(idx int, label string, mode PanelViewMode) menubar.Item {
	panel := a.panelByIndex(idx)
	if panel == nil {
		return menubar.Item{Label: label, Disabled: true}
	}
	return menubar.Item{
		Label:    label,
		Checked:  panel.Mode() == mode,
		Disabled: !a.panelVisible(idx) || !slices.Contains(panel.AvailableModes(), mode),
		Run: func() tea.Cmd {
			p := a.panelByIndex(idx)
			ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
			defer cancel()
			return a.setPanelMode(ctx, p, mode, "menu panel mode")
		},
	}
}

// onPanel makes panel idx the active panel and runs fn, for Left and Right
// entries whose action works on the active panel.
func (a *App) onPanel(idx int, fn func() tea.Cmd) func() tea.Cmd {
	return func() tea.Cmd {
		a.setActivePanel(idx, "menu panel entry")
		return fn()
	}
}

// panelAction runs a panel action on the active panel, if it is allowed.
func (a *App) panelAction(action PanelAction) func() tea.Cmd {
	return func() tea.Cmd {
		panel := a.activePanelRef()
		if panel == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		defer cancel()
		return panel.invokeActionIfAllowed(ctx, action)
	}
}

// panelKey delivers key to panel idx, so a menu entry does exactly what its
// shortcut does in that panel.
func (a *App) panelKey(idx int, key tea.KeyPressMsg) func() tea.Cmd {
	return func() tea.Cmd {
		panel := a.panelByIndex(idx)
		if panel == nil {
			return nil
		}
		model, cmd := panel.Update(key)
		if p, ok := model.(*Panel); ok {
			if idx == 0 {
				a.leftPanel = p
			} else {
				a.rightPanel = p
			}
		}
		a.invalidateView("menu panel key")
		a.invalidateFunctionBar("menu panel key")
		return cmd
	}
}

func ctrlKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

func textKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// panelVisible reports whether panel idx has room on screen.
func (a *App) panelVisible(idx int) bool {
	left, right := a.panelWidths()
	if idx == 0 {
		return left > 0
	}
	return right > 0
}

// panelListActive reports whether panel idx shows a list that takes list
// keys (selection, reread, columns).
func (a *App) panelListActive(idx int) bool {
	panel := a.panelByIndex(idx)
	return panel != nil && panel.Mode() == PanelModeList && !panel.HasCommandFocus()
}

// openMenuBar opens the menu bar on the menu used last time, or on menu i
// when i >= 0. It does nothing while the terminal is fullscreen.
func (a *App) openMenuBar(i int) {
	if a.showTerminal {
		return
	}
	a.escPressed = false
	if i < 0 {
		a.menuBar.Open(a.menuBarMenus(), a.width)
	} else {
		a.menuBar.OpenAt(a.menuBarMenus(), a.width, i)
	}
	a.invalidateView("menu bar open")
}

// handleMenuBarKey gives a key to the open menu bar.
func (a *App) handleMenuBarKey(msg tea.KeyPressMsg) tea.Cmd {
	cmd := a.menuBar.HandleKey(msg)
	a.invalidateView("menu bar key")
	return cmd
}

// handleMenuBarMouse gives a mouse event to the open menu bar.
func (a *App) handleMenuBarMouse(msg tea.MouseMsg) tea.Cmd {
	cmd := a.menuBar.HandleMouse(msg)
	a.invalidateView("menu bar mouse")
	return cmd
}

// overlayMenuBar draws the open menu bar over the top row of screen and its
// drop-down, with a shadow, under the selected title. Like mc with the menu
// bar hidden, the bar takes the top row only while it is open.
func (a *App) overlayMenuBar(screen string) string {
	if !a.menuBar.IsOpen() {
		return screen
	}
	a.menuBar.SetWidth(a.width)
	screen = overlay.Composite(a.menuBar.RenderBar(a.width), screen, overlay.Left, overlay.Top, 0, 0)
	if !a.menuBar.Dropped() {
		return screen
	}
	x, y, w, h := a.menuBar.DropdownBounds()
	base := screen
	screen = overlay.Composite(a.menuBar.RenderDropdown(), screen, overlay.Left, overlay.Top, x, y)
	return castShadow(screen, base, x, y, w, h)
}
