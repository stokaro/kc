package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	"github.com/stokaro/kc/internal/ui/viewer"
	"github.com/stokaro/kc/internal/workloadaction"
	"github.com/stokaro/kc/pkg/appconfig"
)

// enqueueCmd appends a command to be executed on the next Update cycle.
func (a *App) enqueueCmd(cmd tea.Cmd) {
	a.pendingCmdsMu.Lock()
	defer a.pendingCmdsMu.Unlock()
	if cmd != nil {
		a.pendingCmds = append(a.pendingCmds, cmd)
	}
}

func (a *App) updateTerminal(msg tea.Msg, reason string) tea.Cmd {
	if a.terminal == nil {
		return nil
	}
	model, cmd := a.terminal.Update(msg)
	a.terminal = updatedModel(a.terminal, model)
	a.invalidateView(reason)
	a.invalidateTerminalArea(reason)
	return cmd
}

// Update handles messages and updates the application state
func (a *App) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	a.syncActivity()
	defer a.syncActivity()
	// Start the access reviews the selected row needs for function key gating.
	defer func() { cmd = batchAccessPrefetch(cmd, a.accessPrefetchCmd()) }()
	if a.cfg != nil {
		a.logMsg(msg)
	}

	var cmds []tea.Cmd
	a.pendingCmdsMu.Lock()
	if len(a.pendingCmds) > 0 {
		cmds = append(cmds, a.pendingCmds...)
		a.pendingCmds = nil
	}
	a.pendingCmdsMu.Unlock()
	viewWasValid := a.mainViewCache.valid

	// An open node shell owns the keyboard and the screen.
	if a.nodeShell != nil {
		cmd, handled := a.updateNodeShell(msg)
		cmds = append(cmds, cmd)
		if handled {
			return a, tea.Batch(cmds...)
		}
	}

	// Always adapt size
	switch msg := msg.(type) {
	case panelWalkMsg:
		if cmd := a.continuePanelNavigation(msg.panel, msg.gen); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	case startupLogsTargetMsg:
		if cmd := a.startStartupLogs(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	case startupIntentMsg:
		if cmd := a.applyStartupIntent(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	case panelNamespaceChangedMsg:
		ctrllog.FromContext(a.ctx).WithName("namespaceCommand").V(1).Info("ns-follow: namespace message received",
			"leader", msg.PanelIdx,
			"namespace", msg.Namespace,
		)
		for idx := range 2 {
			if cmd := a.maybeRestartNamespaceCommand(idx, msg.Namespace); cmd != nil {
				cmds = append(cmds, cmd)
			}
			ctrllog.FromContext(a.ctx).WithName("namespaceCommand").V(1).Info("ns-follow: namespace message processed",
				"leader", msg.PanelIdx,
				"follower", idx,
				"namespace", msg.Namespace,
			)
		}
		return a, tea.Batch(cmds...)
	case restartNamespaceCommandMsg:
		panel := a.panelByIndex(msg.PanelIdx)
		if panel != nil {
			if cfg := panel.CommandConfig(); cfg != nil && cfg.Type == appconfig.CommandTypeNamespace {
				ctrllog.FromContext(a.ctx).WithName("namespaceCommand").V(1).Info("ns-follow: restart tick",
					"panel", msg.PanelIdx,
					"namespace", msg.Namespace,
					"cfg", cfg.Name,
				)
				if cmd := a.startNamespaceCommand(msg.PanelIdx, *cfg, msg.Namespace); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return a, tea.Batch(cmds...)
	case commandWatchTickMsg:
		model, cmd := a.handleCommandWatchTick(msg)
		return model, cmd
	case workloadLogsReadyMsg:
		return a, tea.Batch(append(cmds, a.showWorkloadLogs(msg))...)
	case logStreamOpenedMsg, logStreamMsg:
		// Stream results go to their viewer even when a dialog (F2) covers
		// it; routed through the top modal they would be dropped and the
		// stream would stop.
		var v *LogsViewer
		switch m := msg.(type) {
		case logStreamOpenedMsg:
			v = m.viewer
		case logStreamMsg:
			v = m.viewer
		}
		if v != nil {
			_, cmd := v.Update(msg)
			cmds = append(cmds, cmd)
			a.invalidateView("logs stream")
		}
		return a, tea.Batch(cmds...)
	case tea.WindowSizeMsg:
		msg.Width = max(40, msg.Width)
		msg.Height = max(5, msg.Height)

		sizeChanged := msg.Width != a.width || msg.Height != a.height
		if sizeChanged {
			a.width = msg.Width
			a.height = msg.Height
			a.invalidateView("window resize")
			a.invalidateFunctionBar("window resize")
			a.invalidateTerminalArea("window resize")

			// Ensure active modal scales with terminal size
			if a.modalManager != nil && a.modalManager.IsModalVisible() {
				if m := a.modalManager.GetActiveModal(); m != nil {
					m.SetDimensions(a.width, a.height)
					if vm, ok := m.content.(*ViewOptionsModel); ok {
						a.layoutViewOptionsModal(m, vm, vm.PanelIndex(), "")
					}
				}
			}
		}

		if a.terminal != nil {
			// Reserve space for status bar (1 line)
			// Terminal gets the remaining space
			terminalMsg := tea.WindowSizeMsg{
				Width:  msg.Width,
				Height: msg.Height - 1,
			}
			if cmd := a.updateTerminal(terminalMsg, "window resize"); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

		if sizeChanged {
			if resizeCmds := a.resizePanelsForCurrentLayout(); len(resizeCmds) > 0 {
				cmds = append(cmds, resizeCmds...)
			}
			return a, tea.Batch(cmds...)
		}

		if !sizeChanged && len(cmds) == 0 {
			return a, nil
		}
	}

	// Handle modals first
	if a.modalManager.IsModalVisible() {
		switch {
		case passesModal(msg):
			// Fall through to the main switch.
		default:
			// Intercept modal-scoped commits while the dialog is open.
			switch m := msg.(type) {
			case ViewOptionsCommittedMsg:
				var subCmds []tea.Cmd
				if m.Viewer != nil {
					if m.PanelIndex < 0 {
						cmd := a.applyViewerOptions(m)
						return a, cmd
					}
					// The Logs panel dialog: the viewer and log options
					// first, then the Panel section below. A mode change
					// there stops the restarted stream again.
					restart, save := a.applyViewerSettings(m)
					a.viewerOptionsTarget = nil
					subCmds = append(subCmds, restart)
					if save != nil {
						subCmds = append(subCmds, a.saveDefaults(save))
					}
				}
				targetIdx := m.PanelIndex
				if targetIdx < 0 || targetIdx > 1 {
					targetIdx = a.activePanel
				}
				closedBySubMsg := false
				if m.SetPanelMode && m.Accept {
					if _, cmd := a.Update(PanelModeSelectedMsg{PanelIndex: targetIdx, Mode: m.PanelMode}); cmd != nil {
						subCmds = append(subCmds, cmd)
					}
				}
				panel := a.panelByIndex(targetIdx)
				currentTableMode := "scroll"
				if panel != nil {
					currentTableMode = panel.TableMode()
				}
				if m.SetPanelWidth && (m.Accept || m.SaveDefault) {
					targetPercent := clampPercent(m.PanelWidthPercent)
					subCmds = append(subCmds, a.setPanelWidthPercent(targetIdx, targetPercent)...)
					if m.SaveDefault {
						left, right := a.leftPanelWidthPercent, a.rightPanelWidthPercent
						a.cfg.Panel.Width.LeftPercent = left
						a.cfg.Panel.Width.RightPercent = right
						subCmds = append(subCmds, a.saveDefaults(func(c *appconfig.Config) {
							c.Panel.Width.LeftPercent = left
							c.Panel.Width.RightPercent = right
						}))
					}
				}
				if m.Resources != nil && (m.Accept || m.SaveDefault) {
					tableMode := currentTableMode
					if m.HasTableMode {
						tableMode = m.TableMode
					}
					resMsg := ResourcesOptionsChangedMsg{
						ShowNonEmptyOnly: !m.Resources.IncludeEmpty,
						Order:            m.Resources.Order,
						TableMode:        tableMode,
						HasInclude:       m.Resources.HasInclude,
						HasOrder:         m.Resources.HasOrder,
						SaveDefault:      m.SaveDefault,
						Accept:           m.Accept,
						Close:            m.Close,
					}
					if _, cmd := a.Update(resMsg); cmd != nil {
						subCmds = append(subCmds, cmd)
					}
					if m.Close {
						closedBySubMsg = true
					}
				}
				if m.Objects != nil && m.Accept && m.Objects.HasFaultsOnly {
					// Faults only is a per-folder view state; Save as
					// defaults does not store it.
					a.setPanelFaultsOnly(a.panelByIndex(m.PanelIndex), m.Objects.FaultsOnly)
				}
				if m.Objects != nil && (m.Accept || m.SaveDefault) {
					tableMode := currentTableMode
					if m.HasTableMode {
						tableMode = m.TableMode
					}
					objMsg := ObjectOptionsChangedMsg{
						TableMode:    tableMode,
						Columns:      m.Objects.Columns,
						ObjectsOrder: m.Objects.Order,
						SaveDefault:  m.SaveDefault,
						Accept:       m.Accept,
						Close:        m.Close,
					}
					if _, cmd := a.Update(objMsg); cmd != nil {
						subCmds = append(subCmds, cmd)
					}
					if m.Close {
						closedBySubMsg = true
					}
				}
				if m.Command != nil && (m.Accept || m.SaveDefault) {
					cmdMsg := CommandOptionsChangedMsg{
						WatchInterval: m.Command.WatchInterval,
						SaveDefault:   m.SaveDefault,
						Accept:        m.Accept,
						Close:         m.Close,
					}
					if _, cmd := a.Update(cmdMsg); cmd != nil {
						subCmds = append(subCmds, cmd)
					}
					if m.Close {
						closedBySubMsg = true
					}
				}
				if m.Accept && !m.SaveDefault {
					subCmds = append(subCmds, a.sessionOnly())
				}
				if m.Close && !closedBySubMsg {
					a.hideModal()
				}
				return a, tea.Batch(subCmds...)
			case ResourcesOptionsChangedMsg:
				var saveCmd tea.Cmd
				if m.SaveDefault {
					// Persist current dialog values to config defaults
					change := func(c *appconfig.Config) {
						if m.HasInclude {
							c.Resources.ShowNonEmptyOnly = m.ShowNonEmptyOnly
						}
						if m.HasOrder {
							c.Resources.Order = appconfig.ResourcesViewOrder(m.Order)
						}
						c.Panel.Table.Mode = appconfig.TableMode(m.TableMode)
					}
					change(a.cfg)
					saveCmd = a.saveDefaults(change)
					a.applyResourceOptions(a.leftPanel)
					a.applyResourceOptions(a.rightPanel)
				}
				if m.Accept {
					// Apply to active panel only; do not persist
					if a.activePanel == 0 {
						ctxTable, cancelTable := context.WithTimeout(a.ctx, panelContextTimeout)
						a.leftPanel.SetTableMode(ctxTable, m.TableMode)
						cancelTable()
						if m.HasInclude || m.HasOrder {
							a.leftPanel.SetResourceViewOptions(m.ShowNonEmptyOnly, m.Order)
						}
						a.syncPanelConfig(a.leftPanel)
						a.applyResourceOptions(a.leftPanel)
					} else {
						ctxTable, cancelTable := context.WithTimeout(a.ctx, panelContextTimeout)
						a.rightPanel.SetTableMode(ctxTable, m.TableMode)
						cancelTable()
						if m.HasInclude || m.HasOrder {
							a.rightPanel.SetResourceViewOptions(m.ShowNonEmptyOnly, m.Order)
						}
						a.syncPanelConfig(a.rightPanel)
						a.applyResourceOptions(a.rightPanel)
					}
					// Refresh only the active panel's folder
					if a.activePanel == 0 && a.leftNav != nil {
						ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
						if rf, ok := a.leftNav.Current().(interface{ Refresh() }); ok {
							rf.Refresh()
						}
						a.setPanelFolder(ctx, 0, a.leftNav.Current(), a.leftNav.HasBack())
						a.leftPanel.SetCurrentPath(a.navigatorPath(a.leftNav))
						cancel()
						ctxRefresh, cancelRefresh := context.WithTimeout(a.ctx, panelContextTimeout)
						a.leftPanel.RefreshFolder(ctxRefresh)
						cancelRefresh()
					}
					if a.activePanel == 1 && a.rightNav != nil {
						ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
						if rf, ok := a.rightNav.Current().(interface{ Refresh() }); ok {
							rf.Refresh()
						}
						a.setPanelFolder(ctx, 1, a.rightNav.Current(), a.rightNav.HasBack())
						a.rightPanel.SetCurrentPath(a.navigatorPath(a.rightNav))
						cancel()
						ctxRefresh, cancelRefresh := context.WithTimeout(a.ctx, panelContextTimeout)
						a.rightPanel.RefreshFolder(ctxRefresh)
						cancelRefresh()
					}
				}
				if m.Close {
					a.hideModal()
				}
				return a, saveCmd
			case ObjectOptionsChangedMsg:
				var saveCmd tea.Cmd
				if m.SaveDefault {
					change := func(c *appconfig.Config) {
						switch strings.ToLower(m.TableMode) {
						case "fit":
							c.Panel.Table.Mode = appconfig.TableModeFit
						default:
							c.Panel.Table.Mode = appconfig.TableModeScroll
						}
						if strings.EqualFold(m.Columns, appconfig.ColumnsModeWide) {
							c.Objects.Columns = appconfig.ColumnsModeWide
						} else {
							c.Objects.Columns = appconfig.ColumnsModeNormal
						}
						c.Objects.Order = m.ObjectsOrder
					}
					change(a.cfg)
					saveCmd = a.saveDefaults(change)
				}
				if a.activePanel == 0 {
					ctxPanel, cancelPanel := context.WithTimeout(a.ctx, panelContextTimeout)
					a.leftPanel.SetTableMode(ctxPanel, m.TableMode)
					a.leftPanel.SetColumnsMode(ctxPanel, m.Columns)
					a.leftPanel.SetObjectOrder(ctxPanel, m.ObjectsOrder)
					a.syncPanelConfig(a.leftPanel)
					cancelPanel()
					if a.leftNav != nil {
						if rf, ok := a.leftNav.Current().(interface{ Refresh() }); ok {
							rf.Refresh()
						}
						ctxRefresh, cancelRefresh := context.WithTimeout(a.ctx, panelContextTimeout)
						a.leftPanel.RefreshFolder(ctxRefresh)
						cancelRefresh()
					}
				} else {
					ctxPanel, cancelPanel := context.WithTimeout(a.ctx, panelContextTimeout)
					a.rightPanel.SetTableMode(ctxPanel, m.TableMode)
					a.rightPanel.SetColumnsMode(ctxPanel, m.Columns)
					a.rightPanel.SetObjectOrder(ctxPanel, m.ObjectsOrder)
					a.syncPanelConfig(a.rightPanel)
					cancelPanel()
					if a.rightNav != nil {
						if rf, ok := a.rightNav.Current().(interface{ Refresh() }); ok {
							rf.Refresh()
						}
						ctxRefresh, cancelRefresh := context.WithTimeout(a.ctx, panelContextTimeout)
						a.rightPanel.RefreshFolder(ctxRefresh)
						cancelRefresh()
					}
				}
				if m.Close {
					a.hideModal()
				}
				return a, saveCmd
			case CommandOptionsChangedMsg:
				panel := a.activePanelRef()
				panelIdx := a.activePanel
				if panel != nil {
					ctxPanel, cancelPanel := context.WithTimeout(a.ctx, panelContextTimeout)
					if cmd := panel.SetCommandWatchInterval(ctxPanel, m.WatchInterval); cmd != nil {
						cmds = append(cmds, cmd)
					}
					cancelPanel()
				}
				if panelIdx >= 0 && panelIdx <= 1 {
					if cmd := a.setCommandWatchInterval(panelIdx, m.WatchInterval); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
				if m.Close {
					a.hideModal()
				}
				return a, tea.Batch(cmds...)
			}
			model, cmd := a.modalManager.Update(msg)
			a.modalManager = updatedModel(a.modalManager, model)
			cmds = append(cmds, cmd)
			// The dialog may have changed (a key moved the selection); the
			// cached screen must not outlive that, or the change shows only
			// with the next unrelated message.
			a.invalidateView("modal update")
			// While a modal is open, still forward non-key messages to the
			// terminal (process output, window size). Background is snapshotted,
			// so this stays light and keeps the 2-line terminal fresh.
			if _, isKey := msg.(tea.KeyMsg); !isKey && a.terminal != nil {
				if tcmd := a.updateTerminal(msg, "modal forward"); tcmd != nil {
					cmds = append(cmds, tcmd)
				}
			}
			return a, tea.Batch(cmds...)
		}
	}

	// Check if terminal process has exited (check on every message)
	if a.terminal.IsProcessExited() {
		return a, tea.Quit
	}

	switch msg := msg.(type) {
	case BusyShowMsg:
		if msg.token == a.busyToken {
			a.busyActive = true
			a.busyFrame = 0
			a.invalidateView("busy show")
			return a, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return BusyTickMsg{} })
		}
		return a, nil
	case BusyTickMsg:
		if a.busyActive {
			a.busyFrame = (a.busyFrame + 1) % 10
			a.invalidateView("busy tick")
			return a, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return BusyTickMsg{} })
		}
		return a, nil
	case BusyHideMsg:
		if msg.token == a.busyToken {
			a.busyActive = false
			a.invalidateView("busy hide")
		}
		return a, nil
	case busyDoneMsg:
		if msg.token == a.busyToken {
			a.busyActive = false
			a.busyToken++
			a.invalidateView("busy done")
		}
		// Re-dispatch the original message for normal handling
		return a, func() tea.Msg { return msg.msg }
	case explainLoadedMsg:
		return a, a.handleExplainLoaded(msg)
	case configSavedMsg:
		return a, a.handleConfigSaved(msg)
	case showToastMsg:
		a.toastActive = true
		a.toastText = msg.text
		a.toastInfo = msg.info
		a.toastUntil = time.Now().Add(msg.ttl)
		a.invalidateView("toast show")
		a.invalidateFunctionBar("toast show")
		var cmds []tea.Cmd
		if resizeCmds := a.resizePanelsForCurrentLayout(); len(resizeCmds) > 0 {
			cmds = append(cmds, resizeCmds...)
		}
		cmds = append(cmds, tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return toastTickMsg{} }))
		return a, tea.Batch(cmds...)
	case toastTickMsg:
		if a.toastActive {
			if !time.Now().After(a.toastUntil) {
				return a, tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return toastTickMsg{} })
			}
			a.toastActive = false
			a.invalidateView("toast hide")
			a.invalidateFunctionBar("toast hide")
			if resizeCmds := a.resizePanelsForCurrentLayout(); len(resizeCmds) > 0 {
				return a, tea.Batch(resizeCmds...)
			}
		}
		return a, nil
	case kubectlEditFinishedMsg:
		if msg.err != nil {
			if a.toastLogger != nil {
				a.enqueueCmd(a.toastLogger.Errorf("kubectl edit %s failed: %v", msg.resourceRef, msg.err))
			} else {
				a.enqueueCmd(a.ShowToast(fmt.Sprintf("kubectl edit failed: %v", msg.err), 5*time.Second))
			}
			return a, nil
		}
		a.refreshPanelAfterEdit(msg.panelIndex)
		return a, nil
	case RowFilterResultMsg:
		if msg.Close {
			a.hideModal()
		}
		if msg.Confirm {
			a.applyRowFilterText(msg.Panel, msg.Text)
		}
		return a, nil
	case NamespaceCreateResultMsg:
		if msg.Close {
			a.hideModal()
			if a.namespaceInput != nil {
				a.namespaceInput.Reset()
			}
			if !msg.Confirm {
				a.namespaceCreatePanel = -1
			}
		}
		if msg.Confirm {
			if a.cl == nil {
				if a.toastLogger != nil {
					a.enqueueCmd(a.toastLogger.Errorf("Cluster not ready for namespace creation"))
				}
				return a, nil
			}
			return a, a.createNamespaceWithName(msg.Name)
		}
		return a, nil
	case CopyToLocalResultMsg:
		if msg.Close {
			a.hideModal()
			if a.copyInput != nil {
				a.copyInput.BlurInputs()
			}
		}
		if msg.Confirm {
			if path := strings.TrimSpace(msg.Path); path != "" {
				a.lastCopyDir = filepath.Dir(path)
				return a, a.executeCopyRequest(path)
			}
			a.pendingCopy = nil
			return a, nil
		}
		a.pendingCopy = nil
		return a, nil
	case namespaceCreatedMsg:
		if msg.err != nil {
			if a.toastLogger != nil {
				a.enqueueCmd(a.toastLogger.Errorf("Create namespace %s failed: %v", msg.name, msg.err))
			} else {
				a.enqueueCmd(a.ShowToast(fmt.Sprintf("Create namespace failed: %v", msg.err), 5*time.Second))
			}
			a.namespaceCreatePanel = -1
			return a, nil
		}
		a.enqueueCmd(a.ShowToast(fmt.Sprintf("Namespace %s created", msg.name), 3*time.Second))
		if a.namespaceCreatePanel == 0 || a.namespaceCreatePanel == 1 {
			a.refreshPanelAfterEdit(a.namespaceCreatePanel)
			other := 1 - a.namespaceCreatePanel
			if other == 0 || other == 1 {
				var otherPanel *Panel
				if other == 0 {
					otherPanel = a.leftPanel
				} else {
					otherPanel = a.rightPanel
				}
				if otherPanel != nil && otherPanel.GetCurrentPath() == "/namespaces" {
					a.refreshPanelAfterEdit(other)
				}
			}
		} else {
			a.refreshPanelAfterEdit(0)
			a.refreshPanelAfterEdit(1)
		}
		a.namespaceCreatePanel = -1
		return a, nil
	case copyFinishedMsg:
		if msg.err != nil {
			if a.toastLogger != nil {
				a.enqueueCmd(a.toastLogger.Errorf("Copy %s failed: %v", msg.subject, msg.err))
			} else {
				a.enqueueCmd(a.ShowToast(fmt.Sprintf("Copy failed: %v", msg.err), 5*time.Second))
			}
			return a, nil
		}
		if msg.path != "" {
			a.lastCopyDir = filepath.Dir(msg.path)
		}
		log := ctrllog.FromContext(a.ctx).WithName("copy")
		log.Info("copy completed", "subject", msg.subject, "path", msg.path)
		return a, nil
	case DeleteConfirmMsg:
		target := a.pendingDelete
		if msg.Close {
			a.hideModal()
		}
		// Clear pending delete regardless of confirmation outcome once we've captured it.
		a.pendingDelete = nil
		if target != nil && msg.Confirm {
			return a, a.performDelete(*target)
		}
		return a, nil
	case PanelSelectionChangedMsg:
		if panel := msg.Panel; panel != nil {
			ctxSel, cancelSel := context.WithTimeout(a.ctx, panelContextTimeout)
			cmds = append(cmds, panel.NotifySelection(ctxSel, msg.Selection))
			cancelSel()
			if idx, ok := a.panelIndexFor(panel); ok {
				other := a.panelByIndex(1 - idx)
				if other != nil {
					ctxOther, cancelOther := context.WithTimeout(a.ctx, panelContextTimeout)
					cmds = append(cmds, other.NotifySelection(ctxOther, msg.Selection))
					cancelOther()
				}
			}
		}
		a.invalidateFunctionBar("selection changed")
		a.invalidateView("selection changed")
		return a, tea.Batch(cmds...)
	case panelcontent.BackgroundMsg:
		// Delivered to both panels directly: behind a dialog or the full
		// screen terminal the default route would drop it, and the widget
		// would wait for it forever.
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		for idx := range 2 {
			if panel := a.panelByIndex(idx); panel != nil {
				cmds = append(cmds, panel.DeliverBackground(ctx, msg))
			}
		}
		cancel()
		a.invalidateView("panel content loaded")
		return a, tea.Batch(cmds...)
	case PanelModeSelectedMsg:
		if panel := a.panelByIndex(msg.PanelIndex); panel != nil {
			log := ctrllog.FromContext(a.ctx).WithName("panelMode").WithValues("panelIdx", msg.PanelIndex, "mode", msg.Mode)
			log.Info("switching panel mode")
			ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
			modeCmd := a.setPanelMode(ctx, panel, msg.Mode, fmt.Sprintf("panel %d mode %v", msg.PanelIndex, msg.Mode))
			cancel()
			if modeCmd != nil {
				cmds = append(cmds, modeCmd)
			}
			ctxSel, cancelSel := context.WithTimeout(a.ctx, panelContextTimeout)
			source := a.panelByIndex(1 - msg.PanelIndex)
			if source == nil {
				source = panel
			}
			if source != nil {
				sel := source.CurrentSelection(ctxSel)
				if sel.Item == nil {
					if item, ok := source.SelectedNavItem(ctxSel); ok {
						sel.Item = item
					}
				}
				if sel.ID != "" || sel.Item != nil {
					log.Info("replaying selection", "selectionID", sel.ID, "path", sel.Path)
					sel.Force = true
					cmds = append(cmds, panel.NotifySelection(ctxSel, sel))
				} else {
					log.Info("missing selection to replay")
				}
			}
			cancelSel()
			a.syncPanelConfig(panel)
			if nav := a.navigatorForPanel(panel); nav != nil {
				if rf, ok := nav.Current().(interface{ Refresh() }); ok {
					rf.Refresh()
				}
			}
		}
		a.hideModalName(panelModeModalKey(msg.PanelIndex))
		return a, tea.Batch(cmds...)
	case resourceDeletedMsg:
		if msg.err != nil {
			text := deleteResultText(msg)
			if a.toastLogger != nil {
				a.enqueueCmd(a.toastLogger.Errorf("%s", text))
			} else {
				a.enqueueCmd(a.ShowToast(text, 5*time.Second))
			}
		}
		// Refresh on failure too: a replaced or vanished object means the
		// panel shows stale rows.
		a.refreshPanelAfterEdit(msg.target.panelIdx)
		return a, nil
	case EscTimeoutMsg:
		// Escape sequence timed out
		a.escPressed = false
		return a, nil
	case readErrorMsg:
		var toast tea.Cmd
		if a.toastLogger != nil {
			toast = a.toastLogger.Errorf("%s", readErrorText(msg.err))
		}
		return a, tea.Batch(toast, a.waitReadErrors())
	case FolderDirtyMsg:
		panel := a.panelByIndex(msg.PanelIdx)
		if panel == nil {
			return a, nil
		}
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		panel.RefreshFolder(ctx)
		// A Describe or Manifest panel may show an object of this folder:
		// the other panel follows this one's selection, and this panel's
		// own content came from its list. Each widget rereads at most once
		// a second, and only when its object changed.
		for idx := range 2 {
			if p := a.panelByIndex(idx); p != nil {
				cmds = append(cmds, p.RefreshContent(ctx))
			}
		}
		cancel()
		a.invalidateView(fmt.Sprintf("folder dirty panel %d", msg.PanelIdx))
		// Always wake the renderer when data changed so cached frames are dropped
		cmds = append(cmds,
			tea.Tick(0, func(time.Time) tea.Msg { return nil }),
			a.waitFolderDirtyEvents(),
		)
		return a, tea.Batch(cmds...)
	case DiscoveryRefreshedMsg:
		a.handleDiscoveryRefresh()
		return a, a.watchDiscovery()

	case tea.KeyMsg:
		return a.handleKeyMsg(msg, cmds)
	case models.ConfirmRequest:
		return a, a.showConfirm(msg.Action)
	case models.ExecRequest:
		if panel := a.activePanelRef(); panel != nil {
			return a, a.resolveExecTarget(panel.Folder(), msg.Item, msg.Mode)
		}
		return a, nil
	case execResolvedMsg:
		return a, a.handleExecResolved(msg)
	case execFinishedMsg:
		return a, a.handleExecFinished(msg)
	case models.OpenObjectRequest:
		return a, a.openLinkedObject(msg.Link)
	case linkSelectMsg:
		return a, a.selectLinkedRow(msg)
	case portForwardPlanMsg, portForwardInputMsg, portForwardStartedMsg:
		return a, a.handlePortForwardMsg(msg)
	case confirmResultMsg:
		return a, a.handleConfirmResult(msg)
	case nodeActionMsg:
		return a, a.handleNodeActionMsg(msg)
	case workloadaction.Request:
		return a, a.startWorkloadAction(msg)
	case workloadMessage:
		return a, a.handleWorkloadMsg(msg)
	case transferChoiceMsg, transferCancelMsg, transferCloseMsg, transferTickMsg, transferDoneMsg:
		return a, a.handleTransferMsg(msg)
	case models.NodeShellRequest:
		return a, tea.Batch(append(cmds, a.startNodeShell(msg))...)
	case nodeShellStartedMsg:
		return a, tea.Batch(append(cmds, a.handleNodeShellStarted(msg))...)
	case execEndedMsg:
		return a, tea.Batch(append(cmds, a.handleExecEnded(msg))...)
	case nodeShellDeletedMsg:
		return a, tea.Batch(append(cmds, a.handleNodeShellDeleted(msg))...)
	case objectActionPreparedMsg:
		return a, a.handleObjectActionPrepared(msg)
	case objectActionDoneMsg:
		return a, a.handleObjectActionDone(msg)
	case selectCreatedMsg:
		return a, a.selectCreatedRow(msg)
	case startupSelectMsg:
		return a, a.selectStartupRows(msg.ids, msg.path, msg.attempt)
	case pathJumpMsg:
		return a, a.continueJump(msg.jump)
	case contextSwitchedMsg:
		return a, a.applyContextSwitch(msg)
	case namespacesLoadedMsg:
		return a, a.showNamespacePicker(msg)
	case viewer.CopyRequestMsg:
		return a, a.copyText(msg.Text, copyWhat(msg))
	case accessResultMsg:
		return a, tea.Batch(append(cmds, a.handleAccessResult())...)
	case relatedFolderMsg:
		return a, a.handleRelatedFolder(msg)
	case compareResultMsg:
		return a, msg.view.apply(msg)
	case copyResultMsg:
		if msg.error {
			return a, a.ShowToast(msg.text, 3*time.Second)
		}
		return a, a.ShowInfo(msg.text, 2*time.Second)
	case tea.PasteMsg:
		// Paste goes where typing goes: the focused command widget, or the shell.
		if a.interactiveCommandActive() {
			panel := a.activePanelRef()
			model, cmd := panel.Update(msg)
			if idx, ok := a.panelIndexFor(panel); ok {
				if idx == 0 {
					a.leftPanel = updatedModel(a.leftPanel, model)
				} else {
					a.rightPanel = updatedModel(a.rightPanel, model)
				}
			}
			return a, tea.Batch(append(cmds, cmd)...)
		}
		return a, tea.Batch(append(cmds, a.updateTerminal(msg, "paste"))...)
	case commandExitMsg:
		if msg.OnExit == appconfig.CommandExitRestore || msg.OnExit == appconfig.CommandExitClose {
			if panel := a.panelByIndex(msg.PanelIdx); panel != nil {
				ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
				modeCmd := a.setPanelMode(ctx, panel, PanelModeList, "command exit restore")
				panel.setCommandFocus(false)
				cancel()
				if modeCmd != nil {
					cmds = append(cmds, modeCmd)
				}
			}
		}
		return a, tea.Batch(cmds...)

	default:
		if mm, ok := msg.(tea.MouseMsg); ok {
			// The open menu bar takes every mouse event until it closes.
			if a.menuBar.IsOpen() {
				return a, tea.Batch(append(cmds, a.handleMenuBarMouse(mm))...)
			}
			if a.showTerminal {
				m := mm.Mouse()
				if m.Y == a.height-1 {
					if rel, ok := mm.(tea.MouseReleaseMsg); ok && rel.Mouse().Button == tea.MouseLeft {
						a.showTerminal = false
						a.terminal.SetShowPanels(true)
						a.invalidateView("terminal bar click exit")
					}
					return a, nil
				}
				if cmd := a.updateTerminal(mm, "terminal mouse"); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return a, tea.Batch(cmds...)
			}
			m := mm.Mouse()
			if m.Button == tea.MouseLeft {
				// Take both halves of a click on a scope label: a press
				// that reached the panel without its release would leave
				// the panel tracking a click and swallowing the next one.
				switch mm.(type) {
				case tea.MouseClickMsg:
					if a.scopeLabelAt(m) {
						return a, nil
					}
				case tea.MouseReleaseMsg:
					if cmd, hit := a.handleScopeClick(m); hit {
						return a, cmd
					}
				}
			}
			if m.Y == a.commandLineY() {
				if rel, ok := mm.(tea.MouseReleaseMsg); ok && rel.Mouse().Button == tea.MouseLeft && a.commandLineCopyHit(m.X) {
					return a, a.copyPanelCommand()
				}
				return a, nil
			}
			if m.Y == a.height-1 {
				if rel, ok := mm.(tea.MouseReleaseMsg); ok && rel.Mouse().Button == tea.MouseLeft {
					if cmd := a.handleFunctionKeyClick(m.X); cmd != nil {
						return a, cmd
					}
				}
				return a, nil
			}
			if a.interactiveCommandActive() {
				panel := a.activePanelRef()
				if panel != nil {
					model, cmd := panel.Update(mm)
					if panelIdx, ok := a.panelIndexFor(panel); ok {
						switch panelIdx {
						case 0:
							a.leftPanel = updatedModel(a.leftPanel, model)
						case 1:
							a.rightPanel = updatedModel(a.rightPanel, model)
						}
					}
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
					return a, tea.Batch(cmds...)
				}
			}
			if d, handled := a.dispatchPanelMouse(mm); handled {
				cmd, panel, panelMsg, panelIdx := d.cmd, d.panel, d.msg, d.panelIdx
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
				if panelMsg.Intent == PanelMouseClick && panelMsg.Button == tea.MouseLeft && panel != nil {
					ctxSel, cancelSel := context.WithTimeout(a.ctx, panelContextTimeout)
					selectionID := panel.currentSelectionID(ctxSel)
					cancelSel()
					if selectionID != "" {
						now := time.Now()
						timeout := a.cfg.Input.Mouse.DoubleClickTimeout.Duration
						if timeout <= 0 {
							timeout = 300 * time.Millisecond
						}
						if a.lastClickRowID == selectionID && a.lastClickPanel == panelIdx && now.Sub(a.lastClickTime) <= timeout {
							a.lastClickTime = time.Time{}
							a.lastClickRowID = ""
							ctxEnter, cancelEnter := context.WithTimeout(a.ctx, panelContextTimeout)
							enterCmd := panel.Enter(ctxEnter)
							cancelEnter()
							if enterCmd != nil {
								return a, enterCmd
							}
						} else {
							a.lastClickTime = now
							a.lastClickPanel = panelIdx
							a.lastClickRowID = selectionID
						}
					}
				}
				return a, tea.Batch(cmds...)
			}
			return a, tea.Batch(cmds...)
		}
		if !a.showTerminal {
			if panelCmds := a.updatePanelsWithMsg(msg); len(panelCmds) > 0 {
				cmds = append(cmds, panelCmds...)
				a.invalidateView("panel update msg")
			}
		}
		if cmd := a.updateTerminal(msg, ""); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if len(cmds) == 0 && viewWasValid && a.mainViewCache.valid {
		return a, nil
	}
	return a, tea.Batch(cmds...)
}

