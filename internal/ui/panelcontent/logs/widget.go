// Package logs implements the Logs panel mode: it streams the log of the pod
// or container selected in the other panel.
//
// All Widget methods run on the Bubble Tea goroutine. Opening a stream and
// reading it happen in tea.Cmds. Every stream belongs to a generation; a
// selection change starts a new generation, cancels the old stream and drops
// any result of an older generation that is still on its way.
package logs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/logview"
	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// DefaultDebounce is how long the selection must rest on a row before its
// stream opens, so scrolling through a list does not open one per row.
const DefaultDebounce = 300 * time.Millisecond

// maxChunkLines bounds the lines one read message carries.
const maxChunkLines = 512

// Hint is shown when the other panel's selection has no log.
const Hint = "Select a pod or container in the other panel to show its log."

// Container choices of Settings besides a container name. "*" is not a
// valid container name, so it cannot clash with one.
const (
	// ContainerDefault streams what the selected row names: its container,
	// or the default container of a pod.
	ContainerDefault = ""
	// ContainerAll merges every started container of the pod.
	ContainerAll = "*"
)

// Settings are the stream options F2 changes.
type Settings struct {
	// TailLines is the number of recent lines; 0 keeps the row's tail
	// (models.DefaultLogsTailLines for a pod), -1 fetches the whole log.
	TailLines int64
	// SinceSeconds limits the log to newer lines; 0 means no limit.
	SinceSeconds int64
	Timestamps   bool
	// Previous reads the previous instance of the container. It belongs
	// to one row and is cleared when the selection moves to another row.
	Previous bool
	// Container is ContainerDefault, ContainerAll or a container of the
	// current pod. A container name is cleared when the selection moves
	// to another row; ContainerAll stays.
	Container string
}

// Stream is an open log stream.
type Stream struct {
	// Spec is what is streamed; Container is filled in for a pod and empty
	// for all containers.
	Spec models.LogsSpec
	// Containers are the pod's containers for the F2 choice; nil when the
	// pod could not be read.
	Containers []string
	Body       io.ReadCloser
	// Cancel stops the stream. It must not block.
	Cancel context.CancelFunc
}

// Open opens a stream. It runs in a tea.Cmd and may do I/O.
type Open func() (Stream, error)

// Source prepares the stream for req. It runs on the UI goroutine and must
// not do I/O: it captures what Open needs, such as the current cluster.
type Source func(req Request) Open

// Options configure a Widget.
type Options struct {
	Source Source
	// Debounce defaults to DefaultDebounce.
	Debounce time.Duration
	// After replaces tea.Tick in tests.
	After func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	Theme string
	Wrap  bool
	// Settings are the initial stream options, usually viewer.logs from
	// the configuration.
	Settings Settings
	// Limits bound the lines the panel keeps; the zero value means
	// logview.DefaultLimits.
	Limits logview.Limits
	// Now replaces time.Now in tests.
	Now func() time.Time
}

type state int

const (
	stateIdle      state = iota // no target: the hint is shown
	stateWaiting                // debouncing or opening
	stateStreaming              // reading
	stateEnded                  // the server closed the stream
	stateFailed                 // opening or reading failed
)

// Widget shows the log of the other panel's selection.
type Widget struct {
	deps   panelcontent.WidgetDeps
	opts   Options
	viewer *viewer.Widget

	gen uint64
	// target is the selected row's request; req is what is streamed, target
	// with the settings applied; spec is req.Spec with the container the
	// opener resolved.
	target   Request
	targetID string
	req      Request
	spec     models.LogsSpec
	settings Settings
	// containers of the pod named by containersOf, from the last open.
	containers   []string
	containersOf string
	state        state
	err          error
	cancel       context.CancelFunc // of the open stream, nil when none
	hasLines     bool
	follow       bool
	// window accounts for the shown lines; the oldest go when it is full
	// (docs/adr/0179-bounded-log-retention.md). lostPlace is set when the
	// lines the user was reading went; the next scroll clears it.
	window    *logview.Window
	lostPlace bool
	// body is the open stream's body; a resumed stream reports when it
	// waits to reconnect (docs/adr/0180-resume-live-logs.md).
	body io.ReadCloser
	// span is the investigation window (docs/adr/0187-pin-freeze-window.md).
	span spanState
}

// reconnecting reports a stream body that waits to reconnect.
func reconnecting(body io.ReadCloser) bool {
	r, ok := body.(interface{ Reconnecting() bool })
	return ok && r.Reconnecting()
}

type debounceMsg struct {
	w   *Widget
	gen uint64
}

type openedMsg struct {
	w      *Widget
	gen    uint64
	stream Stream
	err    error
}

