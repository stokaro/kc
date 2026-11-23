package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/logview"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// logStreamOpener opens the log stream for spec. It runs in a tea.Cmd, off
// the update goroutine. The returned cancel func stops the stream.
type logStreamOpener func(spec models.LogsSpec) (io.ReadCloser, context.CancelFunc, error)

// logStreamOpenedMsg reports the result of opening a stream. viewer and gen
// identify the stream it belongs to; a result for an older stream is closed
// and dropped.
type logStreamOpenedMsg struct {
	viewer *LogsViewer
	gen    int
	reader io.ReadCloser
	cancel context.CancelFunc
	err    error
}

// logStreamMsg carries lines read from a stream. The app hands it to viewer
// directly, so the stream keeps running while a dialog covers the viewer.
type logStreamMsg struct {
	viewer *LogsViewer
	gen    int
	lines  []string
	done   bool
	err    error
}

// LogOptions are the stream options the user changes in the logs viewer.
type LogOptions struct {
	// TailLines is the number of recent lines; -1 fetches the whole log.
	TailLines int64
	// SinceSeconds limits the log to newer lines; 0 means no limit.
	SinceSeconds int64
	Timestamps   bool
	Previous     bool
	// Container is the container choice of the Logs panel: "" for the
	// default, logswidget.ContainerAll or a container name. The fullscreen
	// viewer streams one fixed container and leaves it empty.
	Container string
}

func logOptionsFromSpec(spec models.LogsSpec) LogOptions {
	return LogOptions{
		TailLines:    spec.EffectiveTailLines(),
		SinceSeconds: nonNegative(spec.SinceSeconds),
		Timestamps:   spec.Timestamps,
		Previous:     spec.Previous,
	}
}

func (o LogOptions) applyTo(spec models.LogsSpec) models.LogsSpec {
	spec.TailLines = o.TailLines
	spec.SinceSeconds = o.SinceSeconds
	spec.Timestamps = o.Timestamps
	spec.Previous = o.Previous
	return spec
}

// summary describes the options for the footer, in kubectl's terms.
func (o LogOptions) summary() string {
	parts := make([]string, 0, 4)
	if o.TailLines > 0 {
		parts = append(parts, fmt.Sprintf("tail %d", o.TailLines))
	} else {
		parts = append(parts, "all lines")
	}
	if since := models.FormatLogsSince(o.SinceSeconds); since != "" {
		parts = append(parts, "since "+since)
	}
	if o.Timestamps {
		parts = append(parts, "timestamps")
	}
	if o.Previous {
		parts = append(parts, "previous")
	}
	return strings.Join(parts, ", ")
}

// LogsViewer renders a streaming pod log in a fullscreen modal.
type LogsViewer struct {
	inner      *viewer.Widget
	width      int
	height     int
	spec       models.LogsSpec
	open       logStreamOpener
	commandFor func(models.LogsSpec) string
	// gen numbers the streams. Messages from an older stream are dropped.
	gen        int
	reader     io.ReadCloser
	buffer     *bufio.Reader
	cancel     context.CancelFunc
	opening    bool
	closed     bool
	autoFollow bool
	done       bool
	err        error
	onOptions  func() tea.Cmd
	onClose    func() tea.Cmd
	onFilter   func() tea.Cmd
	onSources  func() tea.Cmd
	bar        viewerCommandBar
	// log keeps every line read; the widget shows log's display lines.
	log *logview.Log

	// merged marks a merged stream: every line starts with a colored
	// `[pod/container]` prefix, which becomes the record's source.
	merged bool
	// lostPlace is set when the lines the user was reading were dropped
	// to stay within the limits; the next scroll clears it.
	lostPlace bool

	// Record actions (M-d, M-z; see logs_fields.go).
	onFields  func() tea.Cmd
	fieldOpts logsFieldOptions
	history   []logsViewState
	// context, when set, is shown in place of the filtered lines.
	context *logsContext
	// correlated is the field=value a Follow action filters by.
	correlated string
	// note is a message for the status line until the next key.
	note string
}

