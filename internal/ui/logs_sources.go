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
	for _, row := range sourceRows(sources, d.rows) {
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

// sourceRows lists the sources that need attention first (failed, then
// reconnecting, waiting, ended, excluded, streaming), in exactly n rows:
// the last row sums up the sources that do not fit, and blank rows pad a
// short list so the dialog keeps its size.
func sourceRows(sources []logmux.Source, n int) []string {
	sorted := slices.Clone(sources)
	slices.SortStableFunc(sorted, func(a, b logmux.Source) int { return cmp.Compare(sourceRank(a.State), sourceRank(b.State)) })
	rows := make([]string, 0, n)
	if len(sorted) == 0 {
		rows = append(rows, "no sources yet")
	}
	for i, s := range sorted {
		if len(rows) == n-1 && len(sorted)-i > 1 {
			rows = append(rows, fmt.Sprintf("... and %d more", len(sorted)-i))
			break
		}
		row := fmt.Sprintf("%-12s %s", s.State, s.Target.Name())
		if s.Err != nil {
			row += ": " + s.Err.Error()
		}
		rows = append(rows, row)
	}
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

func sourceRank(s logmux.SourceState) int {
	switch s {
	case logmux.SourceFailed:
		return 0
	case logmux.SourceReconnecting:
		return 1
	case logmux.SourceWaiting:
		return 2
	case logmux.SourceEnded:
		return 3
	case logmux.SourceExcluded:
		return 4
	}
	return 5
}

func (d *logsSourcesDialog) Init() tea.Cmd { return d.form.FocusFirst() }

func (d *logsSourcesDialog) SetDimensions(w, _ int) { d.width = w }

// PreferredSize implements ModalSizer. The source rows fit the sources
// with room for a few new ones, within what the screen leaves and at most
// logsSourcesMaxRows.
func (d *logsSourcesDialog) PreferredSize(maxW, maxH int) (width, height int) {
	fixed := len(d.form.Items) - d.rows
	sources := 0
	if mux := d.viewer.Mux(); mux != nil {
		sources = len(mux.Sources())
	}
	d.rows = min(logsSourcesMaxRows, max(sources+2, 4))
	if maxH > 0 {
		d.rows = max(1, min(d.rows, maxH-fixed))
	}
	d.syncItems()
	width = 76
	if maxW > 0 {
		width = min(width, maxW)
	}
	return width, d.form.Height()
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
