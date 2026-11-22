package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/nodeaction"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// greyLine renders one line of an mc-style grey dialog. A focused line is
// drawn with the selection color.
func greyLine(text string, width int, focused bool) string {
	style := lipgloss.NewStyle().
		Background(lipgloss.Color(uistyles.ColorModalBg)).
		Foreground(lipgloss.Color(uistyles.ColorModalFg)).
		Width(width)
	if focused {
		style = style.Background(lipgloss.Color(uistyles.ColorModalSelBg)).Bold(true)
	}
	return style.Render(truncateStringToWidth(text, width))
}

func linesWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		w = max(w, lipgloss.Width(l))
	}
	return w
}

// nodeActionKind is one entry of the node action menu.
type nodeActionKind int

const (
	nodeActionCordon nodeActionKind = iota
	nodeActionUncordon
	nodeActionDrain
)

var nodeActionLabels = []struct {
	kind  nodeActionKind
	label string
}{
	{nodeActionCordon, "Cordon..."},
	{nodeActionUncordon, "Uncordon..."},
	{nodeActionDrain, "Drain..."},
}

// nodeActionMenuModel lists the node actions for the target nodes.
type nodeActionMenuModel struct {
	target        nodeTarget
	selected      int
	width, height int
}

func newNodeActionMenu(target nodeTarget) *nodeActionMenuModel {
	return &nodeActionMenuModel{target: target}
}

func (m *nodeActionMenuModel) Init() tea.Cmd          { return nil }
func (m *nodeActionMenuModel) SetDimensions(w, h int) { m.width, m.height = w, h }

func (m *nodeActionMenuModel) header() string {
	if len(m.target.nodes) == 1 {
		return " Node: " + m.target.nodes[0]
	}
	return fmt.Sprintf(" %d nodes: %s", len(m.target.nodes), strings.Join(m.target.nodes, ", "))
}

func (m *nodeActionMenuModel) lines() []string {
	out := []string{m.header(), ""}
	for _, e := range nodeActionLabels {
		out = append(out, "   "+e.label)
	}
	return out
}

func (m *nodeActionMenuModel) PreferredSize(maxW, maxH int) (width, height int) {
	return min(max(linesWidth(m.lines())+2, 28), maxW), min(len(m.lines()), maxH)
}

func (m *nodeActionMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k", "shift+tab":
		m.selected = (m.selected + len(nodeActionLabels) - 1) % len(nodeActionLabels)
	case "down", "j", "tab":
		m.selected = (m.selected + 1) % len(nodeActionLabels)
	case "enter":
		chosen := nodeMenuChosenMsg{kind: nodeActionLabels[m.selected].kind, target: m.target}
		return m, func() tea.Msg { return chosen }
	}
	return m, nil
}

func (m *nodeActionMenuModel) View() tea.View {
	w := max(m.width, 1)
	var out []string
	for i, l := range m.lines() {
		focused := i >= 2 && i-2 == m.selected
		if focused {
			l = " > " + strings.TrimPrefix(l, "   ")
		}
		out = append(out, greyLine(l, w, focused))
	}
	return tea.NewView(strings.Join(out, "\n"))
}

func (m *nodeActionMenuModel) FooterHints() []FooterHint {
	return []FooterHint{{Key: "Enter", Label: "Choose", Enabled: true}}
}

// Drain options form fields, in focus order.
const (
	drainFieldGrace = iota
	drainFieldTimeout
	drainFieldIgnoreDaemonSets
	drainFieldDeleteEmptyDir
	drainFieldForce
	drainFieldOK
	drainFieldCancel
	drainFieldCount
)

// drainOptionsModel asks for the kubectl drain options before kc reads the
// pods. Up/Down/Tab move, Space toggles a check box, Enter accepts (or
// cancels on Cancel).
type drainOptionsModel struct {
	target        nodeTarget
	grace         string
	timeout       string
	opts          nodeaction.DrainOptions
	focus         int
	err           string
	width, height int
}

