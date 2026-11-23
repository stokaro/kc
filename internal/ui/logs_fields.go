package ui

import (
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/logview"
	"github.com/stokaro/kc/internal/ui/dialog"
	"github.com/stokaro/kc/pkg/appconfig"
)

// Keys of the record actions in the logs viewer (see
// docs/adr/0188-log-field-actions-and-context.md).
const (
	logsFieldsKey = "alt+d"
	logsBackKey   = "alt+z"
)

// logsFieldsModalName registers the fields dialog with the modal manager.
const logsFieldsModalName = "logs_fields"

// logsContextLines is how many lines the context shows on each side.
const logsContextLines = 10

// logsHistoryMax bounds the views M-z can return to.
const logsHistoryMax = 20

// logsViewState is a view M-z returns to: the filter and the record the
// user looked at.
type logsViewState struct {
	query string
	seq   logview.Seq
	ok    bool
}

// logsContext is the context view: the lines around one record, shown in
// place of the filtered lines until M-z.
type logsContext struct {
	anchor     logview.Seq
	sameSource bool
	complete   bool
	lines      []logview.ContextLine
}

// logsFieldOptions are the record actions' settings from viewer.logs.
type logsFieldOptions struct {
	correlation []string
	traceURL    string
}

// SetFieldOptions sets the correlation fields and the trace URL template.
func (v *LogsViewer) SetFieldOptions(cfg appconfig.LogsViewerConfig) {
	v.fieldOpts = logsFieldOptions{correlation: cfg.CorrelationFields, traceURL: cfg.TraceURL}
}

// SetOnFields sets what M-d does; the app opens the fields dialog.
func (v *LogsViewer) SetOnFields(fn func() tea.Cmd) { v.onFields = fn }

// focusRecord returns the record the user points at (see
// viewer.Widget.FocusLine): in the context view the line under the focus,
// else the shown record on that line.
func (v *LogsViewer) focusRecord() (logview.Seq, bool) {
	if seq, ok := v.pointedRecord(); ok {
		return seq, true
	}
	line, ok := v.inner.FocusLine()
	if !ok {
		return 0, false
	}
	if v.context != nil {
		return v.context.seqAt(line)
	}
	return v.log.SeqAt(line)
}

// contextNoticeRows is the number of rows contextDisplay puts before the
// first context line: the note that older lines were dropped.
func (c *logsContext) noticeRows() int {
	if c.complete {
		return 0
	}
	return 1
}

// seqAt returns the record on display row line of the context view. A
// notice row is no record.
func (c *logsContext) seqAt(line int) (logview.Seq, bool) {
	i := line - c.noticeRows()
	if i < 0 || i >= len(c.lines) {
		return 0, false
	}
	return c.lines[i].Seq, true
}

// pushView remembers the current view for M-z.
func (v *LogsViewer) pushView(seq logview.Seq, ok bool) {
	v.history = append(v.history, logsViewState{query: v.Query(), seq: seq, ok: ok})
	if len(v.history) > logsHistoryMax {
		v.history = v.history[len(v.history)-logsHistoryMax:]
	}
}

// applyQueryText parses and applies a filter built by an action. The terms
// come from FieldTerm, so a parse error means a bug, not user input.
func (v *LogsViewer) applyQueryText(text string) error {
	q, err := logview.ParseQuery(text)
	if err != nil {
		return err
	}
	v.context = nil
	v.SetQuery(q)
	return nil
}

// addFieldTerm adds an exact include or exclude term for a field of record
// seq to the filter, and keeps that record in view when it still shows.
func (v *LogsViewer) addFieldTerm(seq logview.Seq, f logview.Field, exclude bool) error {
	v.pushView(seq, true)
	text := strings.TrimSpace(v.Query() + " " + logview.FieldTerm(f.Path, f.Value, exclude))
	if err := v.applyQueryText(text); err != nil {
		return err
	}
	v.correlated = ""
	v.showRecord(seq)
	return nil
}

