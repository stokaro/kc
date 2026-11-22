package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sttts/kc/internal/filexfer"
	"github.com/sttts/kc/internal/models"
	uistyles "github.com/sttts/kc/internal/ui/styles"
)

// transferChoiceMsg answers the copy dialog: start with policy, or not.
type transferChoiceMsg struct {
	ok     bool
	policy filexfer.Policy
}

// transferCancelMsg asks the running copy to stop.
type transferCancelMsg struct{}

// transferCloseMsg closes the dialog after the copy ended.
type transferCloseMsg struct{}

type transferPhase int

const (
	transferPhaseConfirm transferPhase = iota
	transferPhaseRunning
	transferPhaseDone
)

// transferMaxResultRows bounds the result list shown at once; up and down
// scroll the rest.
const transferMaxResultRows = 12

// TransferModel is the copy dialog. It shows where the files come from and
// where they go, then the progress, then one line per copied entry.
type TransferModel struct {
	phase     transferPhase
	details   [][2]string
	conflicts int
	// upload marks a copy into a container: a red dialog with Cancel
	// focused, the kubectl cp equivalent, and target for the refusal check.
	upload   bool
	commands []string
	target   models.ContainerFSSource
	// progress reads the running copy's counter.
	progress  func() filexfer.Progress
	canceling bool
	results   []filexfer.Result
	runErr    error
	scroll    int

	width, height int
	focus         int
	buttons       []buttonRect
}

// NewTransferModel builds the dialog for a copy of details, where conflicts
// entries exist at the destination already.
func NewTransferModel(details [][2]string, conflicts int) *TransferModel {
	return &TransferModel{details: details, conflicts: conflicts}
}

// NewUploadModel builds the dialog for a copy into the container dest.
// commands are the equivalent kubectl cp lines. Cancel has the focus, so
// Enter alone never writes into the container.
func NewUploadModel(details [][2]string, conflicts int, commands []string, dest models.ContainerFSSource) *TransferModel {
	m := &TransferModel{details: details, conflicts: conflicts, upload: true, commands: commands, target: dest}
	m.focus = len(m.labels()) - 1
	return m
}