// SetCommand sets the kubectl command shown under the log. A command builder
// set with SetCommandBuilder replaces it when the options change.
func (v *LogsViewer) SetCommand(cmd string) { v.bar.command = cmd }

// SetStyledLines marks the stream as a merged log whose lines carry a
// colored `[pod/container]` prefix (logmux.Format). The prefix becomes the
// record's source, so JSON after it is still parsed, and is shown colored.
func (v *LogsViewer) SetStyledLines(on bool) {
	v.merged = on
	format := logview.DefaultFormat()
	if on {
		format.SourceStyle = mergedSourceStyle
	}
	v.log.SetFormat(format)
	v.refreshDisplay()
}

// SetCommandBuilder sets how the kubectl command under the log is built, and
// builds it for the current spec.
func (v *LogsViewer) SetCommandBuilder(fn func(models.LogsSpec) string) {
	v.commandFor = fn
	v.refreshCommand()
}

// refreshCommand builds the command line again, for a builder that reads
// more than the spec.
func (v *LogsViewer) refreshCommand() {
	if v.commandFor != nil {
		v.bar.command = v.commandFor(v.spec)
	}
}

// NewLogsViewer returns a viewer for spec. Init opens the stream with open.
func NewLogsViewer(title string, spec models.LogsSpec, open logStreamOpener, theme string) *LogsViewer {
	w := viewer.New(theme)
	w.SetPlainMode(true)
	w.SetContent("", viewer.Metadata{
		Title:    title,
		MimeType: "text/plain",
		Filename: strings.ReplaceAll(title, "/", "_") + ".log",
	})
	w.SetLines(nil)
	lv := &LogsViewer{
		log:        logview.NewLog(logview.DefaultFormat()),
		inner:      w,
		spec:       spec,
		open:       open,
		autoFollow: true,
	}
	lv.refreshCallbacks()
	return lv
}

func (v *LogsViewer) Init() tea.Cmd {
	if v.reader != nil || v.opening {
		return nil
	}
	return v.start()
}

// Spec returns the spec of the current stream.
func (v *LogsViewer) Spec() models.LogsSpec { return v.spec }

// LogOptions returns the options of the current stream.
func (v *LogsViewer) LogOptions() LogOptions { return logOptionsFromSpec(v.spec) }

// SetLogOptions restarts the stream with opts and updates the command line.
// It returns nil when opts match the current stream.
func (v *LogsViewer) SetLogOptions(opts LogOptions) tea.Cmd {
	if v.closed || opts == v.LogOptions() {
		return nil
	}
	v.stopStream()
	v.spec = opts.applyTo(v.spec)
	if v.commandFor != nil {
		v.bar.command = v.commandFor(v.spec)
	}
	v.inner.SetContent("", v.inner.Metadata())
	// A reopened stream starts with no records; the view mode and the
	// filter stay.
	v.log.Reset()
	v.inner.SetLines(nil)
	v.inner.ScrollToEnd()
	v.autoFollow = true
	v.lostPlace = false
	v.context, v.correlated = nil, ""
	return v.start()
}

// start opens a new stream for the current spec.
func (v *LogsViewer) start() tea.Cmd {
	if v.closed || v.open == nil {
		return nil
	}
	v.gen++
	v.opening = true
	v.done = false
	v.err = nil
	gen, spec, open := v.gen, v.spec, v.open
	return func() tea.Msg {
		reader, cancel, err := open(spec)
		return logStreamOpenedMsg{viewer: v, gen: gen, reader: reader, cancel: cancel, err: err}
	}
}

func (v *LogsViewer) SetDimensions(w, h int) {
	v.width = w
	v.height = h
}