type chunkMsg struct {
	w      *Widget
	gen    uint64
	stream Stream
	reader *bufio.Reader
	lines  []string
	err    error
}

// The widget's messages are panelcontent.BackgroundMsg: the app hands them
// to the widget even behind a dialog or the terminal, because a dropped
// message stops the stream's read loop.
func (debounceMsg) WidgetBackground() {}
func (openedMsg) WidgetBackground()   {}
func (chunkMsg) WidgetBackground()    {}

// New returns a Logs widget that shows the hint until OnSelectionChanged
// names a pod or container.
func New(deps panelcontent.WidgetDeps, opts Options) *Widget {
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.After == nil {
		opts.After = tea.Tick
	}
	if opts.Limits == (logview.Limits{}) {
		opts.Limits = logview.DefaultLimits()
	}
	v := viewer.New(opts.Theme)
	v.SetPlainMode(true)
	v.SetWrapMode(opts.Wrap)
	w := &Widget{deps: deps, opts: opts, viewer: v, follow: true, settings: opts.Settings, window: logview.NewWindow(opts.Limits)}
	w.showHint()
	return w
}

func (w *Widget) Init(context.Context) tea.Cmd { return nil }

// OnSelectionChanged switches to the log of sel's item, the selection of the
// other panel. A new target stops the old stream at once and opens the new
// one after the debounce delay. The same target keeps its stream; a forced
// selection restarts an ended one.
func (w *Widget) OnSelectionChanged(_ context.Context, sel panelcontent.Selection) tea.Cmd {
	target, ok := Target(sel.Item)
	if !ok {
		if w.state != stateIdle {
			w.stop()
			w.targetID = ""
			w.showHint()
		}
		return nil
	}
	if id := targetID(target); id != w.targetID {
		// Choices made for one row do not carry over to another.
		if w.settings.Container != ContainerAll {
			w.settings.Container = ContainerDefault
		}
		w.settings.Previous = false
		w.targetID = id
		w.resolveSpan()
	}
	w.target = target
	return w.restart(sel.Force)
}

// Settings returns the stream options.
func (w *Widget) Settings() Settings { return w.settings }

// SetSettings changes the stream options and restarts the stream when what
// is streamed changes. Without a target the options apply to the next one.
func (w *Widget) SetSettings(s Settings) tea.Cmd {
	w.settings = s
	if w.state == stateIdle {
		return nil
	}
	return w.restart(false)
}

// Containers returns the containers of the selected pod, known once a
// stream of the pod has opened; nil before.
func (w *Widget) Containers() []string {
	if w.state == stateIdle || w.containersOf != podKey(w.target.Spec) {
		return nil
	}
	return append([]string(nil), w.containers...)
}

// Target returns the selected row's request before the settings are
// applied. It is the zero Request while the hint is shown.
func (w *Widget) Target() Request {
	if w.state == stateIdle {
		return Request{}
	}
	return w.target
}

// effective applies the settings to the selected row's request.
func (w *Widget) effective() Request {
	r := w.target
	r.Spec.Follow = true
	if w.settings.TailLines != 0 {
		r.Spec.TailLines = w.settings.TailLines
	}
	r.Spec.SinceSeconds = w.settings.SinceSeconds
	r.Spec.Timestamps = w.settings.Timestamps
	r.Spec.Previous = w.settings.Previous
	switch c := w.settings.Container; c {
	case ContainerDefault:
	case ContainerAll:
		r.AllContainers, r.Spec.Container = true, ""
	default:
		r.AllContainers, r.Spec.Container = false, c
	}
	return w.applySpan(r)
}

// restart streams the effective request after the debounce delay. It keeps
// a running stream of the same request, and an ended one unless force.
func (w *Widget) restart(force bool) tea.Cmd {
	req := w.effective()
	if req == w.req && w.state != stateIdle {
		if !force || (w.state != stateEnded && w.state != stateFailed) {
			return nil
		}
	}
	w.stop()
	w.req, w.spec = req, req.Spec
	w.state, w.err = stateWaiting, nil
	w.hasLines, w.follow, w.lostPlace = false, true, false
	w.span.hidden, w.span.first = 0, time.Time{}
	w.window.Reset()
	w.viewer.SetContent("Waiting for the log...", viewer.Metadata{Title: w.title()})
	gen := w.gen
	return w.opts.After(w.opts.Debounce, func(time.Time) tea.Msg { return debounceMsg{w: w, gen: gen} })
}

// Spec returns what is streamed, with the container resolved once the
// stream has opened. It is empty while the hint is shown.
func (w *Widget) Spec() models.LogsSpec {
	if w.state == stateIdle {
		return models.LogsSpec{}
	}
	return w.spec
}

// AllContainers reports whether the stream merges every container.
func (w *Widget) AllContainers() bool { return w.state != stateIdle && w.req.AllContainers }