// followField replaces the filter with the exact value of a field, to
// follow one request or trace across the kept lines of every source.
func (v *LogsViewer) followField(seq logview.Seq, f logview.Field) error {
	v.pushView(seq, true)
	if err := v.applyQueryText(logview.FieldTerm(f.Path, f.Value, false)); err != nil {
		return err
	}
	v.correlated = f.Path.String() + "=" + f.Value
	v.showRecord(seq)
	return nil
}

// showContext shows the lines around record seq in place of the filtered
// lines. sameSource keeps the lines of the record's own container.
func (v *LogsViewer) showContext(seq logview.Seq, sameSource bool) error {
	lines, complete, err := v.log.Context(seq, logsContextLines, logsContextLines, sameSource)
	if err != nil {
		return err
	}
	v.pushView(seq, true)
	v.context = &logsContext{anchor: seq, sameSource: sameSource, complete: complete, lines: lines}
	v.autoFollow = false
	v.refreshDisplay()
	for i, l := range lines {
		if l.Kind == logview.ContextAnchor {
			v.inner.ScrollToLine(max(0, i+v.context.noticeRows()-v.height/3))
		}
	}
	return nil
}

// back returns to the view before the last field action or context.
func (v *LogsViewer) back() {
	if len(v.history) == 0 {
		v.note = "nothing to return to"
		return
	}
	prev := v.history[len(v.history)-1]
	v.history = v.history[:len(v.history)-1]
	v.context = nil
	v.correlated = ""
	// The saved text was a valid filter.
	q, _ := logview.ParseQuery(prev.query)
	v.SetQuery(q)
	if prev.ok {
		v.showRecord(prev.seq)
	}
}

// showRecord scrolls to record seq and stops following, or says why it
// cannot: the record was dropped or the filter hides it.
func (v *LogsViewer) showRecord(seq logview.Seq) {
	if _, err := v.log.Record(seq); err != nil {
		v.note = "the selected line was dropped to stay within the limits"
		return
	}
	line, ok := v.log.DisplayLine(seq)
	if !ok {
		v.note = "the filter hides the selected line"
		return
	}
	v.autoFollow = false
	v.inner.ScrollToLine(max(0, line-v.height/3))
}

// contextDisplay renders the context view: > marks the record, * the
// lines that also pass the filter, and the other lines are indented.
func (v *LogsViewer) contextDisplay() []string {
	c := v.context
	// The rows before the lines are counted by noticeRows; keep the two
	// in step, so a row maps to its own record.
	out := make([]string, 0, len(c.lines)+1)
	if !c.complete {
		out = append(out, "[kc] older lines were dropped to stay within the limits")
	}
	for _, l := range c.lines {
		mark := "  "
		switch l.Kind {
		case logview.ContextAnchor:
			mark = "> "
		case logview.ContextMatch:
			mark = "* "
		}
		out = append(out, mark+l.Text)
	}
	return out
}

// fieldsStatus describes a correlation filter or the context view for the
// status line: which sources and which kept lines it covers.
func (v *LogsViewer) fieldsStatus() string {
	var parts []string
	switch {
	case v.context != nil:
		scope := "all sources"
		if v.context.sameSource {
			scope = "its source"
		}
		parts = append(parts, fmt.Sprintf("context: %d lines around the line, %s, M-z returns", len(v.context.lines), scope))
	case v.correlated != "":
		parts = append(parts, "following "+v.correlated+" in "+v.searchScope()+", M-z returns")
	}
	if v.note != "" {
		parts = append(parts, v.note)
	}
	return strings.Join(parts, "; ")
}

// searchScope says what a filter searches: the kept lines, their time span
// when the stream has timestamps, and the sources.
func (v *LogsViewer) searchScope() string {
	recs := v.log.Records()
	text := fmt.Sprintf("the %d kept lines", len(recs))
	if len(recs) > 0 && recs[0].Prefix != "" && recs[len(recs)-1].Prefix != "" {
		text += " from " + recs[0].Prefix + " to " + recs[len(recs)-1].Prefix
	}
	if mux := v.Mux(); mux != nil {
		s := mux.Stats()
		n := len(mux.Sources()) - s.Excluded
		text += fmt.Sprintf(" of %d %s", n, plural(n, "source", "sources"))
		if s.Excluded > 0 {
			text += fmt.Sprintf(" (%d excluded)", s.Excluded)
		}
	}
	return text
}

