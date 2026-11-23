package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/hotlist"
	"github.com/stokaro/kc/internal/models"
)

// Saved views (#185) are named hotlist entries that also restore the list
// settings of the place: columns, sort, namespace grouping and the row
// filter. See docs/adr/0185-saved-views.md.

const viewNameModalKey = "hotlist_view_name"

// viewNameMsg is the outcome of the view name dialog. Entry is the hotlist
// entry to rename, or -1 to save the panel as a new view.
type viewNameMsg struct {
	Panel   int
	Entry   int
	Name    string
	Confirm bool
	Close   bool
}

// currentView returns the list settings of panel idx for a saved view. It
// refuses a panel that does not show a list.
func (a *App) currentView(idx int) (hotlist.View, error) {
	p := a.panelByIndex(idx)
	if p == nil || p.Mode() != PanelModeList {
		return hotlist.View{}, fmt.Errorf("views save object lists; switch the panel to List")
	}
	v := hotlist.View{Columns: p.ColumnsMode()}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	if st, ok := p.SortState(ctx); ok {
		v.Order = st.Applied
	}
	if on, applies := p.NamespaceGrouping(); applies {
		v.GroupByNamespace = &on
	}
	if f, ok := p.RowFilter(); ok {
		v.Filter, v.FaultsOnly = f.Text(), f.FaultsOnly
	}
	return v, nil
}

// promptViewName asks for the name of a new view (entry < 0) or the new
// name of entry.
func (a *App) promptViewName(idx, entry int) tea.Cmd {
	if entry < 0 {
		if _, err := a.currentView(idx); err != nil {
			return a.ShowToast("Cannot save a view: "+err.Error(), 3*time.Second)
		}
		if _, ok := a.panelLocation(idx); !ok {
			return a.ShowToast("Cannot save a view: the panel has no location", 3*time.Second)
		}
	}
	initial, label := "", "Save view as"
	if entry >= 0 && entry < len(a.hotlist) {
		initial, label = a.hotlist[entry].Label(), "Rename to"
	}
	modal := NewTextInputModal(TextInputModalConfig{
		Title:        label,
		Label:        label,
		Description:  "a name such as payments failures; Enter saves, Esc cancels",
		ConfirmLabel: "OK",
		CancelLabel:  "Cancel",
		InitialValue: initial,
		MinWidth:     30,
		MaxWidth:     60,
		MinHeight:    5,
		HideTitle:    true,
		Validate: func(name string) error {
			_, err := a.namedHotlist(idx, entry, name)
			return err
		},
		ResultMsg: func(res TextInputModalResult) tea.Msg {
			return viewNameMsg{Panel: idx, Entry: entry, Name: strings.TrimSpace(res.Value), Confirm: res.Confirm, Close: res.Close}
		},
	})
	m := a.modalManager.modals[viewNameModalKey]
	if m == nil {
		m = NewModal(label, modal)
		m.SetCloseOnSingleEsc(true)
		a.modalManager.Register(viewNameModalKey, m)
	} else {
		m.SetContent(modal)
		m.title = label
	}
	a.configureModalWindow(m, modal, idx, "", 50, 7)
	a.showModal(viewNameModalKey)
	return nil
}

// namedHotlist returns the hotlist with name applied: a new view of panel
// idx when entry < 0, else entry renamed.
func (a *App) namedHotlist(idx, entry int, name string) ([]hotlist.Entry, error) {
	if entry >= 0 {
		return hotlist.Rename(a.hotlist, entry, name)
	}
	loc, ok := a.panelLocation(idx)
	if !ok {
		return nil, fmt.Errorf("the panel has no location")
	}
	view, err := a.currentView(idx)
	if err != nil {
		return nil, err
	}
	return hotlist.AddView(a.hotlist, name, loc, view)
}

// handleViewName saves the result of the view name dialog.
func (a *App) handleViewName(msg viewNameMsg) tea.Cmd {
	if msg.Close {
		a.hideModal()
	}
	if !msg.Confirm {
		return nil
	}
	entries, err := a.namedHotlist(msg.Panel, msg.Entry, msg.Name)
	if err == nil {
		err = a.setHotlist(entries)
	}
	if err != nil {
		return a.ShowToast("Cannot save the view: "+err.Error(), 3*time.Second)
	}
	if msg.Entry >= 0 {
		return a.ShowInfo("Renamed to "+msg.Name, 2*time.Second)
	}
	return a.ShowInfo("Saved view "+msg.Name, 2*time.Second)
}

// updateSavedView stores the location and settings of panel idx in saved
// view i. Only this explicit command changes a view; browsing never does.
func (a *App) updateSavedView(idx, i int) (string, error) {
	loc, ok := a.panelLocation(idx)
	if !ok {
		return "", fmt.Errorf("the panel has no location")
	}
	view, err := a.currentView(idx)
	if err != nil {
		return "", err
	}
	entries, err := hotlist.UpdateView(a.hotlist, i, loc, view)
	if err != nil {
		return "", fmt.Errorf("%s is a bookmark; F2 saves a view", a.hotlist[i].Label())
	}
	if err := a.setHotlist(entries); err != nil {
		return "", err
	}
	return "Updated " + entries[i].Label(), nil
}

// applySavedView restores v on panel idx after a jump reached the view's
// place. Settings the list cannot take are reported, not guessed: an
// invalid filter is left out, and a filter term on a column the list lacks
// stays (it hides every row, which the frame shows) with a warning.
func (a *App) applySavedView(idx int, v hotlist.View) tea.Cmd {
	p := a.panelByIndex(idx)
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	if v.Columns != "" {
		p.SetColumnsMode(ctx, v.Columns)
	}
	if v.Order != "" {
		p.SetObjectOrder(ctx, v.Order)
	}
	if v.GroupByNamespace != nil {
		p.SetNamespaceGrouping(ctx, *v.GroupByNamespace)
	}
	a.syncPanelConfig(p)

	var warn string
	filter, err := models.ParseRowFilter(v.Filter)
	if err != nil {
		warn = fmt.Sprintf("the saved filter %q is invalid (%v) and was left out", v.Filter, err)
		filter = models.RowFilter{}
	}
	filter.FaultsOnly = v.FaultsOnly
	if _, ok := p.RowFilter(); ok {
		a.setPanelRowFilter(p, filter)
		if check, ok := p.Folder().(models.RowFilterValidator); ok && warn == "" {
			if err := check.ValidateRowFilter(filter); err != nil {
				warn = "filter: " + err.Error()
			}
		}
	} else if filter.Active() {
		warn = "this list cannot filter rows; the saved filter was left out"
	}
	a.invalidateView("saved view")
	if warn != "" {
		return a.ShowToast("View opened, but "+warn, 5*time.Second)
	}
	return nil
}