// Streaming reports whether a stream is open or being opened.
func (w *Widget) Streaming() bool { return w.state == stateWaiting || w.state == stateStreaming }

// stop cancels the current stream and makes every pending result stale.
func (w *Widget) stop() {
	w.gen++
	w.body = nil
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
}

func (w *Widget) showHint() {
	w.state, w.err = stateIdle, nil
	w.hasLines = false
	w.viewer.SetContent(Hint, viewer.Metadata{Title: "Logs"})
}

func (w *Widget) Update(_ context.Context, msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case debounceMsg:
		return w.onDebounce(m)
	case openedMsg:
		return w.onOpened(m)
	case chunkMsg:
		return w.onChunk(m)
	case tea.KeyMsg:
		if m.String() == "end" && !w.follow {
			w.follow, w.lostPlace = true, false
			w.viewer.ScrollToEnd()
			return nil, true
		}
	}
	cmd, handled := w.viewer.Update(msg)
	if handled {
		w.follow = w.viewer.AtEnd()
		w.lostPlace = false
	}
	return cmd, handled
}

// onDebounce opens the stream once the target stopped changing.
func (w *Widget) onDebounce(m debounceMsg) (tea.Cmd, bool) {
	if m.w != w {
		return nil, false
	}
	if m.gen != w.gen {
		return nil, true
	}
	if w.opts.Source == nil {
		w.fail(errors.New("logs unavailable"))
		return nil, true
	}
	open, gen := w.opts.Source(w.req), w.gen
	return func() tea.Msg {
		s, err := open()
		return openedMsg{w: w, gen: gen, stream: s, err: err}
	}, true
}

// onOpened starts reading a stream that opened for the current target.
func (w *Widget) onOpened(m openedMsg) (tea.Cmd, bool) {
	if m.w != w {
		return nil, false
	}
	if m.gen != w.gen {
		return closeStream(m.stream), true
	}
	if m.err != nil {
		w.fail(m.err)
		return nil, true
	}
	w.state, w.cancel, w.body = stateStreaming, m.stream.Cancel, m.stream.Body
	if m.stream.Spec.Pod != "" {
		w.spec = m.stream.Spec
	}
	if m.stream.Containers != nil {
		w.containers, w.containersOf = m.stream.Containers, podKey(w.target.Spec)
	}
	w.viewer.SetContent("No log lines yet.", viewer.Metadata{Title: w.title()})
	return read(w, w.gen, m.stream, bufio.NewReader(m.stream.Body)), true
}

// onChunk appends read lines and reads on until the stream ends.
func (w *Widget) onChunk(m chunkMsg) (tea.Cmd, bool) {
	if m.w != w {
		return nil, false
	}
	if m.gen != w.gen {
		return closeStream(m.stream), true
	}
	w.append(w.cutAfterEnd(m.lines))
	if m.err == nil {
		return read(w, w.gen, m.stream, m.reader), true
	}
	w.cancel = nil
	if errors.Is(m.err, io.EOF) {
		w.state = stateEnded
		w.append([]string{"[the log stream ended]"})
	} else {
		w.state, w.err = stateFailed, m.err
		w.append([]string{fmt.Sprintf("[the log stream failed: %v]", m.err)})
	}
	return closeStream(m.stream), true
}

func (w *Widget) fail(err error) {
	w.state, w.err = stateFailed, err
	w.viewer.SetContent(fmt.Sprintf("Logs failed: %v", err), viewer.Metadata{Title: w.title()})
}

func (w *Widget) append(lines []string) {
	if len(lines) == 0 {
		return
	}
	for i, line := range lines {
		lines[i] = logview.TruncateLine(line, logview.ReadLimit)
		w.window.Add(logview.Cost(len(lines[i]), false))
	}
	if w.hasLines {
		w.viewer.AppendLines(lines)
	} else {
		// SetContent keeps the viewer from starting with an empty line.
		w.viewer.SetContent(strings.Join(lines, "\n"), w.viewer.Metadata())
		w.hasLines = true
	}
	if n := w.window.Trim(); n > 0 && w.viewer.DropFirstLines(n) && !w.follow {
		w.lostPlace = true
	}
	if w.follow || w.viewer.AtEnd() {
		w.viewer.ScrollToEnd()
		w.follow = true
	}
}

// read returns a command that reads the next lines of s. Only one read of a
// stream runs at a time, so r is used by one goroutine only.
func read(w *Widget, gen uint64, s Stream, r *bufio.Reader) tea.Cmd {
	return func() tea.Msg {
		lines, err := readLines(r, maxChunkLines)
		return chunkMsg{w: w, gen: gen, stream: s, reader: r, lines: lines, err: err}
	}
}

