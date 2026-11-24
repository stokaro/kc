package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
)

// Keys of the row filter (#30) and the faults toggle (#38).
const (
	rowFilterKey   = "alt+f"
	faultsOnlyKey  = "ctrl+z"
	rowFilterModal = "row_filter"
)

// rowFilterStyle is the active filter in the panel frame.
var rowFilterStyle = lipgloss.NewStyle().Background(lipgloss.Yellow).Foreground(lipgloss.Black)

// rowFilterFolder returns the panel's folder when it can filter rows.
func (p *Panel) rowFilterFolder() (models.RowFilterable, bool) {
	if p.mode != PanelModeList {
		return nil, false
	}
	f, ok := p.Folder().(models.RowFilterable)
	return f, ok && f != nil
}

// RowFilter returns the filter of the panel's folder; ok is false when the
// folder cannot filter.
func (p *Panel) RowFilter() (models.RowFilter, bool) {
	f, ok := p.rowFilterFolder()
	if !ok {
		return models.RowFilter{}, false
	}
	return f.RowFilter(), true
}

// SetRowFilter sets the filter of the panel's folder and redraws the rows.
// The cursor stays on its row when the filter keeps it. It reports whether
// the folder can filter.
func (p *Panel) SetRowFilter(ctx context.Context, filter models.RowFilter) bool {
	f, ok := p.rowFilterFolder()
	if !ok {
		return false
	}
	f.SetRowFilter(filter)
	p.RefreshFolder(ctx)
	return true
}

// renderRowFilter is the frame label of an active filter. A label cut to
// maxWidth ends in "…".
func renderRowFilter(label, dash string, maxWidth int) string {
	room := maxWidth - lipgloss.Width(dash) - 2
	if room < 1 {
		return ""
	}
	return dash + rowFilterStyle.Render(" "+ansi.Truncate("Filter: "+label, room, "…")+" ")
}

// panelKeysFree reports whether a panel shortcut may take a key: the
// terminal, a command or typed shell input do not have the keyboard.
func (a *App) panelKeysFree() bool {
	return !a.showTerminal && !a.interactiveCommandActive() && (a.terminal == nil || !a.terminal.HasInput())
}

// rowFilterTarget returns the active panel when it shows a list that can
// be filtered, and tells the user otherwise.
func (a *App) rowFilterTarget() (*Panel, bool) {
	p := a.activePanelRef()
	if p == nil {
		return nil, false
	}
	if _, ok := p.rowFilterFolder(); !ok {
		a.enqueueCmd(a.ShowToast("Filters work on object lists", 2*time.Second))
		return nil, false
	}
	return p, true
}

// showRowFilterDialog opens the Filter dialog for the active panel.
func (a *App) showRowFilterDialog() tea.Cmd {
	p, ok := a.rowFilterTarget()
	if !ok || a.modalManager == nil {
		return nil
	}
	idx := a.panelIndex(p)
	current, _ := p.RowFilter()
	var check func(models.RowFilter) error
	if v, ok := p.Folder().(models.RowFilterValidator); ok {
		check = v.ValidateRowFilter
	}
	model := newRowFilterModelFor(idx, current.Text(), check)
	modal := a.modalManager.modals[rowFilterModal]
	if modal == nil {
		modal = NewModal("Filter", model)
		modal.SetCloseOnSingleEsc(true)
		a.modalManager.Register(rowFilterModal, modal)
	} else {
		modal.SetContent(model)
	}
	a.configureModalWindow(modal, model, idx, "", 50, 7)
	modal.SetOnClose(func() tea.Cmd { return nil })
	a.showModal(rowFilterModal)
	return nil
}

// applyRowFilterText sets the filter text of a panel and keeps its faults
// toggle.
func (a *App) applyRowFilterText(idx int, text string) {
	p := a.panelByIndex(idx)
	if p == nil {
		return
	}
	filter, err := models.ParseRowFilter(text)
	if err != nil {
		// The dialog validates, so this only happens if the text changed.
		a.enqueueCmd(a.ShowToast(err.Error(), 3*time.Second))
		return
	}
	current, _ := p.RowFilter()
	filter.FaultsOnly = current.FaultsOnly
	a.setPanelRowFilter(p, filter)
}

// toggleFaultsOnly switches "faults only" for the active panel.
func (a *App) toggleFaultsOnly() tea.Cmd {
	p, ok := a.rowFilterTarget()
	if !ok {
		return nil
	}
	filter, _ := p.RowFilter()
	filter.FaultsOnly = !filter.FaultsOnly
	a.setPanelRowFilter(p, filter)
	return nil
}

// setPanelFaultsOnly sets "faults only" for a panel (from the panel options, M-F1/M-F2).
func (a *App) setPanelFaultsOnly(p *Panel, on bool) {
	if p == nil {
		return
	}
	filter, ok := p.RowFilter()
	if !ok || filter.FaultsOnly == on {
		return
	}
	filter.FaultsOnly = on
	a.setPanelRowFilter(p, filter)
}

// clearRowFilter drops the filter of a panel (a click on the frame label).
func (a *App) clearRowFilter(idx int) {
	if p := a.panelByIndex(idx); p != nil {
		a.setPanelRowFilter(p, models.RowFilter{})
	}
}

func (a *App) setPanelRowFilter(p *Panel, filter models.RowFilter) {
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	if !p.SetRowFilter(ctx, filter) {
		return
	}
	a.invalidateView("row filter")
	// The copied kubectl command carries the label selector.
	a.invalidateFunctionBar("row filter")
}