func newDrainOptionsModel(target nodeTarget, opts nodeaction.DrainOptions) *drainOptionsModel {
	timeout := "0"
	if opts.Timeout > 0 {
		timeout = opts.Timeout.String()
	}
	return &drainOptionsModel{
		target:  target,
		grace:   strconv.Itoa(opts.GracePeriodSeconds),
		timeout: timeout,
		opts:    opts,
		focus:   drainFieldOK,
	}
}

func (m *drainOptionsModel) Init() tea.Cmd          { return nil }
func (m *drainOptionsModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// parse validates the text fields and returns the options.
func (m *drainOptionsModel) parse() (nodeaction.DrainOptions, error) {
	opts := m.opts
	grace, err := strconv.Atoi(strings.TrimSpace(m.grace))
	if err != nil || grace < -1 {
		return opts, fmt.Errorf("grace period: whole seconds, or -1 for each pod's own")
	}
	opts.GracePeriodSeconds = grace
	t := strings.TrimSpace(m.timeout)
	if t == "0" || t == "" {
		opts.Timeout = 0
	} else {
		d, err := time.ParseDuration(t)
		if err != nil || d < 0 {
			return opts, fmt.Errorf("timeout: a duration such as 90s or 5m, or 0 for none")
		}
		opts.Timeout = d
	}
	return opts, nil
}

func (m *drainOptionsModel) submit(ok bool) tea.Cmd {
	if !ok {
		msg := nodeDrainOptionsMsg{target: m.target}
		return func() tea.Msg { return msg }
	}
	opts, err := m.parse()
	if err != nil {
		m.err = err.Error()
		return nil
	}
	msg := nodeDrainOptionsMsg{target: m.target, opts: opts, ok: true}
	return func() tea.Msg { return msg }
}

func (m *drainOptionsModel) toggle() {
	switch m.focus {
	case drainFieldIgnoreDaemonSets:
		m.opts.IgnoreDaemonSets = !m.opts.IgnoreDaemonSets
	case drainFieldDeleteEmptyDir:
		m.opts.DeleteEmptyDirData = !m.opts.DeleteEmptyDirData
	case drainFieldForce:
		m.opts.Force = !m.opts.Force
	}
}

func (m *drainOptionsModel) field() *string {
	switch m.focus {
	case drainFieldGrace:
		return &m.grace
	case drainFieldTimeout:
		return &m.timeout
	}
	return nil
}

func (m *drainOptionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch s := key.String(); s {
	case "up", "shift+tab":
		m.focus = (m.focus + drainFieldCount - 1) % drainFieldCount
	case "down", "tab":
		m.focus = (m.focus + 1) % drainFieldCount
	case "left", "right":
		switch m.focus {
		case drainFieldOK:
			m.focus = drainFieldCancel
		case drainFieldCancel:
			m.focus = drainFieldOK
		}
	case "space", " ":
		m.toggle()
	case "enter":
		return m, m.submit(m.focus != drainFieldCancel)
	case "backspace":
		if f := m.field(); f != nil && len(*f) > 0 {
			*f = (*f)[:len(*f)-1]
			m.err = ""
		}
	default:
		if f := m.field(); f != nil && key.Text != "" {
			*f += key.Text
			m.err = ""
		}
	}
	return m, nil
}

func checkbox(on bool) string {
	if on {
		return "[x]"
	}
	return "[ ]"
}

// lines renders the form; focusLine is the line of the focused field.
func (m *drainOptionsModel) lines() (out []string, focusLine int) {
	nodes := m.target.nodes
	head := " Drain node " + strings.Join(nodes, ", ")
	if len(nodes) > 1 {
		head = fmt.Sprintf(" Drain %d nodes: %s", len(nodes), strings.Join(nodes, ", "))
	}
	out = []string{head, ""}
	fields := []string{
		" Grace period (s, -1 = pod's own): " + m.grace,
		" Timeout per node (0 = none):      " + m.timeout,
		" " + checkbox(m.opts.IgnoreDaemonSets) + " Ignore DaemonSet pods",
		" " + checkbox(m.opts.DeleteEmptyDirData) + " Delete emptyDir data",
		" " + checkbox(m.opts.Force) + " Force: evict pods without a controller",
	}
	focusLine = -1
	for i, f := range fields {
		if i == m.focus {
			focusLine = len(out)
		}
		out = append(out, f)
	}
	out = append(out, "")
	if m.err != "" {
		out = append(out, " ! "+m.err)
	} else {
		out = append(out, " Space toggles, Enter drains.")
	}
	return out, focusLine
}

func (m *drainOptionsModel) PreferredSize(maxW, maxH int) (width, height int) {
	lines, _ := m.lines()
	return min(max(linesWidth(lines)+12, 50), maxW), min(len(lines)+2, maxH)
}

func (m *drainOptionsModel) View() tea.View {
	w := max(m.width, 1)
	lines, focusLine := m.lines()
	var out []string
	for i, l := range lines {
		out = append(out, greyLine(l, w, i == focusLine))
	}
	out = append(out, greyLine("", w, false), m.buttons(w))
	return tea.NewView(strings.Join(out, "\n"))
}

func (m *drainOptionsModel) buttons(width int) string {
	return renderGreyButtons(width, []string{"[ Drain ]", "[ Cancel ]"}, m.focus-drainFieldOK)
}

// renderGreyButtons centers buttons on a grey line; focused is the index of
// the focused button, or -1.
func renderGreyButtons(width int, labels []string, focused int) string {
	normal := lipgloss.NewStyle().
		Background(lipgloss.Color(uistyles.ColorModalBg)).
		Foreground(lipgloss.Color(uistyles.ColorModalFg))
	sel := lipgloss.NewStyle().
		Background(lipgloss.Color(uistyles.ColorModalButtonSelBg)).
		Foreground(lipgloss.Color(uistyles.ColorModalFg)).
		Bold(true)
	total := 0
	for _, l := range labels {
		total += lipgloss.Width(l) + 2
	}
	var b strings.Builder
	b.WriteString(normal.Render(strings.Repeat(" ", max(0, (width-total)/2))))
	for i, l := range labels {
		style := normal
		if i == focused {
			style = sel
		}
		b.WriteString(style.Render(l))
		b.WriteString(normal.Render("  "))
	}
	return lipgloss.NewStyle().Background(lipgloss.Color(uistyles.ColorModalBg)).Width(width).Render(b.String())
}

func (m *drainOptionsModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Space", Label: "Toggle", Enabled: true},
		{Key: "Enter", Label: "Drain", Enabled: true},
	}
}

