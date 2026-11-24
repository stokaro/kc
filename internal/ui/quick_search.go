package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/panelcontent"
)

// quickSearchStyle is the search text in the panel frame (mc draws it
// black on cyan).
var quickSearchStyle = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black)

// quickSearch is mc's quick search (C-s, M-s): typing jumps to the
// first row whose name starts with the text.
type quickSearch struct {
	active bool
	text   string
	// pending is set while text was typed into a list that had no rows
	// yet: the search runs again when the rows arrive.
	pending bool
	// hidden is the type the text names when no row matches it: a type
	// of the list that the profile leaves out or that has no row because
	// it is empty or not accessible. Enter opens it.
	hidden *hiddenTypeMatch
}

// hiddenTypeMatch is a type quick search found among the types a type list
// does not show, and why it has no row.
type hiddenTypeMatch struct {
	entry models.TypeEntry
	why   string
}

// QuickSearchActive reports whether the panel is in quick search.
func (p *Panel) QuickSearchActive() bool { return p.search.active }

// StartQuickSearch enters quick search; it works on object lists only.
func (p *Panel) StartQuickSearch() bool {
	if p.mode != PanelModeList || p.listWidget(context.Background()) == nil {
		return false
	}
	p.search = quickSearch{active: true}
	p.invalidateRenderCache()
	return true
}

// StopQuickSearch leaves quick search.
func (p *Panel) StopQuickSearch() {
	if !p.search.active {
		return
	}
	p.search = quickSearch{}
	p.invalidateRenderCache()
}

// QuickSearchKey handles a key during quick search. C-s or M-s jumps to
// the next match, Backspace shortens the text, a printable key extends it
// when some row still matches, and Esc ends the search. Any other key ends
// the search and is not handled, so it does its usual job (Enter enters
// the found row).
func (p *Panel) QuickSearchKey(key tea.KeyPressMsg) bool {
	if !p.search.active {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), panelContextTimeout)
	defer cancel()
	lw := p.listWidget(ctx)
	if lw == nil {
		p.StopQuickSearch()
		return false
	}
	defer p.invalidateRenderCache()
	switch key.String() {
	case "ctrl+s", "alt+s":
		if p.search.hidden != nil {
			p.findHiddenType(p.search.text, true)
			return true
		}
		lw.QuickSearch(ctx, p.search.text, true)
		return true
	case "esc":
		p.StopQuickSearch()
		return true
	case "backspace", "ctrl+h":
		if r := []rune(p.search.text); len(r) > 0 {
			p.search.text = string(r[:len(r)-1])
			p.search.hidden = nil
			if !lw.QuickSearch(ctx, p.search.text, false) && p.search.text != "" {
				p.findHiddenType(p.search.text, false)
			}
		}
		return true
	}
	if k := key.Key(); k.Text != "" && k.Mod&^tea.ModShift == 0 {
		text := p.search.text + k.Text
		switch {
		case lw.QuickSearch(ctx, text, false):
			p.search.text, p.search.pending, p.search.hidden = text, false, nil
		case p.findHiddenType(text, false):
			// No row matches, but a type the list does not show does.
			p.search.text, p.search.pending = text, false
		case !hasObjectRows(lw.Items()):
			// A live list may still be loading: keep the text and search
			// when its rows arrive (see applyPendingQuickSearch).
			p.search.text, p.search.pending = text, true
		}
		// mc ignores a character that matches no row of a loaded list.
		return true
	}
	p.StopQuickSearch()
	return false
}

// HiddenTypeMatch returns the type quick search found among the types the
// panel's type list does not show, if any: Enter opens it.
func (p *Panel) HiddenTypeMatch() (models.TypeEntry, bool) {
	if !p.search.active || p.search.hidden == nil {
		return models.TypeEntry{}, false
	}
	return p.search.hidden.entry, true
}

// findHiddenType looks for a type of the panel's type list whose name
// starts with text and that has no row: the profile leaves it out, or it
// is empty or not accessible. With next it looks after the current match.
// It reports whether one matched, and keeps the current match when none
// does.
func (p *Panel) findHiddenType(text string, next bool) bool {
	src, ok := p.Folder().(models.TypeCatalogSource)
	if !ok || text == "" {
		return false
	}
	catalog := src.TypeCatalog()
	q := strings.ToLower(text)
	var matches []hiddenTypeMatch
	for _, e := range catalog.Entries {
		why := e.Hidden
		if !e.Decision.Included {
			why = "outside " + catalog.Profile.Title()
		}
		if why != "" && strings.HasPrefix(strings.ToLower(e.GVR.Resource), q) {
			matches = append(matches, hiddenTypeMatch{entry: e, why: why})
		}
	}
	if len(matches) == 0 {
		return false
	}
	pick := 0
	if next && p.search.hidden != nil {
		for i, m := range matches {
			if m.entry.ID == p.search.hidden.entry.ID {
				pick = (i + 1) % len(matches)
			}
		}
	}
	p.search.hidden = &matches[pick]
	return true
}

// note is the footer line of a hidden match: what Enter does, the type
// and why it has no row. Enter comes first, so a narrow panel keeps it.
func (m *hiddenTypeMatch) note() string {
	return "Enter opens " + m.entry.GVR.Resource + " (" + m.entry.GVR.GroupVersion().String() + "), " + m.why
}

// quickSearchSelectionChanged records the row the search moved the cursor
// to as the panel's selection and returns the command that tells the other
// panel, as a cursor key does.
func (p *Panel) quickSearchSelectionChanged() tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), panelContextTimeout)
	defer cancel()
	return p.widgetSelectionChanged(ctx, panelcontent.Selection{})
}

// applyPendingQuickSearch runs the search for text typed before the list
// had rows, once it has them. It reports whether the cursor moved.
func (p *Panel) applyPendingQuickSearch(ctx context.Context) bool {
	if !p.search.active || !p.search.pending || p.search.text == "" {
		return false
	}
	lw := p.listWidget(ctx)
	if lw == nil || !hasObjectRows(lw.Items()) {
		return false
	}
	p.search.pending = false
	p.invalidateRenderCache()
	return lw.QuickSearch(ctx, p.search.text, false)
}

// hasObjectRows reports whether items has a row other than "/..".
func hasObjectRows(items []Item) bool {
	for _, it := range items {
		if it.Item == nil || it.Name == ".." {
			continue
		}
		if back, ok := it.Item.(models.Back); ok && back.IsBack() {
			continue
		}
		return true
	}
	return false
}

// renderQuickSearch is the frame label shown instead of the scope while
// searching. A match among the types the list does not show draws the
// label in the "outside" mark's colors with the reason after the text.
func renderQuickSearch(text string, hidden *hiddenTypeMatch, dash string, maxWidth int) string {
	if hidden != nil {
		label := truncateStringToWidth(" Search: "+text+"_ "+hidden.why+" ", max(0, maxWidth-lipgloss.Width(dash)))
		return dash + typeMarkStyle().Render(label)
	}
	label := truncateStringToWidth(" Search: "+text+"_ ", max(0, maxWidth-lipgloss.Width(dash)))
	return dash + quickSearchStyle.Render(label)
}

// startQuickSearch starts quick search in the active panel, unless the
// terminal or a command has the keyboard.
func (a *App) startQuickSearch() bool {
	if a.showTerminal || a.interactiveCommandActive() || (a.terminal != nil && a.terminal.HasInput()) {
		return false
	}
	p := a.activePanelRef()
	if p == nil || !p.StartQuickSearch() {
		return false
	}
	a.invalidateView("quick search start")
	return true
}
