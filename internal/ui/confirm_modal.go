package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sttts/kc/internal/models"
	uistyles "github.com/sttts/kc/internal/ui/styles"
)

// confirmResultMsg reports the answer of a red confirmation dialog.
type confirmResultMsg struct{ ok bool }

// ConfirmModel is the content of a red dialog that confirms a change to
// the cluster: what, where (context, namespace, object), and what stays
// behind. Cancel has the focus, so Enter alone never changes anything.
type ConfirmModel struct {
	c             models.Confirmation
	width, height int
	focus         int // 0 = action, 1 = cancel
	buttons       [2]buttonRect
}

func NewConfirmModel(c models.Confirmation) *ConfirmModel {
	if c.Action == "" {
		c.Action = "OK"
	}
	return &ConfirmModel{c: c, focus: 1}
}

func (m *ConfirmModel) Init() tea.Cmd          { return nil }
func (m *ConfirmModel) SetDimensions(w, h int) { m.width, m.height = w, h }

func answer(ok bool) tea.Cmd { return func() tea.Msg { return confirmResultMsg{ok: ok} } }

// HandleModalEscape cancels on a single Esc.
func (m *ConfirmModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) { return true, answer(false) }

func (m *ConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab", "shift+tab", "left", "right":
			m.focus = 1 - m.focus
		case "enter":
			return m, answer(m.focus == 0)
		}
	case tea.MouseReleaseMsg:
		mouse := msg.Mouse()
		for i, r := range m.buttons {
			if mouse.Button == tea.MouseLeft && r.contains(mouse.X, mouse.Y) {
				return m, answer(i == 0)
			}
		}
	}
	return m, nil
}

// lines renders the body without the buttons.
func (m *ConfirmModel) lines() []string {
	out := []string{" " + m.c.Title, ""}
	labelW := 0
	for _, d := range m.c.Details {
		labelW = max(labelW, len(d[0]))
	}
	for _, d := range m.c.Details {
		out = append(out, " "+d[0]+":"+strings.Repeat(" ", labelW-len(d[0])+2)+d[1])
	}
	if len(m.c.Notes) > 0 {
		out = append(out, "")
		for _, n := range m.c.Notes {
			out = append(out, " "+n)
		}
	}
	return append(out, "")
}

// size is the content size the dialog needs.
func (m *ConfirmModel) size() (int, int) {
	w := 0
	for _, l := range m.lines() {
		w = max(w, lipgloss.Width(l)+1)
	}
	return max(w, 30), len(m.lines()) + 1
}

func (m *ConfirmModel) View() tea.View {
	bg := lipgloss.Color(uistyles.ColorDangerBg)
	fg := lipgloss.Color(uistyles.ColorDangerFg)
	row := lipgloss.NewStyle().Background(bg).Foreground(fg).Width(m.width)
	var out []string
	for i, l := range m.lines() {
		style := row
		if i == 0 {
			style = style.Bold(true)
		}
		out = append(out, style.Render(truncateStringToWidth(l, m.width)))
	}
	labels := [2]string{"[ " + m.c.Action + " ]", "[ Cancel ]"}
	normal := lipgloss.NewStyle().Background(bg).Foreground(fg)
	focused := lipgloss.NewStyle().Background(fg).Foreground(lipgloss.Color(uistyles.ColorBlack)).Bold(true)
	total := lipgloss.Width(labels[0]) + 2 + lipgloss.Width(labels[1])
	x := max(0, (m.width-total)/2)
	y := len(out)
	var b strings.Builder
	b.WriteString(normal.Render(strings.Repeat(" ", x)))
	for i, l := range labels {
		style := normal
		if i == m.focus {
			style = focused
		}
		m.buttons[i] = buttonRect{x: x, y: y, w: lipgloss.Width(l), h: 1}
		b.WriteString(style.Render(l))
		x += lipgloss.Width(l)
		if i == 0 {
			b.WriteString(normal.Render("  "))
			x += 2
		}
	}
	out = append(out, row.Render(b.String()))
	return tea.NewView(strings.Join(out, "\n"))
}

// showConfirm opens a red dialog for action and remembers it until the
// user answers.
func (a *App) showConfirm(action models.ConfirmedAction) tea.Cmd {
	// A confirmed action changes the cluster by definition.
	if a.accessPolicy().ReadOnly {
		return a.readOnlyRefusal(confirmedActionName(action))
	}
	model := NewConfirmModel(action.Confirmation())
	modal := NewModal("Confirm", model)
	modal.SetDanger(true)
	a.pendingConfirm = action
	a.modalManager.Register("confirm_action", modal)
	w, h := model.size()
	a.configureModalWindow(modal, model, a.activePanel, "", min(w+2, max(30, a.width-6)), h+2)
	a.modalManager.Show("confirm_action")
	a.invalidateView("confirm action")
	return nil
}

// handleConfirmResult closes the dialog and runs the action when approved.
func (a *App) handleConfirmResult(msg confirmResultMsg) tea.Cmd {
	action := a.pendingConfirm
	a.pendingConfirm = nil
	a.hideModalName("confirm_action")
	if !msg.ok || action == nil {
		return nil
	}
	if a.accessPolicy().ReadOnly {
		// The context became read-only while the dialog was open.
		return a.readOnlyRefusal(confirmedActionName(action))
	}
	if err := action.Confirm(); err != nil {
		return a.ShowToast("Failed: "+err.Error(), 5*time.Second)
	}
	return nil
}

// confirmedActionName names a confirmed action in messages: its dialog
// title without the question mark ("Start a debug helper").
func confirmedActionName(action models.ConfirmedAction) string {
	if name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(action.Confirmation().Title), "?")); name != "" {
		return name
	}
	return "This action"
}
