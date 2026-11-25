package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/dialog"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
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
	if m.c.Command != "" {
		out = append(out, "", " Same as:", " "+m.c.Command)
	}
	return append(out, "")
}

// size is the content size the dialog needs.
func (m *ConfirmModel) size() (width, height int) {
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
	// Enter on the focused button runs it; the default marker stays on
	// Cancel, which has the focus when the dialog opens.
	buttonRow, rects := dialog.RenderButtons(dialog.Danger(), m.width, []dialog.Button{
		{Label: dialog.Label(m.c.Action)}, {Label: "Cancel", Default: true},
	}, m.focus)
	for i, r := range rects {
		m.buttons[i] = buttonRect{x: r.X, y: len(out), w: r.W, h: 1}
	}
	out = append(out, buttonRow)
	return tea.NewView(strings.Join(out, "\n"))
}

// pendingConfirmation is the change a red dialog waits to run: either a
// row action, run in place, or a cluster action, run in the background.
type pendingConfirmation struct {
	row      models.ConfirmedAction
	cluster  models.ClusterAction
	panelIdx int // the panel the cluster action came from
	// kubeContext is the context the action changes, for the read-only
	// check when the dialog is answered.
	kubeContext string
	// conn is the connection of the row the action came from; a follow
	// up (the node shell) runs there.
	conn *panelConn
}

// showConfirm opens a red dialog for action, a row of conn, and remembers
// it until the user answers.
func (a *App) showConfirm(conn *panelConn, action models.ConfirmedAction) tea.Cmd {
	return a.showConfirmation(action.Confirmation(), &pendingConfirmation{row: action, kubeContext: conn.name(), conn: conn})
}

// showClusterActionConfirm opens a red dialog for a cluster action in
// kubeContext that came from panel panelIdx.
func (a *App) showClusterActionConfirm(action models.ClusterAction, panelIdx int, kubeContext string) tea.Cmd {
	return a.showConfirmation(action.Confirmation(), &pendingConfirmation{cluster: action, panelIdx: panelIdx, kubeContext: kubeContext, conn: a.ownConn(panelIdx)})
}

// confirmationContext is the Context row of c, "" when it has none.
func confirmationContext(c models.Confirmation) string {
	for _, d := range c.Details {
		if d[0] == "Context" {
			return d[1]
		}
	}
	return ""
}

// showConfirmation opens the red dialog. The context the action changes
// is the confirmation's own Context row, which the row's folder fills in;
// without one, the pending action's. The dialog always names it: with two
// panels in two contexts, it is the last guard against acting in the
// wrong one.
func (a *App) showConfirmation(c models.Confirmation, pending *pendingConfirmation) tea.Cmd {
	if kc := confirmationContext(c); kc != "" {
		pending.kubeContext = kc
	} else if pending.kubeContext != "" {
		c.Details = append([][2]string{{"Context", pending.kubeContext}}, c.Details...)
	}
	model := NewConfirmModel(c)
	// A confirmed action changes the cluster unless it says it is local.
	if !c.Local && a.policyFor(pending.kubeContext).ReadOnly {
		return a.readOnlyRefusal(pending.kubeContext, confirmationName(c))
	}
	modal := NewModal("Confirm", model)
	modal.SetDanger(true)
	a.pendingConfirm = pending
	a.modalManager.Register("confirm_action", modal)
	w, h := model.size()
	a.configureModalWindow(modal, model, a.activePanel, "", min(w+2, max(30, a.width-6)), h+2)
	a.modalManager.Show("confirm_action")
	a.invalidateView("confirm action")
	return nil
}

// handleConfirmResult closes the dialog and runs the action when approved.
func (a *App) handleConfirmResult(msg confirmResultMsg) tea.Cmd {
	pending := a.pendingConfirm
	a.pendingConfirm = nil
	a.hideModalName("confirm_action")
	if !msg.ok || pending == nil {
		return nil
	}
	if !pending.local() && a.policyFor(pending.kubeContext).ReadOnly {
		// The context became read-only while the dialog was open.
		return a.readOnlyRefusal(pending.kubeContext, pending.name())
	}
	if pending.cluster != nil {
		return a.runClusterAction(pending.cluster, pending.panelIdx)
	}
	if pending.row == nil {
		return nil
	}
	if err := pending.row.Confirm(); err != nil {
		return a.ShowToast("Failed: "+err.Error(), 5*time.Second)
	}
	if f, ok := pending.row.(models.ConfirmFollowUp); ok {
		next := f.FollowUp()
		if req, ok := next.(models.NodeShellRequest); ok {
			// The node shell runs in the cluster of the row that asked.
			return a.startNodeShell(pending.conn, req)
		}
		return func() tea.Msg { return next }
	}
	return nil
}

// confirmedActionName names a confirmed action in messages: its dialog
// title without the question mark ("Start a debug helper").
func confirmedActionName(action models.ConfirmedAction) string {
	return confirmationName(action.Confirmation())
}

// confirmationName names the action a confirmation asks for, for the
// read-only refusal.
func confirmationName(c models.Confirmation) string {
	if name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c.Title), "?")); name != "" {
		return name
	}
	return "This action"
}

// name names the pending action, for the read-only refusal.
// local reports whether the pending action changes only kc's own state.
func (p *pendingConfirmation) local() bool {
	return p.row != nil && p.row.Confirmation().Local
}

func (p *pendingConfirmation) name() string {
	switch {
	case p.row != nil:
		return confirmedActionName(p.row)
	case p.cluster != nil:
		return confirmationName(p.cluster.Confirmation())
	}
	return "This action"
}
