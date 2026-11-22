package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/filexfer"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/dialog"
)

// transferChoiceMsg answers the copy dialog: start with policy, or not.
// dest is the local target directory of a copy from a container, as the
// user left it in the "to:" line; it is empty for a copy into one.
type transferChoiceMsg struct {
	ok     bool
	policy filexfer.Policy
	dest   string
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

// Button IDs of the copy dialog.
const (
	transferOK     = "ok"
	transferCancel = "cancel"
)

// TransferModel is the copy dialog, laid out like mc's Copy dialog: what
// is copied, the "to:" target, the overwrite option and the buttons. Then
// it shows the progress, then one line per copied entry.
type TransferModel struct {
	phase   transferPhase
	details [][2]string
	// dest is the "to:" line of a copy from a container; conflictsAt
	// counts the entries that exist in a directory, to keep the note
	// under it current while the user edits it.
	dest        textinput.Model
	conflictsAt func(dir string) int
	conflicts   int
	overwrite   bool
	errMsg      string
	// upload marks a copy into a container: a red dialog with Cancel
	// as the default button, the kubectl cp equivalent, and target for
	// the refusal check. Its target directory is fixed.
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
	form          dialog.Form
	rules         []int
}

// NewTransferModel builds the dialog for a copy of details into the local
// directory dest. conflictsAt, when set, counts the entries that already
// exist in a directory.
func NewTransferModel(details [][2]string, dest string, conflictsAt func(string) int) *TransferModel {
	skin := dialog.Normal()
	m := &TransferModel{details: details, conflictsAt: conflictsAt, form: dialog.Form{Skin: skin}}
	m.dest = dialog.NewInput(skin, dest)
	m.recount()
	m.syncItems()
	m.form.FocusFirst()
	return m
}

// NewUploadModel builds the dialog for a copy into the container dest.
// commands are the equivalent kubectl cp lines. Cancel is the default
// button and has the focus, so Enter alone never writes into the container.
func NewUploadModel(details [][2]string, conflicts int, commands []string, dest models.ContainerFSSource) *TransferModel {
	m := &TransferModel{
		details: details, conflicts: conflicts, upload: true, commands: commands, target: dest,
		form: dialog.Form{Skin: dialog.Danger(), AltOnly: true},
	}
	m.syncItems()
	m.form.FocusButton(transferCancel)
	return m
}

func (m *TransferModel) Init() tea.Cmd          { return nil }
func (m *TransferModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// RuleRows implements ModalRules.
func (m *TransferModel) RuleRows() []int { return m.rules }

// start switches the dialog to the progress view.
func (m *TransferModel) start(progress func() filexfer.Progress) {
	m.phase, m.progress = transferPhaseRunning, progress
	m.dest.Blur()
	m.syncItems()
	m.form.FocusFirst()
}

// finish switches the dialog to the result view.
func (m *TransferModel) finish(results []filexfer.Result, err error) {
	m.phase, m.results, m.runErr, m.scroll = transferPhaseDone, results, err, 0
	m.syncItems()
	m.form.FocusFirst()
}

func (m *TransferModel) recount() {
	if m.conflictsAt != nil {
		m.conflicts = m.conflictsAt(m.destDir())
	}
}

// destDir is the "to:" line as an absolute directory.
func (m *TransferModel) destDir() string {
	dir := expandedPath(m.dest.Value())
	if abs, err := filepath.Abs(dir); err == nil && dir != "" {
		return abs
	}
	return dir
}

func (m *TransferModel) policy() filexfer.Policy {
	if m.overwrite {
		return filexfer.Overwrite
	}
	return filexfer.SkipExisting
}

// press runs the button id.
func (m *TransferModel) press(id string) tea.Cmd {
	switch m.phase {
	case transferPhaseConfirm:
		if id != transferOK {
			return transferMsg(transferChoiceMsg{ok: false})
		}
		if m.upload {
			return transferMsg(transferChoiceMsg{ok: true, policy: m.policy()})
		}
		dir := m.destDir()
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			m.errMsg = "Not a local directory: " + dir
			return nil
		}
		return transferMsg(transferChoiceMsg{ok: true, policy: m.policy(), dest: dir})
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
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+c", "ctrl+g":
			return m, m.escape()
		case "up", "down":
			if m.phase == transferPhaseDone {
				m.scrollResults(key.String() == "down")
				return m, nil
			}
		}
	}
	m.syncItems()
	ev, cmd := m.form.Update(msg)
	if ev.Edited {
		m.errMsg = ""
		m.recount()
	}
	if ev.Pressed != "" {
		return m, m.press(ev.Pressed)
	}
	return m, cmd
}