// readLines blocks for one line, then takes the complete lines that are
// already buffered, up to limit. A last line without a newline is returned
// with io.EOF.
func readLines(r *bufio.Reader, limit int) ([]string, error) {
	var lines []string
	for len(lines) < limit {
		line, err := logview.ReadLine(r, logview.ReadLimit)
		if line != "" {
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return lines, err
		}
		buffered, _ := r.Peek(r.Buffered())
		if bytes.IndexByte(buffered, '\n') < 0 {
			break
		}
	}
	return lines, nil
}

// closeStream cancels and closes s off the UI goroutine: Close of an HTTP
// body can wait for a read in progress.
func closeStream(s Stream) tea.Cmd {
	if s.Cancel == nil && s.Body == nil {
		return nil
	}
	return func() tea.Msg {
		if s.Cancel != nil {
			s.Cancel()
		}
		if s.Body != nil {
			_ = s.Body.Close()
		}
		return nil
	}
}

// title names the stream: the container once it is known, all-containers
// for a merged stream.
func (w *Widget) title() string {
	name := w.spec.Namespace + "/" + w.spec.Pod
	switch {
	case w.req.AllContainers:
		name += "/all-containers"
	case w.spec.Container != "":
		name += "/" + w.spec.Container
	}
	return "logs:" + name
}

func (w *Widget) View(_ context.Context, frame panelcontent.Frame) tea.View {
	return tea.NewView(w.viewer.View(viewer.Frame{
		Width:   frame.Size.Width,
		Height:  frame.Size.Height,
		Focused: frame.Focused,
	}))
}

func (w *Widget) Resize(_ context.Context, size panelcontent.Size) {
	w.viewer.Resize(size.Width, size.Height)
}

// SetFocus stops the stream when the panel leaves Logs mode; the app replays
// the other panel's selection when the mode comes back.
func (w *Widget) SetFocus(_ context.Context, focused bool) {
	if focused {
		return
	}
	w.stop()
	w.targetID = ""
	w.showHint()
}

// Teardown stops the stream.
func (w *Widget) Teardown(context.Context) { w.stop() }

// SetTheme sets the viewer theme (F2).
func (w *Widget) SetTheme(theme string) { w.viewer.SetTheme(theme) }

// Theme returns the viewer theme.
func (w *Widget) Theme() string { return w.viewer.Theme() }

// SetWrapMode switches line wrapping (F2).
func (w *Widget) SetWrapMode(on bool) { w.viewer.SetWrapMode(on) }

// WrapMode reports whether lines wrap.
func (w *Widget) WrapMode() bool { return w.viewer.WrapMode() }

func (w *Widget) Footer(_ context.Context, width int) string {
	return w.viewer.Footer(width)
}

// FrameInfo shows the stream target as breadcrumb and its state in the
// header.
func (w *Widget) FrameInfo(context.Context, panelcontent.FrameInfoRequest) panelcontent.FrameInfo {
	info := panelcontent.FrameInfo{SuppressFooter: true, TopIndicator: "─", BottomIndicator: "─", Breadcrumb: "Logs"}
	if w.state != stateIdle {
		info.Breadcrumb = w.title()
	}
	switch w.state {
	case stateWaiting:
		info.HeaderStatus = panelcontent.LoadingStatus
	case stateStreaming:
		switch {
		case reconnecting(w.body):
			info.HeaderStatus = "reconnecting"
		case w.lostPlace:
			info.HeaderStatus = "paused, view moved: its lines were dropped"
		case !w.follow:
			info.HeaderStatus = "paused, End follows"
		}
		// A merged stream (all containers) always shows which of its
		// sources it follows.
		if c, ok := w.body.(interface{ Coverage() string }); ok {
			info.HeaderStatus = strings.TrimSuffix(c.Coverage()+", "+info.HeaderStatus, ", ")
		}
	case stateEnded:
		info.HeaderStatus = "ended"
	case stateFailed:
		info.HeaderStatus = "failed"
	}
	if s := w.spanStatus(); s != "" && w.state != stateIdle {
		info.HeaderStatus = strings.TrimSuffix(s+", "+info.HeaderStatus, ", ")
	}
	current, total := w.viewer.Position()
	if total > 0 && w.hasLines {
		above, below := w.viewer.ScrollIndicators()
		if above {
			info.TopIndicator = "^"
		}
		if below {
			info.BottomIndicator = "v"
		}
		percent := int(math.Round(float64(current) * 100 / float64(total)))
		info.FooterStatus = fmt.Sprintf("%d/%d • %d%%", current, total, min(max(percent, 0), 100))
		if n := w.window.Dropped(); n > 0 {
			info.FooterStatus += fmt.Sprintf(" • %d older dropped", n)
		}
	}
	return info
}
