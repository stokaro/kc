package ui

import (
	"fmt"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/dialog"
)

// DeleteConfirmMsg signals the result of the delete confirmation dialog.
type DeleteConfirmMsg struct {
	Confirm bool
	Close   bool
}

// DeleteConfirmModel is the red Yes/No dialog of F8. It asks about one
// object, or about the marked rows: their count, kind and first names.
type DeleteConfirmModel struct {
	width, height int
	title         string // the question, "Delete pod shop/web-1?"
	context       string
	// names are the objects of a marked set the dialog lists, more the
	// number of objects the set holds beyond them.
	names      []string
	more       int
	focus      int // 0=yes, 1=no
	buttonRect [2]buttonRect
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

// Configure asks about one object, target ("pod shop/web-1"). context is
// the kubeconfig context the object was read from; empty hides the line.
func (m *DeleteConfirmModel) Configure(target, context string) {
	m.ConfigureSet(fmt.Sprintf("Delete %s?", target), context, nil, 0)
}

// ConfigureSet asks title about a set of objects, listing names and
// saying how many more the set holds.
func (m *DeleteConfirmModel) ConfigureSet(title, context string, names []string, more int) {
	m.title = title
	m.context = context
	m.names = append([]string(nil), names...)
	m.more = more
	m.focus = 1 // default to "No"
}

// answerDelete is the dialog's answer.
func answerDelete(confirm bool) tea.Cmd {
	return func() tea.Msg { return DeleteConfirmMsg{Confirm: confirm, Close: true} }
}

// Update handles the keys of a red dialog: a plain letter does nothing, so
// a "y" typed for the shell line or a fast double press cannot delete.
// Enter runs the focused button, No when the dialog opens; M-y and M-n
// pick the button whose letter is highlighted.
func (m *DeleteConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c", "ctrl+g":
			return m, answerDelete(false)
		case "enter":
			return m, answerDelete(m.focus == 0)
		case "left", "right", "tab", "shift+tab":
			m.focus = 1 - m.focus
			return m, nil
		}
		if key := msg.Key(); key.Mod == tea.ModAlt {
			switch unicode.ToLower(key.Code) {
			case 'y':
				return m, answerDelete(true)
			case 'n':
				return m, answerDelete(false)
			}
		}
	case tea.MouseMsg:
		mouse := msg.Mouse()
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
				return m, answerDelete(idx == 0)
			}
		}
	}
	return m, nil
}

func (m *DeleteConfirmModel) View() tea.View {
	innerWidth := max(30, m.width-4)
	skin := dialog.Danger()
	row := skin.Normal.Width(innerWidth)
	// The title and the names wrap rather than cut: the names are what
	// the user checks before confirming.
	lines := []string{row.Bold(true).Align(lipgloss.Center).Render(m.title)}
	if m.context != "" {
		lines = append(lines, row.Align(lipgloss.Center).Render(fmt.Sprintf("Context: %s", m.context)))
	}
	if len(m.names) > 0 {
		lines = append(lines, row.Render(""))
		item := row.PaddingLeft(2)
		for _, name := range m.names {
			lines = append(lines, item.Render(name))
		}
		if m.more > 0 {
			lines = append(lines, item.Render(fmt.Sprintf("and %d more", m.more)))
		}
	}
	lines = append(lines, row.Render(""))
	top := lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, lines...))
	// No is the default answer, so Enter right after opening keeps the
	// objects, as mc's confirm dialogs do.
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
// width is innerW: the title (wrapped), the context, the names of a set
// after a blank line, a blank line and the buttons.
func (m *DeleteConfirmModel) Height(innerW int) int {
	w, h := m.width, m.height
	m.SetDimensions(innerW, h)
	n := lipgloss.Height(m.View().Content)
	m.SetDimensions(w, h)
	return n
}

// FooterHints leaves Enter out: it runs the focused button, which is No
// when the dialog opens, so "Enter Confirm" would promise a delete.
func (m *DeleteConfirmModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Esc", Label: "Cancel", Enabled: true},
	}
}