func (v *LogsViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		v.note = ""
		if cmd, handled := v.handleLogViewKey(key); handled {
			return v, cmd
		}
	}
	if cmd, handled := v.bar.handle(msg, v.inner, v.width, v.height); handled {
		return v, cmd
	}
	switch m := msg.(type) {
	case logStreamOpenedMsg:
		return v, v.handleOpened(m)
	case logStreamMsg:
		return v, v.handleChunk(m)
	case tea.KeyMsg:
		if m.String() == "end" {
			if !v.autoFollow {
				v.autoFollow = true
				v.lostPlace = false
				v.inner.ScrollToEnd()
				return v, nil
			}
		}
		if cmd, handled := v.inner.Update(m); handled {
			v.autoFollow = v.inner.AtEnd()
			v.lostPlace = false
			return v, cmd
		}
	case tea.MouseWheelMsg:
		if cmd, handled := v.inner.Update(m); handled {
			v.autoFollow = v.inner.AtEnd()
			v.lostPlace = false
			return v, cmd
		}
	default:
		if cmd, handled := v.inner.Update(msg); handled {
			v.autoFollow = v.inner.AtEnd()
			return v, cmd
		}
	}
	return v, nil
}

func (v *LogsViewer) handleOpened(m logStreamOpenedMsg) tea.Cmd {
	if m.viewer != v || m.gen != v.gen || v.closed {
		closeStream(m.reader, m.cancel)
		return nil
	}
	v.opening = false
	if m.err != nil {
		closeStream(m.reader, m.cancel)
		v.err = m.err
		v.done = true
		v.appendRecords([]string{"[kc] logs stream failed: " + m.err.Error()})
		return nil
	}
	v.reader = m.reader
	v.cancel = m.cancel
	v.buffer = bufio.NewReader(m.reader)
	return v.readNextChunk()
}

func (v *LogsViewer) handleChunk(m logStreamMsg) tea.Cmd {
	if m.viewer != v || m.gen != v.gen || v.closed {
		return nil
	}
	if len(m.lines) > 0 && v.appendRecords(m.lines) {
		if v.autoFollow || v.inner.AtEnd() {
			v.inner.ScrollToEnd()
			v.autoFollow = true
		}
	}
	if m.err != nil && !errors.Is(m.err, io.EOF) && !errors.Is(m.err, context.Canceled) {
		v.err = m.err
		v.done = true
		return nil
	}
	if m.done {
		v.done = true
		return nil
	}
	return v.readNextChunk()
}

func (v *LogsViewer) View() tea.View {
	if v.height <= 0 || v.width <= 0 {
		return tea.NewView("")
	}
	return tea.NewView(renderViewerWithBar(v.inner, &v.bar, v.width, v.height))
}

func (v *LogsViewer) FooterHints() []FooterHint {
	if v.inner.SearchMode() {
		return []FooterHint{
			{Key: "Enter", Label: "OK", Enabled: true},
			{Key: "Tab", Label: "Next field", Enabled: true},
			{Key: "Space", Label: "Toggle", Enabled: true},
			{Key: "Esc", Label: "Cancel", Enabled: true},
		}
	}
	return append([]FooterHint{
		{Key: "F2", Label: "Options", Enabled: v.onOptions != nil},
		{Key: "F3", Label: "Close", Enabled: v.onClose != nil},
		{Key: "F7", Label: "Search", Enabled: true},
		{Key: "F10", Label: "Close", Enabled: v.onClose != nil},
		{Key: "n", Label: "Next", Enabled: v.inner.HasSearchMatches()},
		{Key: "End", Label: "Follow", Enabled: !v.autoFollow},
	}, v.logViewHints()...)
}

// FooterStatus shows the search summary while a search is active, and the
// stream options and state otherwise.
func (v *LogsViewer) FooterStatus(width int) string {
	if width <= 0 {
		return ""
	}
	if base := v.inner.FooterStatusText(width); base != "" {
		return lipgloss.NewStyle().Width(width).Render(base)
	}
	return lipgloss.NewStyle().Width(width).Render(truncateStringToWidth(v.statusText(), width))
}