// maxBlockedLines bounds how many PDB-blocked pods the progress dialog
// lists.
const maxBlockedLines = 5

// drainDialogWidth is the width the progress dialog opens with when the
// screen has room. Refusal messages arrive after the dialog opened and are
// long, so it does not size itself to the first lines. Lines still wrap
// on a narrower screen.
const drainDialogWidth = 100

// drainProgressModel shows a running drain: per node its phase, evicted,
// remaining and PDB-blocked pods. Esc or Enter cancels while it runs and
// closes once it is done.
type drainProgressModel struct {
	nodes         []string
	cancel        func()
	progress      nodeaction.Progress
	canceling     bool
	done          bool
	result        string
	failed        bool
	width, height int
}

func newDrainProgressModel(nodes []string, cancel func()) *drainProgressModel {
	p := nodeaction.Progress{}
	for _, n := range nodes {
		p.Nodes = append(p.Nodes, nodeaction.NodeProgress{Node: n, Phase: nodeaction.PhaseWaiting})
	}
	return &drainProgressModel{nodes: nodes, cancel: cancel, progress: p}
}

func (m *drainProgressModel) setProgress(p nodeaction.Progress) { m.progress = p }

func (m *drainProgressModel) finish(res nodeaction.Result, err error) {
	m.done = true
	if err != nil {
		m.failed = true
		m.result = err.Error()
		return
	}
	m.result = res.Message
}

func (m *drainProgressModel) Init() tea.Cmd          { return nil }
func (m *drainProgressModel) SetDimensions(w, h int) { m.width, m.height = w, h }

func (m *drainProgressModel) stop() tea.Cmd {
	if m.done {
		return func() tea.Msg { return nodeDrainCloseMsg{} }
	}
	if !m.canceling {
		m.canceling = true
		m.cancel()
	}
	return nil
}

