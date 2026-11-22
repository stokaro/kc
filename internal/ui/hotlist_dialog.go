package ui

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/hotlist"
	navui "github.com/stokaro/kc/internal/navigation"
)

const (
	// hotlistKey is C-\, mc's hotlist key.
	hotlistKey      = "ctrl+\\"
	hotlistModalKey = "hotlist"
	historyModalKey = "history_list"
)

// locationChoice shows a location as the path, with "context : namespace"
// as the note. Value is the index into the list the choices came from.
func locationChoice(i int, loc navui.Location) scopeChoice {
	label := loc.Path
	if label == "" {
		label = "/"
	}
	note := namespaceScopeLabel(loc.Namespace)
	if loc.Context != "" {
		note = loc.Context + " : " + note
	}
	return scopeChoice{Label: label, Value: strconv.Itoa(i), Note: note}
}

func locationChoices(locs []navui.Location) []scopeChoice {
	out := make([]scopeChoice, 0, len(locs))
	for i, loc := range locs {
		out = append(out, locationChoice(i, loc))
	}
	return out
}

func choiceIndex(c *scopeChoice, n int) (int, bool) {
	if c == nil {
		return 0, false
	}
	i, err := strconv.Atoi(c.Value)
	return i, err == nil && i >= 0 && i < n
}

// loadHotlist reads the hotlist file. A broken file is reported and left
// alone: kc does not overwrite it until the user changes the hotlist.
func (a *App) loadHotlist() tea.Cmd {
	if a.hotlistPath == "" {
		p, err := hotlist.DefaultPath()
		if err != nil {
			return a.ShowToast(err.Error(), 5*time.Second)
		}
		a.hotlistPath = p
	}
	entries, err := hotlist.Load(a.hotlistPath)
	if err != nil {
		return a.ShowToast(err.Error(), 5*time.Second)
	}
	a.hotlist = entries
	return nil
}

// setHotlist saves entries and keeps them when the save works.
func (a *App) setHotlist(entries []navui.Location) error {
	if a.hotlistPath == "" {
		return fmt.Errorf("hotlist: no file path")
	}
	if err := hotlist.Save(a.hotlistPath, entries); err != nil {
		return err
	}
	a.hotlist = entries
	return nil
}

// addCurrentToHotlist adds the active panel's location to the hotlist.
func (a *App) addCurrentToHotlist() (string, error) {
	loc, ok := a.panelLocation(a.activePanel)
	if !ok {
		return "", fmt.Errorf("the panel has no location")
	}
	entries, added := hotlist.Add(a.hotlist, loc)
	if !added {
		return loc.Path + " is already in the hotlist", nil
	}
	if err := a.setHotlist(entries); err != nil {
		return "", err
	}
	return "Added " + loc.Path, nil
}

// removeFromHotlist removes hotlist entry i.
func (a *App) removeFromHotlist(i int) error {
	if i < 0 || i >= len(a.hotlist) {
		return nil
	}
	return a.setHotlist(hotlist.Remove(a.hotlist, i))
}

// showHotlist opens the hotlist (C-\): Enter goes to the entry, Ins adds
// the active panel's location, Del removes the highlighted entry.
func (a *App) showHotlist() tea.Cmd {
	idx := a.activePanel
	var picker *ScopePickerModel
	picker = NewScopePickerModel(locationChoices(a.hotlist), "", func(c scopeChoice) tea.Cmd {
		i, ok := choiceIndex(&c, len(a.hotlist))
		if !ok {
			return nil
		}
		target := a.hotlist[i]
		a.hideModal()
		return a.jumpTo(idx, target, true)
	}, func() tea.Cmd {
		a.hideModal()
		return nil
	})
	picker.onKey = func(key string, c *scopeChoice) (bool, tea.Cmd) {
		switch key {
		case "insert":
			msg, err := a.addCurrentToHotlist()
			if err != nil {
				return true, a.ShowToast("Cannot add to hotlist: "+err.Error(), 3*time.Second)
			}
			picker.SetChoices(locationChoices(a.hotlist))
			picker.move(len(a.hotlist))
			return true, a.ShowInfo(msg, 2*time.Second)
		case "delete", "f8":
			i, ok := choiceIndex(c, len(a.hotlist))
			if !ok {
				return true, nil
			}
			if err := a.removeFromHotlist(i); err != nil {
				return true, a.ShowToast("Cannot remove from hotlist: "+err.Error(), 3*time.Second)
			}
			picker.SetChoices(locationChoices(a.hotlist))
			return true, nil
		}
		return false, nil
	}
	modal := NewModal("Hotlist: Enter go, Ins add, Del remove", picker)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(hotlistModalKey, modal)
	a.configureModalWindow(modal, picker, idx, "", 70, min(max(len(a.hotlist), 4)+3, 20))
	a.showModal(hotlistModalKey)
	return nil
}

// showHistoryList opens the active panel's history (M-H), newest first.
// Choosing an entry moves the history cursor there, like M-y and M-u.
func (a *App) showHistoryList() tea.Cmd {
	idx := a.activePanel
	entries, cur := a.panelHistory(idx).Entries()
	choices := locationChoices(entries)
	if cur >= 0 && cur < len(choices) {
		choices[cur].Note += "  current"
	}
	slices.Reverse(choices)
	picker := NewScopePickerModel(choices, strconv.Itoa(cur), func(c scopeChoice) tea.Cmd {
		a.hideModal()
		i, ok := choiceIndex(&c, len(entries))
		if !ok {
			return nil
		}
		loc, ok := a.panelHistory(idx).Go(i)
		if !ok {
			return nil
		}
		return a.jumpTo(idx, loc, false)
	}, func() tea.Cmd {
		a.hideModal()
		return nil
	})
	modal := NewModal("History", picker)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(historyModalKey, modal)
	a.configureModalWindow(modal, picker, idx, "", 70, min(max(len(choices), 4)+3, 20))
	a.showModal(historyModalKey)
	return nil
}