// traceURL fills the trace URL template with value as a URL component.
// It reports why there is no URL.
func traceURL(template, value string) (string, error) {
	switch {
	case template == "":
		return "", fmt.Errorf("no trace link: set viewer.logs.traceURL")
	case !strings.Contains(template, "{trace}"):
		return "", fmt.Errorf("viewer.logs.traceURL has no {trace}")
	case value == "":
		return "", fmt.Errorf("no trace ID in this line")
	}
	// QueryEscape with %20 for spaces is safe in a path and in a query.
	escaped := strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
	return strings.ReplaceAll(template, "{trace}", escaped), nil
}

// correlationField returns the index of the first configured correlation
// field the record has, or -1.
// A name is a top-level key or a dotted path to a nested one.
func correlationField(fields []logview.Field, names []string) int {
	for _, name := range names {
		want := logview.KeyPath(strings.Split(strings.TrimPrefix(name, "."), ".")...).String()
		for i, f := range fields {
			if f.Path.String() == want {
				return i
			}
		}
	}
	return -1
}

// logsFieldsDialog lists the fields of one record and runs the record
// actions on the selected field.
type logsFieldsDialog struct {
	viewer *LogsViewer
	seq    logview.Seq
	title  string
	fields []logview.Field
	cursor int
	top    int
	note   string
	width  int
	height int
	skin   dialog.Skin
	done   func()
}

func newLogsFieldsDialog(v *LogsViewer, seq logview.Seq, done func()) (*logsFieldsDialog, error) {
	r, err := v.log.Record(seq)
	if err != nil {
		return nil, err
	}
	d := &logsFieldsDialog{viewer: v, seq: seq, fields: r.Fields(), done: done, skin: dialog.Normal()}
	d.title = r.Source.String()
	if d.title != "" {
		d.title = "[" + d.title + "] "
	}
	d.title += ansi.Strip(r.Raw)
	if i := correlationField(d.fields, v.fieldOpts.correlation); i >= 0 {
		d.cursor = i
	}
	if len(d.fields) == 0 {
		d.note = "not a JSON line: no fields; x and s show the context"
	}
	return d, nil
}

func (d *logsFieldsDialog) Init() tea.Cmd { return nil }

func (d *logsFieldsDialog) SetDimensions(w, h int) { d.width, d.height = w, h }

// PreferredSize implements ModalSizer.
func (d *logsFieldsDialog) PreferredSize(maxW, maxH int) (width, height int) {
	width = 90
	if maxW > 0 {
		width = min(width, maxW)
	}
	height = len(d.fields) + 5
	if maxH > 0 {
		height = min(height, maxH)
	}
	return width, max(height, 7)
}

// FooterHints implements ModalFooterHints.
func (d *logsFieldsDialog) FooterHints() []FooterHint {
	has := len(d.fields) > 0
	return []FooterHint{
		{Key: "Enter", Label: "Include", Enabled: has},
		{Key: "-", Label: "Exclude", Enabled: has},
		{Key: "f", Label: "Follow", Enabled: has},
		{Key: "c", Label: "Copy", Enabled: has},
		{Key: "t", Label: "Trace", Enabled: has},
		{Key: "x", Label: "Context", Enabled: true},
		{Key: "s", Label: "Ctx source", Enabled: true},
		{Key: "Esc", Label: "Close", Enabled: true},
	}
}

func (d *logsFieldsDialog) rows() int { return max(1, d.height-4) }

func (d *logsFieldsDialog) View() tea.View {
	width := max(d.width, 20)
	inner := width - 2
	line := func(text string, style lipgloss.Style) string {
		text = ansi.Truncate(text, inner, "…")
		text += strings.Repeat(" ", max(0, inner-lipgloss.Width(text)))
		return d.skin.Normal.Render(" ") + style.Render(text) + d.skin.Normal.Render(" ")
	}
	out := []string{line(d.title, d.skin.Normal.Bold(true)), dialog.RenderRule(d.skin, width)}
	rows := d.rows()
	d.top = min(max(d.top, d.cursor-rows+1), d.cursor)
	for i := d.top; i < d.top+rows; i++ {
		if i >= len(d.fields) {
			out = append(out, line("", d.skin.Normal))
			continue
		}
		f := d.fields[i]
		style := d.skin.Normal
		if i == d.cursor {
			style = d.skin.Focus
		}
		out = append(out, line(f.Path.String()+" = "+f.Value, style))
	}
	out = append(out, dialog.RenderRule(d.skin, width))
	style := d.skin.Normal
	if strings.HasPrefix(d.note, "no ") || strings.HasPrefix(d.note, "viewer.") || strings.Contains(d.note, "dropped") {
		style = d.skin.Error
	}
	out = append(out, line(d.note, style))
	return tea.NewView(strings.Join(out, "\n"))
}