// HandleModalEscape cancels a running drain and keeps the dialog open
// until the drain has stopped; once it is done, Esc closes the dialog.
func (m *drainProgressModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) {
	if m.done {
		return false, nil
	}
	return true, m.stop()
}

func (m *drainProgressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" {
		return m, m.stop()
	}
	return m, nil
}

func (m *drainProgressModel) lines() []string {
	nameW := 4
	for _, n := range m.progress.Nodes {
		nameW = max(nameW, len(n.Node))
	}
	out := []string{fmt.Sprintf(" %-*s  %-9s %7s %9s %7s", nameW, "Node", "Phase", "Evicted", "Remaining", "Blocked"), ""}
	var blocked []string
	for _, n := range m.progress.Nodes {
		out = append(out, fmt.Sprintf(" %-*s  %-9s %7d %9d %7d", nameW, n.Node, n.Phase, n.Evicted, n.Remaining, len(n.Blocked)))
		for _, b := range n.Blocked {
			blocked = append(blocked, "   "+b.String())
		}
	}
	out = append(out, "")
	if len(blocked) > 0 {
		out = append(out, " Refused (usually by a PodDisruptionBudget), retried every "+nodeaction.DefaultRetryDelay.String()+":")
		if len(blocked) > maxBlockedLines {
			blocked = append(blocked[:maxBlockedLines], fmt.Sprintf("   ... and %d more", len(blocked)-maxBlockedLines))
		}
		out = append(out, blocked...)
		out = append(out, "")
	}
	switch {
	case m.done && m.failed:
		out = append(out, " ! "+m.result)
	case m.done:
		out = append(out, " "+m.result)
	case m.canceling:
		out = append(out, " Canceling; the nodes stay cordoned.")
	default:
		out = append(out, " Esc cancels; the nodes stay cordoned.")
	}
	return out
}

func (m *drainProgressModel) PreferredSize(maxW, maxH int) (width, height int) {
	// Room for the blocked list, so the window does not grow while the
	// drain runs.
	// A refusal usually wraps once, so each blocked pod gets two lines.
	h := 2 + len(m.nodes) + 1 + 1 + 2*maxBlockedLines + 2 + 1 + 2
	return min(max(linesWidth(m.lines())+2, drainDialogWidth), maxW), min(h, maxH)
}

// wrapDialogLine wraps line to width. The pieces after the first are
// indented two more columns than the line, so a wrapped refusal stays
// readable under its pod name.
func wrapDialogLine(line string, width int) []string {
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	text := strings.TrimLeft(line, " ")
	lead := line[:len(line)-len(text)]
	cont := lead + "  "
	parts := strings.Split(ansi.Wrap(text, max(1, width-len(cont)), ""), "\n")
	out := []string{lead + parts[0]}
	for _, p := range parts[1:] {
		out = append(out, cont+p)
	}
	return out
}

func (m *drainProgressModel) View() tea.View {
	w := max(m.width, 1)
	lines := m.lines()
	var out []string
	var wrapped []string
	for _, l := range lines[:len(lines)-1] {
		wrapped = append(wrapped, wrapDialogLine(l, w)...)
	}
	// The last line says how the drain ended or how to cancel; it stays
	// visible when the rest does not fit.
	status := wrapDialogLine(lines[len(lines)-1], w)
	body := max(1, m.height-2)
	if keep := max(0, body-len(status)); len(wrapped) > keep {
		wrapped = wrapped[:keep]
	}
	for _, l := range append(wrapped, status...) {
		out = append(out, greyLine(l, w, false))
	}
	for len(out) < body {
		out = append(out, greyLine("", w, false))
	}
	if len(out) > body {
		out = out[:body]
	}
	label := "[ Cancel ]"
	if m.done {
		label = "[ Close ]"
	}
	out = append(out, greyLine("", w, false), renderGreyButtons(w, []string{label}, 0))
	return tea.NewView(strings.Join(out, "\n"))
}

func (m *drainProgressModel) FooterHints() []FooterHint {
	if m.done {
		return []FooterHint{{Key: "Enter", Label: "Close", Enabled: true}}
	}
	return []FooterHint{{Key: "Esc", Label: "Cancel drain", Enabled: true}}
}
