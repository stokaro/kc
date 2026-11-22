package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/styles"
)

// scopeChoice is one entry of a scope picker.
type scopeChoice struct {
	Label string // shown
	Value string // returned on select
	Note  string // dim text after the label, e.g. "current"
}

// ScopePickerModel is a list to pick a namespace (or a context) from. Typing
// filters the list by substring, like mc's quick search narrows a panel.
type ScopePickerModel struct {
	choices  []scopeChoice
	filter   string
	visible  []int
	selected int
	scroll   int
	width    int
	height   int
	onSelect func(scopeChoice) tea.Cmd
	onCancel func() tea.Cmd
	// typed offers the filter text itself as the last entry when it is a
	// valid name that no entry has (see AllowTypedEntry).
	typed bool
	// onKey, when set, sees each key before the picker does, with the
	// highlighted choice (nil when the list is empty). It reports whether
	// it used the key.
	onKey func(key string, current *scopeChoice) (bool, tea.Cmd)
}

// typedEntry marks the typed name in visible.
const typedEntry = -1

func NewScopePickerModel(choices []scopeChoice, current string, onSelect func(scopeChoice) tea.Cmd, onCancel func() tea.Cmd) *ScopePickerModel {
	m := &ScopePickerModel{choices: choices, onSelect: onSelect, onCancel: onCancel}
	m.refilter()
	for i, idx := range m.visible {
		if choices[idx].Value == current {
			m.selected = i
		}
	}
	return m
}

func (m *ScopePickerModel) Init() tea.Cmd { return nil }

// SetChoices replaces the entries and keeps the highlight in range.
func (m *ScopePickerModel) SetChoices(choices []scopeChoice) {
	m.choices = choices
	m.refilter()
}

// HandleModalEscape makes one Esc clear the filter, or close the picker
// when there is none, instead of the modal's Esc Esc sequence.
func (m *ScopePickerModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) {
	if m.filter != "" {
		m.filter = ""
		m.refilter()
		return true, nil
	}
	if m.onCancel != nil {
		return true, m.onCancel()
	}
	return false, nil
}

func (m *ScopePickerModel) SetDimensions(w, h int) {
	m.width, m.height = w, h
	m.ensureVisible()
}

func (m *ScopePickerModel) refilter() {
	q := strings.ToLower(m.filter)
	m.visible = m.visible[:0]
	for i, c := range m.choices {
		if q == "" || strings.Contains(strings.ToLower(c.Label), q) {
			m.visible = append(m.visible, i)
		}
	}
	if m.typedAvailable() {
		// Last, so Enter still picks a known name the filter matches.
		m.visible = append(m.visible, typedEntry)
	}
	m.selected = min(m.selected, max(0, len(m.visible)-1))
	m.ensureVisible()
}

func (m *ScopePickerModel) ensureVisible() {
	rows := m.listRows()
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	if rows > 0 && m.selected >= m.scroll+rows {
		m.scroll = m.selected - rows + 1
	}
}

// listRows is the room for entries: the last row shows the filter.
func (m *ScopePickerModel) listRows() int { return max(1, m.height-1) }

func (m *ScopePickerModel) move(d int) {
	if len(m.visible) == 0 {
		return
	}
	m.selected = max(0, min(len(m.visible)-1, m.selected+d))
	m.ensureVisible()
}

//nolint:gocyclo // one short case per key
func (m *ScopePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch mm := msg.(type) {
	case tea.MouseWheelMsg:
		switch mm.Mouse().Button {
		case tea.MouseWheelUp:
			m.move(-1)
		case tea.MouseWheelDown:
			m.move(1)
		}
		return m, nil
	case tea.MouseReleaseMsg:
		return m, m.clickRow(mm.Mouse())
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if m.onKey != nil {
		var current *scopeChoice
		if m.selected < len(m.visible) {
			c := m.choices[m.visible[m.selected]]
			current = &c
		}
		if used, cmd := m.onKey(key.String(), current); used {
			return m, cmd
		}
	}
	switch key.String() {
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.refilter()
			return m, nil
		}
		if m.onCancel != nil {
			return m, m.onCancel()
		}
	case "up":
		m.move(-1)
	case "down":
		m.move(1)
	case "pgup":
		m.move(-m.listRows())
	case "pgdown":
		m.move(m.listRows())
	case "home":
		m.move(-len(m.visible))
	case "end":
		m.move(len(m.visible))
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
			m.refilter()
		}
	case "enter":
		if m.selected < len(m.visible) && m.onSelect != nil {
			return m, m.onSelect(m.choiceAt(m.selected))
		}
	default:
		if key.Text != "" && key.Mod == 0 {
			m.filter += key.Text
			m.selected = 0
			m.refilter()
		}
	}
	return m, nil
}

// clickRow chooses the entry under a left click (content coordinates).
func (m *ScopePickerModel) clickRow(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft || mouse.Y < 0 || mouse.Y >= m.listRows() || mouse.X < 0 || mouse.X >= m.width {
		return nil
	}
	i := m.scroll + mouse.Y
	if i >= len(m.visible) {
		return nil
	}
	m.selected = i
	if m.onSelect == nil {
		return nil
	}
	return m.onSelect(m.choiceAt(i))
}

func (m *ScopePickerModel) View() tea.View {
	row := lipgloss.NewStyle().Background(lipgloss.Color(styles.ColorModalBg)).Foreground(lipgloss.Color(styles.ColorModalFg))
	sel := row.Background(lipgloss.Color(styles.ColorModalSelBg)).Bold(true)
	note := row.Foreground(lipgloss.Color(styles.ColorDarkGrey))
	var lines []string
	end := min(len(m.visible), m.scroll+m.listRows())
	for i := m.scroll; i < end; i++ {
		c := m.choiceAt(i)
		style, noteStyle := row, note
		if i == m.selected {
			style, noteStyle = sel, sel
		}
		text := " " + c.Label
		suffix := ""
		if c.Note != "" {
			suffix = "  " + c.Note
		}
		text = truncateStringToWidth(text, max(0, m.width-lipgloss.Width(suffix)))
		pad := max(0, m.width-lipgloss.Width(text)-lipgloss.Width(suffix))
		lines = append(lines, style.Render(text)+noteStyle.Render(suffix)+style.Render(strings.Repeat(" ", pad)))
	}
	for len(lines) < m.listRows() {
		lines = append(lines, row.Width(m.width).Render(""))
	}
	filter := " Filter: " + m.filter
	if m.typed {
		filter = " Name or filter: " + m.filter
	}
	if len(m.visible) == 0 {
		filter += "  (no match)"
	}
	lines = append(lines, note.Width(m.width).Render(truncateStringToWidth(filter, m.width)))
	return tea.NewView(strings.Join(lines, "\n"))
}