// RuleRows implements ModalRules.
func (d *logsFieldsDialog) RuleRows() []int { return []int{1, 2 + d.rows()} }

func (d *logsFieldsDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return d, nil
	}
	field, has := d.selected()
	switch key.String() {
	case "up":
		d.cursor = max(0, d.cursor-1)
	case "down":
		d.cursor = min(max(0, len(d.fields)-1), d.cursor+1)
	case "pgup":
		d.cursor = max(0, d.cursor-d.rows())
	case "pgdown":
		d.cursor = min(max(0, len(d.fields)-1), d.cursor+d.rows())
	case "enter", "=", "+", "-", "!", "f":
		if !has {
			return d, nil
		}
		var err error
		switch key.String() {
		case "-", "!":
			err = d.viewer.addFieldTerm(d.seq, field, true)
		case "f":
			err = d.viewer.followField(d.seq, field)
		default:
			err = d.viewer.addFieldTerm(d.seq, field, false)
		}
		return d, d.finish(err)
	case "c":
		if !has {
			return d, nil
		}
		return d, tea.Batch(viewerCopyRequest(field.Value, field.Path.String()+" value"), d.finish(nil))
	case "t":
		if !has {
			return d, nil
		}
		link, err := traceURL(d.viewer.fieldOpts.traceURL, field.Value)
		if err != nil {
			d.note = err.Error()
			return d, nil
		}
		return d, tea.Batch(viewerCopyRequest(link, "trace URL"), d.finish(nil))
	case "x", "s":
		return d, d.finish(d.viewer.showContext(d.seq, key.String() == "s"))
	}
	return d, nil
}

func (d *logsFieldsDialog) selected() (logview.Field, bool) {
	if d.cursor < 0 || d.cursor >= len(d.fields) {
		return logview.Field{}, false
	}
	return d.fields[d.cursor], true
}

// finish closes the dialog after an action, or shows why it failed.
func (d *logsFieldsDialog) finish(err error) tea.Cmd {
	if err != nil {
		d.note = err.Error()
		return nil
	}
	if d.done != nil {
		d.done()
	}
	return nil
}

// showLogsFieldsModal opens the fields dialog for the record the viewer
// points at.
func (a *App) showLogsFieldsModal(v *LogsViewer) tea.Cmd {
	if a.modalManager == nil || v == nil {
		return nil
	}
	seq, ok := v.focusRecord()
	if !ok {
		v.note = "no line to inspect"
		return nil
	}
	behind := a.modalManager.GetActiveModal()
	d, err := newLogsFieldsDialog(v, seq, func() { a.hideModalName(logsFieldsModalName) })
	if err != nil {
		v.note = err.Error()
		return nil
	}
	modal := NewModal("Log fields", d)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(logsFieldsModalName, modal)
	background := ""
	if behind != nil {
		background = viewString(behind.View())
	}
	a.configureModalWindow(modal, d, -1, background, 92, 20)
	if behind != nil {
		modal.SetWindowedBackgroundProvider(func() string { return viewString(behind.View()) })
	}
	a.showModal(logsFieldsModalName)
	return d.Init()
}

// setupLogFields wires the record actions of a logs viewer.
func (a *App) setupLogFields(v *LogsViewer) {
	cfg := appconfig.Default().Viewer.Logs
	if a.cfg != nil {
		cfg = a.cfg.Viewer.Logs
	}
	v.SetFieldOptions(cfg)
	v.SetOnFields(func() tea.Cmd { return a.showLogsFieldsModal(v) })
}
