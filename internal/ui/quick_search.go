package ui

import (
	"context"

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
		lw.QuickSearch(ctx, p.search.text, true)
		return true
	case "esc":
		p.StopQuickSearch()
		return true
	case "backspace", "ctrl+h":
		if r := []rune(p.search.text); len(r) > 0 {
			p.search.text = string(r[:len(r)-1])
			lw.QuickSearch(ctx, p.search.text, false)
		}
		return true
	}
	if k := key.Key(); k.Text != "" && k.Mod&^tea.ModShift == 0 {
		text := p.search.text + k.Text
		switch {
		case lw.QuickSearch(ctx, text, false):
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
// searching.
func renderQuickSearch(text, dash string, maxWidth int) string {
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