// shouldRouteToPanel determines if a key should be routed to the panel based on terminal state

// panelMouseDispatch is what dispatchPanelMouse did with a mouse event.
type panelMouseDispatch struct {
	cmd      tea.Cmd
	panel    *Panel
	msg      PanelMouseMsg
	panelIdx int
}

// dispatchPanelMouse routes a mouse event to the panel under it. It
// reports false when no panel took the event.
func (a *App) dispatchPanelMouse(msg tea.MouseMsg) (panelMouseDispatch, bool) {
	leftPanelWidth, rightPanelWidth := a.panelWidths()
	panelHeight := a.panelsHeight()
	headerOffset := panelHeaderOffset
	m := msg.Mouse()
	if m.Y >= panelHeight {
		return panelMouseDispatch{}, false
	}
	panelIdx := 0
	if leftPanelWidth <= 0 && rightPanelWidth > 0 {
		panelIdx = 1
	} else if rightPanelWidth > 0 && leftPanelWidth > 0 && m.X >= leftPanelWidth {
		panelIdx = 1
	}
	panel := a.panelByIndex(panelIdx)
	if panel == nil {
		return panelMouseDispatch{panelIdx: panelIdx}, false
	}
	relRow := max(m.Y-headerOffset, 0)
	var panelMsg PanelMouseMsg
	switch mm := msg.(type) {
	case tea.MouseWheelMsg:
		delta := 0
		switch mm.Button {
		case tea.MouseWheelUp:
			delta = -1
		case tea.MouseWheelDown:
			delta = 1
		default:
			return panelMouseDispatch{panelIdx: panelIdx}, false
		}
		panelMsg = PanelMouseMsg{
			Intent: PanelMouseWheel,
			DeltaY: delta,
		}
	case tea.MouseClickMsg:
		panelMsg = PanelMouseMsg{
			Intent: PanelMouseClick,
			Row:    relRow,
			Button: mm.Button,
		}
	default:
		return panelMouseDispatch{panelIdx: panelIdx}, false
	}
	if panelIdx != a.activePanel {
		a.setActivePanel(panelIdx, "panel mouse focus")
	}
	model, cmd := panel.Update(panelMsg)
	if model != nil {
		if panelIdx == 0 {
			a.leftPanel = updatedModel(a.leftPanel, model)
		} else {
			a.rightPanel = updatedModel(a.rightPanel, model)
		}
	}
	return panelMouseDispatch{cmd: cmd, panel: panel, msg: panelMsg, panelIdx: panelIdx}, true
}

