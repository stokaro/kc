package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/dialog"
)

// DeleteConfirmMsg signals the result of the delete confirmation dialog.
type DeleteConfirmMsg struct {
	Confirm bool
	Close   bool
}

// DeleteConfirmModel renders a simple Yes/No confirmation prompt.
type DeleteConfirmModel struct {
	width, height int
	target        string
	context       string
	focus         int // 0=yes, 1=no
	buttonRect    [2]buttonRect
}

type buttonRect struct{ x, y, w, h int }

func (r buttonRect) contains(px, py int) bool {
	return px >= r.x && px < r.x+r.w && py >= r.y && py < r.y+r.h
}

func NewDeleteConfirmModel() *DeleteConfirmModel {
	return &DeleteConfirmModel{focus: 1}
}

func (m *DeleteConfirmModel) Init() tea.Cmd          { return nil }
func (m *DeleteConfirmModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// Configure sets the resource details displayed in the dialog. context is
// the kubeconfig context the object was read from; empty hides the line.
func (m *DeleteConfirmModel) Configure(target, context string) {
	m.target = target
	m.context = context
	m.focus = 1 // default to "No"
}

func (m *DeleteConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch key := msg.(type) {
	case tea.KeyMsg:
		switch strings.ToLower(key.String()) {
		case "esc", "ctrl+c", "ctrl+g":
			return m, func() tea.Msg { return DeleteConfirmMsg{Confirm: false, Close: true} }
		case "shift+tab":
			m.focus = (m.focus + 1) % 2
			return m, nil
		case "y":
			return m, func() tea.Msg { return DeleteConfirmMsg{Confirm: true, Close: true} }
		case "n":
			return m, func() tea.Msg { return DeleteConfirmMsg{Confirm: false, Close: true} }
		}
		k := key.Key()
		switch k.Code {
		case tea.KeyEnter:
			return m, func() tea.Msg {
				return DeleteConfirmMsg{
					Confirm: m.focus == 0,
					Close:   true,
				}
			}
		case tea.KeyLeft, tea.KeyRight, tea.KeyTab:
			m.focus = (m.focus + 1) % 2
			return m, nil
		}
	case tea.MouseMsg:
		mouse := key.Mouse()
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		for idx, r := range m.buttonRect {
			if !r.contains(mouse.X, mouse.Y) {
				continue
			}
			if _, ok := msg.(tea.MouseClickMsg); ok {
				m.focus = idx
				return m, nil
			}
			if _, ok := msg.(tea.MouseReleaseMsg); ok {
				return m, func() tea.Msg {
					return DeleteConfirmMsg{
						Confirm: idx == 0,
						Close:   true,
					}
				}
			}
		}
	}
	return m, nil
}

func (m *DeleteConfirmModel) View() tea.View {
	innerWidth := max(30, m.width-4)
	skin := dialog.Danger()
	row := skin.Normal.Width(innerWidth)
	// The title wraps rather than cuts: the name is what the user checks
	// before confirming.
	lines := []string{row.Bold(true).Align(lipgloss.Center).Render(fmt.Sprintf("Delete %s?", m.target))}
	if m.context != "" {
		lines = append(lines, row.Align(lipgloss.Center).Render(fmt.Sprintf("Context: %s", m.context)))
	}
	lines = append(lines, row.Render(""))
	top := lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, lines...))
	// No is the default answer, so Enter right after opening keeps the
	// object, as mc's confirm dialogs do.
	buttons, rects := dialog.RenderButtons(skin, innerWidth, []dialog.Button{
		{Label: "&Yes"}, {Label: "&No", Default: true},
	}, m.focus)
	for i := range m.buttonRect {
		m.buttonRect[i] = buttonRect{}
	}
	for i, r := range rects {
		if i < len(m.buttonRect) {
			m.buttonRect[i] = buttonRect{x: r.X, y: top, w: r.W, h: 1}
		}
	}
	lines = append(lines, buttons)
	return tea.NewView(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

// Height is the number of lines the dialog shows in a window whose inner
// width is innerW: the title (wrapped), the context, a blank line and
// the buttons.
func (m *DeleteConfirmModel) Height(innerW int) int {
	w, h := m.width, m.height
	m.SetDimensions(innerW, h)
	n := lipgloss.Height(m.View().Content)
	m.SetDimensions(w, h)
	return n
}

func (m *DeleteConfirmModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Enter", Label: "Confirm", Enabled: true},
		{Key: "Esc", Label: "Cancel", Enabled: true},
	}
}