// statusText is the stream options followed by the stream state. A merged
// log starts with its coverage, so a view that misses sources says so.
func (v *LogsViewer) statusText() string {
	text := v.LogOptions().summary() + ", " + v.logViewStatus()
	if mux := v.Mux(); mux != nil {
		text = mux.Stats().String() + "; " + text
	}
	if fields := v.fieldsStatus(); fields != "" {
		text += "; " + fields
	}
	switch {
	case v.opening:
		text += " (connecting)"
	case isReconnecting(v.reader):
		text += " (reconnecting)"
	case v.err != nil:
		text += " (failed)"
	case v.done:
		text += " (ended)"
	}
	return text
}

func (v *LogsViewer) FooterCursor(width int) *tea.Cursor {
	return v.inner.FooterCursor(width)
}

func (v *LogsViewer) HandleModalEscape(key tea.KeyMsg) (bool, tea.Cmd) {
	if key.String() != "esc" {
		return false, nil
	}
	if cmd, handled := v.inner.HandleEscape(); handled {
		return true, cmd
	}
	if v.inner.HasSelection() {
		v.inner.ClearSelection()
		return true, nil
	}
	return false, nil
}

func (v *LogsViewer) SetOnOptions(fn func() tea.Cmd) {
	v.onOptions = fn
	v.refreshCallbacks()
}

func (v *LogsViewer) SetOnClose(fn func() tea.Cmd) {
	v.onClose = fn
	v.refreshCallbacks()
}

func (v *LogsViewer) SetTheme(name string) {
	v.inner.SetTheme(name)
}

func (v *LogsViewer) Theme() string { return v.inner.Theme() }

func (v *LogsViewer) SetWrapMode(on bool) { v.inner.SetWrapMode(on) }

func (v *LogsViewer) WrapMode() bool { return v.inner.WrapMode() }

// Close stops the stream for good. Results of a stream still opening are
// closed when they arrive.
func (v *LogsViewer) Close() {
	if v.closed {
		return
	}
	v.closed = true
	v.done = true
	v.stopStream()
}

// stopStream closes the current stream and drops messages still in flight
// for it.
func (v *LogsViewer) stopStream() {
	v.gen++
	v.opening = false
	closeStream(v.reader, v.cancel)
	v.reader = nil
	v.buffer = nil
	v.cancel = nil
}

// nonNegative clamps v at zero. The package's max helper takes ints.
func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func closeStream(reader io.ReadCloser, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	if reader != nil {
		_ = reader.Close()
	}
}

// readNextChunk reads the next line in a tea.Cmd. It captures the buffer and
// generation so a stream replaced meanwhile cannot feed the new one.
func (v *LogsViewer) readNextChunk() tea.Cmd {
	if v.closed || v.done || v.buffer == nil {
		return nil
	}
	buf, gen := v.buffer, v.gen
	return func() tea.Msg {
		line, err := logview.ReadLine(buf, logview.ReadLimit)
		if len(line) > 0 {
			text := strings.TrimRight(line, "\r\n")
			msg := logStreamMsg{viewer: v, gen: gen, lines: []string{text}}
			if errors.Is(err, io.EOF) {
				msg.done = true
			}
			return msg
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return logStreamMsg{viewer: v, gen: gen, done: true}
			}
			return logStreamMsg{viewer: v, gen: gen, err: err}
		}
		return logStreamMsg{viewer: v, gen: gen}
	}
}

func (v *LogsViewer) refreshCallbacks() {
	if v.inner == nil {
		return
	}
	v.inner.SetCallbacks(nil, func() tea.Cmd {
		if v.onOptions != nil {
			return v.onOptions()
		}
		return nil
	}, func() tea.Cmd {
		v.Close()
		if v.onClose != nil {
			return v.onClose()
		}
		return nil
	})
}
