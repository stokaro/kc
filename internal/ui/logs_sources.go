package ui

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/ui/dialog"
)

// logsSourcesKey opens the sources dialog of a merged logs viewer (see
// docs/adr/0181-log-sources-and-coverage.md).
const logsSourcesKey = "alt+s"

// logsSourcesModalName registers the sources dialog with the modal manager.
const logsSourcesModalName = "logs_sources"

// logsSourcesMaxLimit bounds the stream limit the dialog accepts. Each
// stream is an open request to the API server and the kubelet.
const logsSourcesMaxLimit = 500

// logsSourcesMaxRows bounds the source rows the dialog lists.
const logsSourcesMaxRows = 12

// logSelection is the source filter and stream limit of a merged logs
// viewer. It outlives the merged stream: an F2 change starts a new stream
// with the same selection. The dialog writes it on the update goroutine;
// the stream is started in a tea.Cmd.
type logSelection struct {
	mu     sync.Mutex
	filter logmux.Filter
	limit  int
}

func (s *logSelection) get() (logmux.Filter, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filter, s.limit
}

func (s *logSelection) set(f logmux.Filter, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filter, s.limit = f, limit
}

// Coverage describes which sources the merged stream follows, for the Logs
// panel header.
func (s *workloadLogsSource) Coverage() string { return s.mux.Stats().String() }

// mergedMux returns the mux behind a merged stream, or nil for a single
// container log.
func mergedMux(r io.Reader) *logmux.Mux {
	if src, ok := r.(*workloadLogsSource); ok {
		return src.mux
	}
	return nil
}

// SetOnSources sets what M-s does in a merged log; the app opens the
// sources dialog.
func (v *LogsViewer) SetOnSources(fn func() tea.Cmd) { v.onSources = fn }

// Mux returns the mux of the current merged stream, or nil.
func (v *LogsViewer) Mux() *logmux.Mux { return mergedMux(v.reader) }

const (
	sourcesApplyID = "apply"
	sourcesRetryID = "retry"
	sourcesCloseID = "close"
)

// logsSourcesDialog shows the coverage of a merged log, one row per
// source, and edits the source filter and the stream limit. It reads the
// mux on every View, so it stays current while the log streams behind it.
type logsSourcesDialog struct {
	viewer *LogsViewer
	sel    *logSelection
	done   func()

	form   dialog.Form
	filter textinput.Model
	limit  textinput.Model
	note   string
	isErr  bool
	rows   int
	width  int
	rules  []int
}

func newLogsSourcesDialog(v *LogsViewer, sel *logSelection, done func()) *logsSourcesDialog {
	skin := dialog.Normal()
	f, limit := sel.get()
	d := &logsSourcesDialog{
		viewer: v,
		sel:    sel,
		done:   done,
		form:   dialog.Form{Skin: skin},
		filter: dialog.NewInput(skin, f.String()),
		limit:  dialog.NewInput(skin, strconv.Itoa(limit)),
		rows:   logsSourcesMaxRows,
	}
	d.filter.Placeholder = "-/istio-proxy api-*"
	d.filter.CursorEnd()
	d.syncItems()
	return d
}

func (d *logsSourcesDialog) syncItems() {
	mux := d.viewer.Mux()
	summary := "the stream is not open"
	var sources []logmux.Source
	if mux != nil {
		summary = mux.Stats().String()
		sources = mux.Sources()
	}
	items := []dialog.Item{
		dialog.Text{Text: summary, Bold: true},
		dialog.Rule{},
		dialog.Text{Text: "Sources: pod[/container] globs; -term excludes"},
		dialog.Input{Model: &d.filter},
		dialog.Text{Text: "Streams at once:"},
		dialog.Input{Model: &d.limit},
		dialog.Text{Text: d.note, Error: d.isErr},
		dialog.Rule{},
	}
	for _, row := range sourceRows(sources, d.rows, d.textWidth()) {
		items = append(items, dialog.Text{Text: row})
	}
	items = append(items,
		dialog.Rule{},
		dialog.Buttons{
			{ID: sourcesApplyID, Label: "&Apply", Default: true},
			{ID: sourcesRetryID, Label: "&Retry failed"},
			{ID: sourcesCloseID, Label: "&Close"},
		},
	)
	d.form.Items = items
}

// sourceStateWidth is the narrowest state column of the source rows; a
// longer state, such as "waiting (CrashLoopBackOff)", widens it.
const sourceStateWidth = 12

// sourceMaxLines bounds the lines one wrapped source row takes.
const sourceMaxLines = 3