func (a *App) handleCommandWatchTick(msg commandWatchTickMsg) (tea.Model, tea.Cmd) {
	if msg.PanelIdx < 0 || msg.PanelIdx > 1 {
		return a, nil
	}
	// Ignore stale ticks.
	if msg.Token != a.commandWatchToken[msg.PanelIdx] {
		return a, nil
	}
	interval := a.commandWatchInterval[msg.PanelIdx]
	if interval <= 0 {
		return a, nil
	}
	panel := a.panelByIndex(msg.PanelIdx)
	if panel == nil {
		return a, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	restart := panel.RestartCommand(ctx)
	next := a.scheduleCommandWatch(msg.PanelIdx)
	return a, tea.Batch(restart, next)
}

// passesModal reports whether msg skips an open modal and goes to the main
// switch. Modal results do, so they can close their dialog. So do messages
// from background wait loops: their handler re-arms the wait, and if the
// modal swallowed one, the loop would stop for the rest of the session
// (no more read errors, panel refreshes or discovery updates).
func passesModal(msg tea.Msg) bool {
	switch msg.(type) {
	case PanelModeSelectedMsg, DeleteConfirmMsg, NamespaceCreateResultMsg, CopyToLocalResultMsg, RowFilterResultMsg:
		return true
	case viewer.CopyRequestMsg:
		// The app owns the clipboard; the viewer only asks.
		return true
	case readErrorMsg, FolderDirtyMsg, DiscoveryRefreshedMsg:
		return true
	case panelcontent.BackgroundMsg:
		// Panel content keeps loading behind a dialog; a dropped result
		// would leave the widget waiting for it.
		return true
	case showToastMsg, toastTickMsg:
		// Toasts must keep their hide timer running behind a dialog, or
		// one shown before the dialog opened never goes away.
		return true
	case confirmResultMsg:
		// The confirmation dialog answers itself.
		return true
	case nodeActionMsg:
		// Node action dialogs answer themselves, and a drain reports
		// progress while its dialog is open.
		return true
	case workloadMessage:
		// The workload dialogs answer themselves.
		return true
	case transferChoiceMsg, transferCancelMsg, transferCloseMsg, transferTickMsg, transferDoneMsg:
		// The copy dialog drives itself; a swallowed tick would freeze its
		// progress and a swallowed result would leave it open for good.
		return true
	case accessResultMsg:
		// Function keys and an open access view redraw from the cache.
		return true
	case compareResultMsg:
		// The result names its viewer; it must not be lost when another
		// dialog (viewer options) is on top while the manifests load.
		return true
	case portForwardPlanMsg, portForwardInputMsg, portForwardStartedMsg:
		// The dialog answers itself; a forward that finished starting
		// reports even when another dialog opened meanwhile.
		return true
	case configSavedMsg:
		// C-s keeps the options dialog open; its result must still show.
		return true
	}
	return false
}
