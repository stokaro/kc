package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/access"
	"github.com/sttts/kc/internal/ui/styles"
)

// accessViewKey opens the access view for the selected row. Esc k is the
// fallback when the terminal does not send Meta.
const accessViewKey = "alt+k"

const accessViewModalName = "access_view"

// accessViewModel lists what the current identity may do with one target.
// It reads answers from the cache on every render, so results that arrive
// while the dialog is open show up without extra wiring.
type accessViewModel struct {
	target   access.Target
	identity access.Identity
	policy   access.Policy
	checks   []access.Check
	cache    *access.Cache

	selected int
	scroll   int
	width    int
	height   int
	status   string

	onCopy    func(text string) tea.Cmd
	onRefresh func() tea.Cmd
	onClose   func() tea.Cmd
}

func newAccessViewModel(target access.Target, id access.Identity, policy access.Policy, cache *access.Cache) *accessViewModel {
	return &accessViewModel{
		target:   target,
		identity: id,
		policy:   policy,
		checks:   access.ViewChecks(target),
		cache:    cache,
	}
}

func (m *accessViewModel) Init() tea.Cmd { return nil }

func (m *accessViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case copyResultMsg:
		m.status = msg.text
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "f10":
			if m.onClose != nil {
				return m, m.onClose()
			}
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "home":
			m.move(-len(m.checks))
		case "end":
			m.move(len(m.checks))
		case "enter":
			if m.onCopy != nil && m.selected < len(m.checks) {
				return m, m.onCopy(m.CanICommand())
			}
		case "r", "ctrl+r":
			m.status = ""
			if m.onRefresh != nil {
				return m, m.onRefresh()
			}
		}
	}
	return m, nil
}

func (m *accessViewModel) move(delta int) {
	if len(m.checks) == 0 {
		return
	}
	m.selected = max(0, min(len(m.checks)-1, m.selected+delta))
	visible := m.listHeight()
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	if m.selected >= m.scroll+visible {
		m.scroll = m.selected - visible + 1
	}
}

// CanICommand is the kubectl command for the selected row.
func (m *accessViewModel) CanICommand() string {
	if m.selected < 0 || m.selected >= len(m.checks) {
		return ""
	}
	return access.CanICommand(m.identity, m.checks[m.selected])
}

// headerLines and footerLines frame the list of checks: the footer is a
// separator, two lines of reason, two of command and a status line.
const (
	accessHeaderLines = 3
	accessFooterLines = 6
	accessWrapLines   = 2
)

func (m *accessViewModel) listHeight() int {
	h := m.height - accessHeaderLines - accessFooterLines
	if h < 1 {
		return 1
	}
	return h
}

// rowState renders the result column. A missing answer is "checking"; the
// view never shows a guess.
func (m *accessViewModel) rowState(c access.Check) (string, access.Result, bool) {
	res, ok := m.cache.Lookup(m.identity, c)
	if !ok {
		return "checking", res, false
	}
	label := res.Decision.String()
	if res.Stale {
		label += " (stale)"
	}
	return label, res, true
}

func (m *accessViewModel) View() tea.View {
	width := max(20, m.width)
	base := lipgloss.NewStyle().
		Background(lipgloss.Color(styles.ColorModalBg)).
		Foreground(lipgloss.Color(styles.ColorModalFg))
	bold := base.Bold(true)
	focus := lipgloss.NewStyle().
		Background(lipgloss.Color(styles.ColorModalSelBg)).
		Foreground(lipgloss.Color(styles.ColorModalFg)).
		Bold(true)
	line := func(st lipgloss.Style, s string) string {
		return st.Width(width).Render(truncateStringToWidth(s, width))
	}

	resWidth := len("RESOURCE")
	for _, c := range m.checks {
		resWidth = max(resWidth, lipgloss.Width(m.resourceLabel(c)))
	}
	resWidth = min(resWidth, max(8, width-30))

	var out []string
	out = append(out, line(base, "Context: "+m.identityLabel()))
	out = append(out, line(base, "Target:  "+m.targetLabel()))
	if m.policy.ReadOnly {
		out = append(out, line(bold, "Read-only mode: writes stay disabled whatever RBAC allows"))
	} else {
		out = append(out, line(bold, fmt.Sprintf("  %-8s %-*s %s", "VERB", resWidth, "RESOURCE", "RESULT")))
	}

	visible := m.listHeight()
	end := min(len(m.checks), m.scroll+visible)
	for i := m.scroll; i < end; i++ {
		c := m.checks[i]
		state, _, _ := m.rowState(c)
		res := truncateStringToWidth(m.resourceLabel(c), resWidth)
		cursor := "  "
		st := base
		if i == m.selected {
			cursor = "> "
			st = focus
		}
		out = append(out, line(st, fmt.Sprintf("%s%-8s %-*s %s", cursor, c.Verb, resWidth, res, state)))
	}
	for i := end - m.scroll; i < visible; i++ {
		out = append(out, line(base, ""))
	}

	reason := ""
	if m.selected < len(m.checks) {
		_, res, ok := m.rowState(m.checks[m.selected])
		switch {
		case !ok:
			reason = "waiting for the access review"
		case res.Reason != "":
			reason = res.Reason
		}
		if ok {
			reason = fmt.Sprintf("%s (checked %s)", reason, res.CheckedAt.Format(time.TimeOnly))
		}
	}
	out = append(out, line(base, strings.Repeat("─", width)))
	for _, l := range wrapLines("Reason: "+reason, width, accessWrapLines) {
		out = append(out, line(base, l))
	}
	for _, l := range wrapLines("$ "+m.CanICommand(), width, accessWrapLines) {
		out = append(out, line(base, l))
	}
	out = append(out, line(base, m.status))
	return tea.NewView(strings.Join(out, "\n"))
}