// sourceRows lists the sources that need attention first (failed, then
// waiting containers, reconnecting, waiting for a slot, ended, excluded,
// streaming), in exactly n lines: the last line sums up the sources that
// do not fit, and blank lines pad a short list so the dialog keeps its
// size. A failed source shows its last error; a waiting container whose
// previous instance is shown says so. A row longer than width (when
// positive) wraps onto lines indented under the name.
func sourceRows(sources []logmux.Source, n, width int) []string {
	sorted := slices.Clone(sources)
	slices.SortStableFunc(sorted, func(a, b logmux.Source) int { return cmp.Compare(sourceRank(a.State), sourceRank(b.State)) })
	rows := make([]string, 0, n)
	if len(sorted) == 0 {
		rows = append(rows, "no sources yet")
	}
	states := sourceStateColumn(sorted)
	for i, s := range sorted {
		lines := wrapRow(sourceRow(s, states), width, states+1)
		lines = lines[:min(len(lines), sourceMaxLines)]
		// The last line is kept for the sum while more sources follow.
		room := n - len(rows)
		if left := len(sorted) - i; left > 1 {
			room--
		}
		if len(lines) <= room {
			rows = append(rows, lines...)
			continue
		}
		left := len(sorted) - i
		if len(rows) == 0 && room > 0 {
			// The first source shows what fits rather than nothing.
			rows = append(rows, lines[:room]...)
			left--
		}
		if left > 0 {
			rows = append(rows, fmt.Sprintf("... and %d more", left))
		}
		break
	}
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

// sourceStateColumn is the width of the state column for sources.
func sourceStateColumn(sources []logmux.Source) int {
	width := sourceStateWidth
	for _, s := range sources {
		width = max(width, len(s.Status()))
	}
	return width
}

// sourceRow is one source as one line: its state, name and detail.
func sourceRow(s logmux.Source, states int) string {
	row := fmt.Sprintf("%-*s %s", states, s.Status(), s.Target.Name())
	switch {
	case s.State == logmux.SourceFailed && s.Err != nil:
		row += ": " + s.Err.Error()
	case s.State == logmux.SourceContainerWaiting && s.Previous:
		row += ": previous instance shown"
	case s.State == logmux.SourceEnded && s.Done != "":
		row += ": no log, " + s.Done
	}
	return row
}

// wrapRow breaks a row wider than width terminal cells at spaces; the
// lines after the first start with indent spaces. A width too narrow to
// wrap into leaves the row whole, for the dialog to cut.
func wrapRow(row string, width, indent int) []string {
	if width <= 0 || ansi.StringWidth(row) <= width || width-indent < 20 {
		return []string{row}
	}
	r := []rune(row)
	var out []string
	pad, room := "", width
	for ansi.StringWidth(string(r)) > room {
		n := fitCells(r, room)
		cut := n
		if i := lastSpace(r[:min(n+1, len(r))]); i > n/2 {
			cut = i
		}
		out = append(out, pad+strings.TrimRight(string(r[:cut]), " "))
		r = []rune(strings.TrimLeft(string(r[cut:]), " "))
		pad, room = strings.Repeat(" ", indent), width-indent
	}
	return append(out, pad+string(r))
}

// fitCells is how many of the runes of r, at least one, fit in cells
// terminal cells; r is wider than cells.
func fitCells(r []rune, cells int) int {
	used := 0
	for i, c := range r {
		used += ansi.StringWidth(string(c))
		if used > cells {
			return max(i, 1)
		}
	}
	return len(r)
}

func lastSpace(r []rune) int {
	for i, c := range slices.Backward(r) {
		if c == ' ' {
			return i
		}
	}
	return -1
}

func sourceRank(s logmux.SourceState) int {
	switch s {
	case logmux.SourceFailed:
		return 0
	case logmux.SourceContainerWaiting:
		return 1
	case logmux.SourceReconnecting:
		return 2
	case logmux.SourceWaiting:
		return 3
	case logmux.SourceEnded:
		return 4
	case logmux.SourceExcluded:
		return 5
	}
	return 6
}

func (d *logsSourcesDialog) Init() tea.Cmd { return d.form.FocusFirst() }

func (d *logsSourcesDialog) SetDimensions(w, _ int) { d.width = w }

// logsSourcesMinWidth is the content width of the sources dialog; it
// widens, as far as the screen allows, to show long rows such as an error.
const logsSourcesMinWidth = 76

// PreferredSize implements ModalSizer. The dialog is as wide as its
// longest source row, within the screen; the source rows fit the sources,
// wrapped to that width, with room for a few new ones, within what the
// screen leaves and at most logsSourcesMaxRows.
func (d *logsSourcesDialog) PreferredSize(maxW, maxH int) (width, height int) {
	fixed := len(d.form.Items) - d.rows
	var sources []logmux.Source
	if mux := d.viewer.Mux(); mux != nil {
		sources = mux.Sources()
	}
	states := sourceStateColumn(sources)
	width = logsSourcesMinWidth
	for _, s := range sources {
		width = max(width, ansi.StringWidth(sourceRow(s, states))+2)
	}
	if maxW > 0 {
		width = min(width, maxW)
	}
	d.width = width
	lines := 0
	for _, s := range sources {
		lines += min(len(wrapRow(sourceRow(s, states), d.textWidth(), states+1)), sourceMaxLines)
	}
	d.rows = min(logsSourcesMaxRows, max(lines+2, 4))
	if maxH > 0 {
		d.rows = max(1, min(d.rows, maxH-fixed))
	}
	d.syncItems()
	return width, d.form.Height()
}

// textWidth is the width a row of text has in the dialog, or 0 before the
// dialog knows its width.
func (d *logsSourcesDialog) textWidth() int {
	if d.width <= 0 {
		return 0
	}
	return max(1, d.width-2)
}

// FooterHints implements ModalFooterHints.
func (d *logsSourcesDialog) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Enter", Label: "Apply", Enabled: true},
		{Key: "Tab", Label: "Next field", Enabled: true},
		{Key: "Esc", Label: "Close", Enabled: true},
	}
}