func (m *TransferModel) scrollResults(down bool) {
	if down && m.scroll < len(resultLines(m.results))-transferMaxResultRows {
		m.scroll++
	}
	if !down && m.scroll > 0 {
		m.scroll--
	}
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

// syncItems rebuilds the form for the current phase.
func (m *TransferModel) syncItems() {
	items := []dialog.Item{dialog.Text{Text: m.title(), Bold: true}, dialog.Text{}}
	text := func(lines ...string) {
		for _, l := range lines {
			items = append(items, dialog.Text{Text: l})
		}
	}
	text(detailLines(m.details)...)
	switch m.phase {
	case transferPhaseConfirm:
		items = m.confirmItems(items)
	case transferPhaseRunning:
		var p filexfer.Progress
		if m.progress != nil {
			p = m.progress()
		}
		current := p.Current
		if p.Total > 0 {
			current = fmt.Sprintf("%s (%d of %d)", p.Current, p.Index, p.Total)
		}
		text("")
		text(detailLines([][2]string{
			{"Entry", current},
			{"Files", fmt.Sprintf("%d", p.Files)},
			{"Bytes", formatTransferBytes(p.Bytes)},
		})...)
		text("", "Esc or C-c cancels; nothing of the current entry is kept.")
		items = append(items, dialog.Rule{}, dialog.Buttons{{ID: transferCancel, Label: "&Cancel", Default: true}})
	case transferPhaseDone:
		text("")
		if m.runErr != nil {
			items = append(items, dialog.Text{Text: m.runErr.Error(), Error: true})
		}
		rows := resultLines(m.results)
		end := min(len(rows), m.scroll+transferMaxResultRows)
		text(rows[min(m.scroll, end):end]...)
		if len(rows) > transferMaxResultRows {
			text(fmt.Sprintf("(%d-%d of %d, up/down scroll)", m.scroll+1, end, len(rows)))
		}
		items = append(items, dialog.Rule{}, dialog.Buttons{{ID: transferOK, Label: "&OK", Default: true}})
	}
	m.form.Items = items
}

func (m *TransferModel) confirmItems(items []dialog.Item) []dialog.Item {
	text := func(lines ...string) {
		for _, l := range lines {
			items = append(items, dialog.Text{Text: l})
		}
	}
	note := ""
	if m.conflicts > 0 {
		note = fmt.Sprintf("%d of the entries exist in the destination.", m.conflicts)
	}
	if m.upload {
		text("", "Writes into the container. Symbolic links and special",
			"files are left out.")
		if len(m.commands) > 0 {
			text("", "Same as:")
			text(m.commands...)
		}
		if note != "" {
			// The target is fixed, so the option only shows when it matters.
			text("", note)
			items = append(items, dialog.Rule{},
				dialog.Check{ID: "overwrite", Label: "Over&write existing entries", On: &m.overwrite})
		}
		return append(items, dialog.Rule{}, dialog.Buttons{
			{ID: transferOK, Label: "&OK"},
			{ID: transferCancel, Label: "&Cancel", Default: true},
		})
	}
	text("to:")
	items = append(items, dialog.Input{Model: &m.dest})
	if m.errMsg != "" {
		items = append(items, dialog.Text{Text: m.errMsg, Error: true})
	} else {
		text(note)
	}
	return append(items,
		dialog.Rule{},
		dialog.Check{ID: "overwrite", Label: "Over&write existing entries", On: &m.overwrite},
		dialog.Rule{},
		dialog.Buttons{
			{ID: transferOK, Label: "&OK", Default: true},
			{ID: transferCancel, Label: "&Cancel"},
		})
}

// lines is the dialog's text, for tests and the size.
func (m *TransferModel) lines() []string {
	m.syncItems()
	content, _, _ := m.form.View(max(m.width, 40))
	return strings.Split(ansi.Strip(content), "\n")
}

func detailLines(details [][2]string) []string {
	labelW := 0
	for _, d := range details {
		labelW = max(labelW, len(d[0]))
	}
	out := make([]string, 0, len(details))
	for _, d := range details {
		out = append(out, d[0]+":"+strings.Repeat(" ", labelW-len(d[0])+2)+d[1])
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
		out = append(out, fmt.Sprintf("%-8s %s  %s", r.Status, r.Name+strings.Repeat(" ", nameW-lipgloss.Width(r.Name)), detail))
		for _, n := range r.Notes {
			out = append(out, "         "+n)
		}
	}
	return out
}

// summarizeResults counts the results by status, for the title and the log.
func summarizeResults(results []filexfer.Result) string {
	counts := make(map[filexfer.Status]int)
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
func (m *TransferModel) size() (width, height int) {
	m.syncItems()
	return max(m.form.Width(lipgloss.Width(m.dest.Value())+4), 50), m.form.Height()
}

func (m *TransferModel) View() tea.View {
	m.syncItems()
	content, cursor, rules := m.form.View(max(m.width, 1))
	m.rules = rules
	return viewWithCursor(content, cursor)
}