func (m *accessViewModel) resourceLabel(c access.Check) string {
	res := c.ResourceString()
	if c.Name == "" && m.target.Name != "" {
		res += " (any)"
	}
	return res
}

// wrapLines wraps s to width and returns exactly n lines; text that does
// not fit ends with an ellipsis (Enter still copies the full command).
func wrapLines(s string, width, n int) []string {
	lines := strings.Split(ansi.Wrap(s, width, ""), "\n")
	if len(lines) > n {
		last := truncateStringToWidth(lines[n-1], max(1, width-1))
		lines = append(lines[:n-1], last+"…")
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}

func (m *accessViewModel) identityLabel() string {
	id := m.identity
	ctx := id.Context
	if ctx == "" {
		ctx = "(none)"
	}
	if !id.Impersonated() {
		return ctx + " as the kubeconfig user"
	}
	var parts []string
	if id.User != "" {
		parts = append(parts, "user "+id.User)
	}
	if id.UID != "" {
		parts = append(parts, "uid "+id.UID)
	}
	if len(id.Groups) > 0 {
		parts = append(parts, "groups "+strings.Join(id.Groups, ","))
	}
	return ctx + " impersonating " + strings.Join(parts, ", ")
}

func (m *accessViewModel) targetLabel() string {
	t := m.target
	res := access.Check{Resource: t.Resource}.ResourceString()
	if t.Name != "" {
		res += "/" + t.Name
	} else {
		res += " (any object)"
	}
	if t.Namespace == "" {
		return res + ", cluster-wide or all namespaces"
	}
	return res + " in namespace " + t.Namespace
}

func (m *accessViewModel) SetDimensions(w, h int) {
	m.width = w
	m.height = h
	m.move(0)
}

// PreferredSize implements ModalSizer.
func (m *accessViewModel) PreferredSize(maxContentWidth, maxContentHeight int) (int, int) {
	return min(maxContentWidth, 96), min(maxContentHeight, len(m.checks)+accessHeaderLines+accessFooterLines)
}

// FooterHints implements ModalFooterHints.
func (m *accessViewModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Up/Down", Label: "Move", Enabled: true},
		{Key: "Enter", Label: "Copy can-i", Enabled: true},
		{Key: "r", Label: "Recheck", Enabled: true},
		{Key: "Esc", Label: "Close", Enabled: true},
	}
}

// showAccessView opens the access view for the active panel's selected row
// and starts the reviews it needs.
func (a *App) showAccessView() tea.Cmd {
	panel := a.activePanelRef()
	if panel == nil || a.modalManager == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, _ := panel.SelectedNavItem(ctx)
	cancel()
	target, ok := accessTargetForItem(item)
	if !ok {
		return a.ShowToast("Access: select an object or a resource type", 3*time.Second)
	}
	st := a.accessState()
	id := a.accessIdentity()
	view := newAccessViewModel(target, id, a.accessPolicy(), st.cache)
	run := func(force bool) tea.Cmd {
		reviewer := a.accessReviewer()
		if reviewer == nil {
			return nil
		}
		if force {
			st.cache.Forget(id, view.checks...)
		}
		return a.runAccessChecks(reviewer, id, view.checks)
	}
	view.onCopy = func(text string) tea.Cmd { return a.copyText(text, "the can-i command") }
	view.onRefresh = func() tea.Cmd { return run(true) }
	view.onClose = func() tea.Cmd {
		a.hideModal()
		return nil
	}

	modal := NewModal("Access", view)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(accessViewModalName, modal)
	a.configureModalWindow(modal, view, a.panelIndex(panel), "", 96, len(view.checks)+accessHeaderLines+accessFooterLines+2)
	a.showModal(accessViewModalName)
	return run(false)
}