// RuleRows implements ModalRules.
func (d *logsSourcesDialog) RuleRows() []int { return d.rules }

func (d *logsSourcesDialog) View() tea.View {
	d.syncItems()
	content, cursor, rules := d.form.View(max(d.width, 20))
	d.rules = rules
	return viewWithCursor(content, cursor)
}

func (d *logsSourcesDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	d.syncItems()
	ev, cmd := d.form.Update(msg)
	if ev.Edited {
		d.note, d.isErr = "", false
	}
	switch ev.Pressed {
	case sourcesApplyID:
		if err := d.apply(); err != nil {
			d.note, d.isErr = err.Error(), true
			return d, nil
		}
		d.close()
	case sourcesRetryID:
		d.retry()
	case sourcesCloseID:
		d.close()
	}
	return d, cmd
}

func (d *logsSourcesDialog) close() {
	if d.done != nil {
		d.done()
	}
}

// apply checks both fields and hands the selection to the stream. Only
// what changed is applied, so the other streams go on as they are.
func (d *logsSourcesDialog) apply() error {
	f, err := logmux.ParseFilter(d.filter.Value())
	if err != nil {
		return fmt.Errorf("sources: %w", err)
	}
	limit, err := strconv.Atoi(strings.TrimSpace(d.limit.Value()))
	if err != nil || limit < 1 || limit > logsSourcesMaxLimit {
		return fmt.Errorf("streams at once: a number from 1 to %d", logsSourcesMaxLimit)
	}
	oldFilter, oldLimit := d.sel.get()
	d.sel.set(f, limit)
	d.viewer.refreshCommand()
	mux := d.viewer.Mux()
	if mux == nil {
		return nil // the next stream starts with the selection
	}
	if f.String() != oldFilter.String() {
		mux.SetFilter(f)
	}
	if limit != oldLimit {
		mux.SetMaxStreams(limit)
	}
	return nil
}

func (d *logsSourcesDialog) retry() {
	mux := d.viewer.Mux()
	if mux == nil {
		return
	}
	switch n := mux.Retry(); n {
	case 0:
		d.note = "no failed source to retry"
	default:
		d.note = fmt.Sprintf("retrying %d %s", n, plural(n, "source", "sources"))
	}
	d.isErr = false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// showLogsSourcesModal opens the sources dialog over a merged logs viewer.
// The viewer stays drawn, and keeps streaming, behind it.
func (a *App) showLogsSourcesModal(v *LogsViewer, sel *logSelection) tea.Cmd {
	if a.modalManager == nil || v == nil || sel == nil {
		return nil
	}
	behind := a.modalManager.GetActiveModal()
	d := newLogsSourcesDialog(v, sel, func() { a.hideModalName(logsSourcesModalName) })
	modal := NewModal("Log sources", d)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(logsSourcesModalName, modal)
	background := ""
	if behind != nil {
		background = viewString(behind.View())
	}
	a.configureModalWindow(modal, d, -1, background, 78, 24)
	if behind != nil {
		modal.SetWindowedBackgroundProvider(func() string { return viewString(behind.View()) })
	}
	a.showModal(logsSourcesModalName)
	return d.Init()
}
