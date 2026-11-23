package ui

import (
	"context"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/overlay"
	"github.com/stokaro/kc/internal/ui/menubar"
	"github.com/stokaro/kc/pkg/appconfig"
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
	menus := menuBarTitles()
	menus[menuLeft].Items = a.panelMenuItems(0)
	menus[menuFile].Items = a.fileMenuItems()
	menus[menuCommand].Items = a.commandMenuItems()
	menus[menuOptions].Items = a.optionsMenuItems()
	menus[menuRight].Items = a.panelMenuItems(1)
	return menus
}

// menuBarTitles returns the menus of the bar without entries: what the
// visible bar draws, and where its titles are, while no menu is open.
func menuBarTitles() []menubar.Menu {
	return []menubar.Menu{
		menuLeft:    {Title: "&Left"},
		menuFile:    {Title: "&File"},
		menuCommand: {Title: "&Command"},
		menuOptions: {Title: "&Options"},
		menuRight:   {Title: "&Right"},
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
		a.localFilesItem(idx),
		menubar.Separator(),
		a.pinMenuItem(idx),
		a.freezeMenuItem(idx),
		{Label: "Panel &options...", Shortcut: optionsKey, Disabled: !visible, Run: func() tea.Cmd {
			return a.showViewOptionsModalForPanel(a.panelByIndex(idx))
		}},
		{Label: "&Columns normal/wide", Shortcut: "C-w", Disabled: !list, Run: a.panelKey(idx, ctrlKey('w'))},
		{Label: "&Reread", Shortcut: "C-r", Disabled: !visible || panel == nil, Run: a.onPanel(idx, func() tea.Cmd { return a.rereadPanel(idx) })},
		{Label: "&Sort by next column", Shortcut: "M-o", Disabled: !list, Run: a.onPanel(idx, func() tea.Cmd {
			a.changeObjectSort(nextSortOrder)
			return nil
		})},
		{Label: "Re&verse sort", Shortcut: "M-r", Disabled: !list, Run: a.onPanel(idx, func() tea.Cmd {
			a.changeObjectSort(reverseSortOrder)
			return nil
		})},
		a.groupNamespacesItem(idx, list),
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
	view := "&View"
	if p := a.activePanelRef(); p != nil && p.Mode() == PanelModeLogs {
		// F3 in a Logs panel shows its session full screen.
		view = "&Expand logs"
	}
	return []menubar.Item{
		{Label: view, Shortcut: "F3", Disabled: !caps.CanView, Run: a.panelAction(PanelActionView)},
		{Label: "&Edit", Shortcut: "F4", Disabled: !caps.CanEdit, Run: a.panelAction(PanelActionEdit)},
		{Label: "&Copy", Shortcut: "F5", Disabled: !caps.CanCopy, Run: a.panelAction(PanelActionCopy)},
		{Label: "Create &namespace", Shortcut: "F7", Disabled: !caps.CanCreateNS, Run: a.panelAction(PanelActionCreateNamespace)},
		{Label: "&Delete", Shortcut: "F8", Disabled: !caps.CanDelete, Run: a.panelAction(PanelActionDelete)},
		{Label: "Port &forward...", Shortcut: "M-p", Run: func() tea.Cmd { return a.portForwardForPanel(a.activePanelRef()) }},
		{Label: "&Owners", Shortcut: "M-j", Run: func() tea.Cmd { return a.openRelated(models.RelatedOwners) }},
		{Label: "Used &by", Shortcut: "M-b", Run: func() tea.Cmd { return a.openRelated(models.RelatedUsedBy) }},
		{Label: "&Requires", Shortcut: "M-d", Run: func() tea.Cmd { return a.openRelated(models.RelatedDependsOn) }},
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
		{Label: "Compare lis&ts", Shortcut: "C-x d", Run: a.compareLists},
		{Label: "Compar&e whole lists", Shortcut: "C-x a", Run: a.compareWholeLists},
		{Label: "Compare with conte&xt...", Shortcut: "C-x c", Run: a.compareListWithContext},
		{Label: "Network&Policy between pods", Shortcut: "C-x n", Run: a.explainNetworkPolicy},
		{Label: "Compare whole list with co&ntext...", Run: a.compareWholeListWithContext},
		a.windowMenuItem(),
		a.metricSourcesMenuItem(),
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
		menubar.Separator(),
		{Label: "&Menu bar visible", Checked: a.menuBarVisible(), Run: a.toggleMenuBarVisible},
	}
}

// panelModeItem is a Left/Right entry that switches the panel to mode; the
// current mode is checked.
func (a *App) panelModeItem(idx int, label string, mode PanelViewMode) menubar.Item {
	panel := a.panelByIndex(idx)
	if panel == nil {
		return menubar.Item{Label: label, Disabled: true}
	}
	checked := panel.Mode() == mode
	if mode == PanelModeList && a.panelShowsLocal(idx) {
		// The local files entry carries the mark while the list shows
		// the local filesystem.
		checked = false
	}
	return menubar.Item{
		Label:    label,
		Checked:  checked,
		Disabled: !a.panelVisible(idx) || !slices.Contains(panel.AvailableModes(), mode),
		Run: func() tea.Cmd {
			p := a.panelByIndex(idx)
			ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
			defer cancel()
			return a.setPanelMode(ctx, p, mode, "menu panel mode")
		},
	}
}

// localFilesItem is the Left/Right entry that shows the local filesystem in
// the panel, next to the panel modes, as mc lists its panel kinds there. It
// switches the panel to List mode first; chosen again, it returns the panel
// to the cluster (M-l).
func (a *App) localFilesItem(idx int) menubar.Item {
	panel := a.panelByIndex(idx)
	if panel == nil {
		return menubar.Item{Label: "Local fil&es", Shortcut: "M-l", Disabled: true}
	}
	return menubar.Item{
		Label:    "Local fil&es",
		Shortcut: "M-l",
		Checked:  a.panelShowsLocal(idx),
		Disabled: !a.panelVisible(idx) || !slices.Contains(panel.AvailableModes(), PanelModeList),
		Run: func() tea.Cmd {
			a.setActivePanel(idx, "menu local files")
			p := a.panelByIndex(idx)
			var cmd tea.Cmd
			if p.Mode() != PanelModeList {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				cmd = a.setPanelMode(ctx, p, PanelModeList, "menu local files")
				cancel()
			}
			return tea.Batch(cmd, a.toggleLocalPanel())
		},
	}
}

// panelShowsLocal reports whether panel idx lists the local filesystem.
func (a *App) panelShowsLocal(idx int) bool {
	panel := a.panelByIndex(idx)
	if panel == nil {
		return false
	}
	nav := a.navigatorForPanel(panel)
	return nav != nil && isLocalFolder(nav)
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

// menuBarVisible reports whether layout.menuBarVisible keeps the bar on the
// top row.
func (a *App) menuBarVisible() bool {
	return a.cfg != nil && a.cfg.Layout.MenuBarVisible
}

// menuBarRows is the number of screen rows above the panels: 1 while the
// visible menu bar is drawn there, 0 when the setting is off or the
// terminal is fullscreen (C-o), which covers the whole screen as in mc.
func (a *App) menuBarRows() int {
	if a.showTerminal || !a.menuBarVisible() {
		return 0
	}
	return 1
}

// renderIdleMenuBar draws the visible menu bar while no menu is open.
func (a *App) renderIdleMenuBar() string {
	return menubar.RenderTitles(menuBarTitles(), a.width)
}

// handleIdleMenuBarMouse opens the menu whose title a left button press on
// the visible bar hit. Every event on the bar row is consumed, as mc does;
// the release of the click goes to the open bar, which ignores it.
func (a *App) handleIdleMenuBarMouse(msg tea.MouseMsg) tea.Cmd {
	m := msg.Mouse()
	if _, press := msg.(tea.MouseClickMsg); !press || m.Button != tea.MouseLeft {
		return nil
	}
	if i := menubar.TitleIndex(menuBarTitles(), m.X); i >= 0 {
		a.openMenuBar(i)
	}
	return nil
}

// toggleMenuBarVisible switches layout.menuBarVisible, gives the top row to
// the panels or takes it back, and saves the setting, as mc saves its
// setup when the layout changes.
func (a *App) toggleMenuBarVisible() tea.Cmd {
	visible := !a.menuBarVisible()
	if a.cfg != nil {
		a.cfg.Layout.MenuBarVisible = visible
	}
	cmds := a.resizePanelsForCurrentLayout()
	a.invalidateView("menu bar visible")
	cmds = append(cmds, a.saveDefaults(func(c *appconfig.Config) {
		c.Layout.MenuBarVisible = visible
	}))
	return tea.Batch(cmds...)
}

// overlayMenuBar draws the open menu bar over the top row of screen and its
// drop-down, with a shadow, under the selected title. With the visible bar
// it covers the idle bar; with layout.menuBarVisible off it covers the top
// panel row while it is open, as mc does with the menu bar hidden.
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