func (m *TransferModel) Init() tea.Cmd          { return nil }
func (m *TransferModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// start switches the dialog to the progress view.
func (m *TransferModel) start(progress func() filexfer.Progress) {
	m.phase, m.progress, m.focus = transferPhaseRunning, progress, 0
}

// finish switches the dialog to the result view.
func (m *TransferModel) finish(results []filexfer.Result, err error) {
	m.phase, m.results, m.runErr, m.focus, m.scroll = transferPhaseDone, results, err, 0, 0
}

func (m *TransferModel) labels() []string {
	switch m.phase {
	case transferPhaseConfirm:
		if m.conflicts > 0 {
			return []string{"Skip existing", "Overwrite", "Cancel"}
		}
		return []string{"Copy", "Cancel"}
	case transferPhaseRunning:
		return []string{"Cancel"}
	default:
		return []string{"OK"}
	}
}

// press runs the button at index i.
func (m *TransferModel) press(i int) tea.Cmd {
	switch m.phase {
	case transferPhaseConfirm:
		labels := m.labels()
		switch labels[i] {
		case "Skip existing", "Copy":
			return transferMsg(transferChoiceMsg{ok: true, policy: filexfer.SkipExisting})
		case "Overwrite":
			return transferMsg(transferChoiceMsg{ok: true, policy: filexfer.Overwrite})
		}
		return transferMsg(transferChoiceMsg{ok: false})
	case transferPhaseRunning:
		return m.cancel()
	default:
		return transferMsg(transferCloseMsg{})
	}
}

func (m *TransferModel) cancel() tea.Cmd {
	if m.canceling {
		return nil
	}
	m.canceling = true
	return transferMsg(transferCancelMsg{})
}

func transferMsg(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// escape is what Esc and C-c do in each phase.
func (m *TransferModel) escape() tea.Cmd {
	switch m.phase {
	case transferPhaseConfirm:
		return transferMsg(transferChoiceMsg{ok: false})
	case transferPhaseRunning:
		return m.cancel()
	default:
		return transferMsg(transferCloseMsg{})
	}
}

// HandleModalEscape keeps the dialog open while a copy runs: Esc cancels
// the copy, and the dialog closes when the result is in.
func (m *TransferModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) { return true, m.escape() }

func (m *TransferModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		n := len(m.labels())
		switch msg.String() {
		case "ctrl+c", "ctrl+g":
			return m, m.escape()
		case "tab", "right":
			m.focus = (m.focus + 1) % n
		case "shift+tab", "left":
			m.focus = (m.focus + n - 1) % n
		case "up":
			if m.scroll > 0 {
				m.scroll--
			}
		case "down":
			if m.scroll < len(m.results)-transferMaxResultRows {
				m.scroll++
			}
		case "enter":
			return m, m.press(min(m.focus, n-1))
		}
	case tea.MouseReleaseMsg:
		mouse := msg.Mouse()
		for i, r := range m.buttons {
			if mouse.Button == tea.MouseLeft && r.contains(mouse.X, mouse.Y) {
				return m, m.press(i)
			}
		}
	}
	return m, nil
}

func (m *TransferModel) title() string {
	switch m.phase {
	case transferPhaseConfirm:
		if m.upload {
			return "Copy into the container?"
		}
		return "Copy from the container?"
	case transferPhaseRunning:
		if m.canceling {
			return "Canceling the copy..."
		}
		if m.upload {
			return "Copying into the container..."
		}
		return "Copying from the container..."
	}
	return "Copy finished: " + summarizeResults(m.results)
}

// lines renders the body without the buttons.
func (m *TransferModel) lines() []string {
	out := []string{" " + m.title(), ""}
	out = append(out, detailLines(m.details)...)
	switch m.phase {
	case transferPhaseConfirm:
		if m.conflicts > 0 {
			out = append(out, "", fmt.Sprintf(" %d of the entries exist in the destination.", m.conflicts),
				" Overwrite replaces files and merges directories.")
		}
		if m.upload {
			out = append(out, "", " Writes into the container. Symbolic links and special",
				" files are left out.")
			if len(m.commands) > 0 {
				out = append(out, "", " Same as:")
				for _, c := range m.commands {
					out = append(out, " "+c)
				}
			}
		}
	case transferPhaseRunning:
		var p filexfer.Progress
		if m.progress != nil {
			p = m.progress()
		}
		current := p.Current
		if p.Total > 0 {
			current = fmt.Sprintf("%s (%d of %d)", p.Current, p.Index, p.Total)
		}
		out = append(out, "")
		out = append(out, detailLines([][2]string{
			{"Entry", current},
			{"Files", fmt.Sprintf("%d", p.Files)},
			{"Bytes", formatTransferBytes(p.Bytes)},
		})...)
		out = append(out, "", " Esc or C-c cancels; nothing of the current entry is kept.")
	case transferPhaseDone:
		out = append(out, "")
		if m.runErr != nil {
			out = append(out, " "+m.runErr.Error())
		}
		rows := resultLines(m.results)
		end := min(len(rows), m.scroll+transferMaxResultRows)
		out = append(out, rows[min(m.scroll, end):end]...)
		if len(rows) > transferMaxResultRows {
			out = append(out, fmt.Sprintf(" (%d-%d of %d, up/down scroll)", m.scroll+1, end, len(rows)))
		}
	}
	return append(out, "")
}

func detailLines(details [][2]string) []string {
	labelW := 0
	for _, d := range details {
		labelW = max(labelW, len(d[0]))
	}
	out := make([]string, 0, len(details))
	for _, d := range details {
		out = append(out, " "+d[0]+":"+strings.Repeat(" ", labelW-len(d[0])+2)+d[1])
	}
	return out
}

// resultLines renders one line per entry, then its notes indented.
func resultLines(results []filexfer.Result) []string {
	nameW := 0
	for _, r := range results {
		nameW = max(nameW, lipgloss.Width(r.Name))
	}
	var out []string
	for _, r := range results {
		detail := r.Message
		if r.Status == filexfer.StatusCopied {
			detail = fmt.Sprintf("%d files, %s", r.Files, formatTransferBytes(r.Bytes))
			if len(r.Notes) > 0 {
				detail += fmt.Sprintf(", %d left out", len(r.Notes))
			}
		}
		out = append(out, fmt.Sprintf(" %-8s %s  %s", r.Status, r.Name+strings.Repeat(" ", nameW-lipgloss.Width(r.Name)), detail))
		for _, n := range r.Notes {
			out = append(out, "          "+n)
		}
	}
	return out
}

// summarizeResults counts the results by status, for the title and the log.
func summarizeResults(results []filexfer.Result) string {
	counts := map[filexfer.Status]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	var parts []string
	for _, s := range []filexfer.Status{filexfer.StatusCopied, filexfer.StatusSkipped, filexfer.StatusFailed, filexfer.StatusCanceled} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	if len(parts) == 0 {
		return "nothing copied"
	}
	return strings.Join(parts, ", ")
}

func formatTransferBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// size is the content size the dialog needs.
func (m *TransferModel) size() (int, int) {
	w := 0
	for _, l := range m.lines() {
		w = max(w, lipgloss.Width(l)+1)
	}
	return max(w, 44), len(m.lines()) + 1
}

func (m *TransferModel) View() tea.View {
	bg := lipgloss.Color(uistyles.ColorModalBg)
	fg := lipgloss.Color(uistyles.ColorModalFg)
	selBg := lipgloss.Color(uistyles.ColorModalButtonSelBg)
	selFg := fg
	if m.upload {
		bg, fg = lipgloss.Color(uistyles.ColorDangerBg), lipgloss.Color(uistyles.ColorDangerFg)
		selBg, selFg = fg, lipgloss.Color(uistyles.ColorBlack)
	}
	row := lipgloss.NewStyle().Background(bg).Foreground(fg).Width(m.width)
	var out []string
	for i, l := range m.lines() {
		style := row
		if i == 0 {
			style = style.Bold(true)
		}
		out = append(out, style.Render(truncateStringToWidth(l, m.width)))
	}
	labels := m.labels()
	normal := lipgloss.NewStyle().Background(bg).Foreground(fg)
	focused := lipgloss.NewStyle().Background(selBg).Foreground(selFg).Bold(true)
	rendered := make([]string, len(labels))
	total := 0
	for i, l := range labels {
		rendered[i] = "[ " + l + " ]"
		total += lipgloss.Width(rendered[i])
	}
	total += 2 * (len(labels) - 1)
	x := max(0, (m.width-total)/2)
	y := len(out)
	m.buttons = make([]buttonRect, len(labels))
	var b strings.Builder
	b.WriteString(normal.Render(strings.Repeat(" ", x)))
	for i, l := range rendered {
		style := normal
		if i == m.focus {
			style = focused
		}
		m.buttons[i] = buttonRect{x: x, y: y, w: lipgloss.Width(l), h: 1}
		b.WriteString(style.Render(l))
		x += lipgloss.Width(l)
		if i < len(rendered)-1 {
			b.WriteString(normal.Render("  "))
			x += 2
		}
	}
	out = append(out, row.Render(b.String()))
	return tea.NewView(strings.Join(out, "\n"))
}
