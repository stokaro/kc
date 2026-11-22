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
	models "github.com/sttts/kc/internal/models"
	panelcontent "github.com/sttts/kc/internal/ui/panelcontent"
	"github.com/sttts/kc/internal/ui/viewer"
)

// DefaultDebounce is how long the selection must rest on a row before its
// stream opens, so scrolling through a list does not open one per row.
const DefaultDebounce = 300 * time.Millisecond

// maxChunkLines bounds the lines one read message carries.
const maxChunkLines = 512

// Hint is shown when the other panel's selection has no log.
const Hint = "Select a pod or container in the other panel to show its log."

// Stream is an open log stream.
type Stream struct {
	// Spec is what is streamed; Container is filled in for a pod.
	Spec models.LogsSpec
	Body io.ReadCloser
	// Cancel stops the stream. It must not block.
	Cancel context.CancelFunc
}

// Open opens a stream. It runs in a tea.Cmd and may do I/O.
type Open func() (Stream, error)

// Source prepares the stream for spec. It runs on the UI goroutine and must
// not do I/O: it captures what Open needs, such as the current cluster.
type Source func(spec models.LogsSpec) Open

// Msg marks the widget's background messages. The app must hand them to
// the panels' Logs widgets even behind a dialog or the terminal: a dropped
// message stops the stream's read loop.
type Msg interface {
	tea.Msg
	logsPanelMsg()
}

// Options configure a Widget.
type Options struct {
	Source Source
	// Debounce defaults to DefaultDebounce.
	Debounce time.Duration
	// After replaces tea.Tick in tests.
	After func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	Theme string
	Wrap  bool
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

	gen      uint64
	key      string
	spec     models.LogsSpec
	state    state
	err      error
	cancel   context.CancelFunc // of the open stream, nil when none
	hasLines bool
	follow   bool
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

func (debounceMsg) logsPanelMsg() {}
func (openedMsg) logsPanelMsg()   {}
func (chunkMsg) logsPanelMsg()    {}

// New returns a Logs widget that shows the hint until Follow names a pod or
// container.
func New(deps panelcontent.WidgetDeps, opts Options) *Widget {
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.After == nil {
		opts.After = tea.Tick
	}
	v := viewer.New(opts.Theme)
	v.SetPlainMode(true)
	v.SetWrapMode(opts.Wrap)
	w := &Widget{deps: deps, opts: opts, viewer: v, follow: true}
	w.showHint()
	return w
}

func (w *Widget) Init(context.Context) tea.Cmd { return nil }

// Follow switches to the log of sel's item. A new target stops the old
// stream at once and opens the new one after the debounce delay. The same
// target keeps its stream; a forced selection restarts an ended one.
func (w *Widget) Follow(_ context.Context, sel panelcontent.Selection) tea.Cmd {
	spec, ok := Target(sel.Item)
	if !ok {
		if w.state != stateIdle {
			w.stop()
			w.key = ""
			w.showHint()
		}
		return nil
	}
	k := key(spec)
	if k == w.key && w.state != stateIdle {
		if !sel.Force || (w.state != stateEnded && w.state != stateFailed) {
			return nil
		}
	}
	w.stop()
	w.key, w.spec = k, spec
	w.state, w.err = stateWaiting, nil
	w.hasLines, w.follow = false, true
	w.viewer.SetContent("Waiting for the log...", viewer.Metadata{Title: title(spec)})
	gen := w.gen
	return w.opts.After(w.opts.Debounce, func(time.Time) tea.Msg { return debounceMsg{w: w, gen: gen} })
}

// Spec returns the stream target. It is empty while the hint is shown.
func (w *Widget) Spec() models.LogsSpec {
	if w.state == stateIdle {
		return models.LogsSpec{}
	}
	return w.spec
}

// Streaming reports whether a stream is open or being opened.
func (w *Widget) Streaming() bool { return w.state == stateWaiting || w.state == stateStreaming }

// stop cancels the current stream and makes every pending result stale.
func (w *Widget) stop() {
	w.gen++
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
		open, gen := w.opts.Source(w.spec), w.gen
		return func() tea.Msg {
			s, err := open()
			return openedMsg{w: w, gen: gen, stream: s, err: err}
		}, true
	case openedMsg:
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
		w.state, w.cancel = stateStreaming, m.stream.Cancel
		if m.stream.Spec.Pod != "" {
			w.spec = m.stream.Spec
		}
		w.viewer.SetContent("No log lines yet.", viewer.Metadata{Title: title(w.spec)})
		return read(w, w.gen, m.stream, bufio.NewReader(m.stream.Body)), true
	case chunkMsg:
		if m.w != w {
			return nil, false
		}
		if m.gen != w.gen {
			return closeStream(m.stream), true
		}
		w.append(m.lines)
		if m.err != nil {
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
		return read(w, w.gen, m.stream, m.reader), true
	case tea.KeyMsg:
		if m.String() == "end" && !w.follow {
			w.follow = true
			w.viewer.ScrollToEnd()
			return nil, true
		}
	}
	cmd, handled := w.viewer.Update(msg)
	if handled {
		w.follow = w.viewer.AtEnd()
	}
	return cmd, handled
}

func (w *Widget) fail(err error) {
	w.state, w.err = stateFailed, err
	w.viewer.SetContent(fmt.Sprintf("Logs failed: %v", err), viewer.Metadata{Title: title(w.spec)})
}

func (w *Widget) append(lines []string) {
	if len(lines) == 0 {
		return
	}
	if w.hasLines {
		w.viewer.AppendLines(lines)
	} else {
		// SetContent keeps the viewer from starting with an empty line.
		w.viewer.SetContent(strings.Join(lines, "\n"), w.viewer.Metadata())
		w.hasLines = true
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
		line, err := r.ReadString('\n')
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

func title(spec models.LogsSpec) string {
	name := spec.Namespace + "/" + spec.Pod
	if spec.Container != "" {
		name += "/" + spec.Container
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

// SetFocus stops the stream when the panel leaves Logs mode; the app calls
// Follow again when the mode comes back.
func (w *Widget) SetFocus(_ context.Context, focused bool) {
	if focused {
		return
	}
	w.stop()
	w.key = ""
	w.showHint()
}

// Teardown stops the stream.
func (w *Widget) Teardown(context.Context) { w.stop() }

func (w *Widget) Footer(_ context.Context, width int) string {
	return w.viewer.Footer(width)
}

// FrameInfo shows the stream target as breadcrumb and its state in the
// header.
func (w *Widget) FrameInfo(context.Context, panelcontent.FrameInfoRequest) panelcontent.FrameInfo {
	info := panelcontent.FrameInfo{SuppressFooter: true, TopIndicator: "─", BottomIndicator: "─", Breadcrumb: "Logs"}
	if w.state != stateIdle {
		info.Breadcrumb = title(w.spec)
	}
	switch w.state {
	case stateWaiting:
		info.HeaderStatus = panelcontentLoading
	case stateStreaming:
		if !w.follow {
			info.HeaderStatus = "paused, End follows"
		}
	case stateEnded:
		info.HeaderStatus = "ended"
	case stateFailed:
		info.HeaderStatus = "failed"
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
	}
	return info
}

// panelcontentLoading matches the loading status #78 adds to panelcontent.
const panelcontentLoading = "loading..."
