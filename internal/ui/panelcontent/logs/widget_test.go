package logs_test

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// chanBody is a log body whose lines the test feeds through a channel. Read
// blocks like a followed log until a line or Close arrives.
type chanBody struct {
	lines chan string
	buf   string

	mu     sync.Mutex
	closed bool
}

func newChanBody(lines ...string) *chanBody {
	b := &chanBody{lines: make(chan string, 64)}
	for _, l := range lines {
		b.lines <- l + "\n"
	}
	return b
}

func (b *chanBody) Read(p []byte) (int, error) {
	if b.buf == "" {
		s, ok := <-b.lines
		if !ok {
			return 0, io.EOF
		}
		b.buf = s
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

func (b *chanBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *chanBody) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// fakeSource serves one chanBody per pod and records what was opened and
// canceled.
type fakeSource struct {
	mu       sync.Mutex
	bodies   map[string]*chanBody
	opened   []models.LogsSpec
	requests []logswidget.Request
	canceled map[string]bool
}

func newFakeSource(bodies map[string]*chanBody) *fakeSource {
	return &fakeSource{bodies: bodies, canceled: make(map[string]bool)}
}

func (s *fakeSource) Source(req logswidget.Request) logswidget.Open {
	spec := req.Spec
	return func() (logswidget.Stream, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if spec.Container == "" && !req.AllContainers {
			spec.Container = "app"
		}
		s.requests = append(s.requests, req)
		s.opened = append(s.opened, spec)
		pod := spec.Pod
		return logswidget.Stream{
			Spec:       spec,
			Containers: []string{"app", "sidecar"},
			Body:       s.bodies[pod],
			Cancel: func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				s.canceled[pod] = true
			},
		}, nil
	}
}

func (s *fakeSource) openedPods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pods []string
	for _, spec := range s.opened {
		pods = append(pods, spec.Pod)
	}
	return pods
}

func (s *fakeSource) wasCanceled(pod string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canceled[pod]
}

// immediate fires debounce timers at once.
func immediate(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
	return func() tea.Msg { return fn(time.Time{}) }
}

func newWidget(t *testing.T, src *fakeSource) *logswidget.Widget {
	t.Helper()
	w := logswidget.New(panelcontent.WidgetDeps{}, logswidget.Options{Source: src.Source, After: immediate})
	w.Resize(t.Context(), panelcontent.Size{Width: 80, Height: 10})
	return w
}

// run executes cmd and returns its message.
func run(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	return cmd()
}

// update delivers msg, which must belong to w.
func update(t *testing.T, w *logswidget.Widget, msg tea.Msg) tea.Cmd {
	t.Helper()
	cmd, handled := w.Update(t.Context(), msg)
	if !handled {
		t.Fatalf("widget did not handle %T", msg)
	}
	return cmd
}

// open follows item through the debounce and the open; it returns the
// command that reads the first lines.
func open(t *testing.T, w *logswidget.Widget, item models.Item) tea.Cmd {
	t.Helper()
	debounced := run(t, w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: item}))
	opened := run(t, update(t, w, debounced))
	return update(t, w, opened)
}

func screen(t *testing.T, w *logswidget.Widget) string {
	t.Helper()
	return ansi.Strip(w.View(t.Context(), panelcontent.Frame{Size: panelcontent.Size{Width: 80, Height: 10}}).Content)
}

func TestSelectionChangeSwitchesStream(t *testing.T) {
	t.Parallel()
	a, b := newChanBody("a1", "a2"), newChanBody("b1")
	src := newFakeSource(map[string]*chanBody{"pod-a": a, "pod-b": b})
	w := newWidget(t, src)

	readA := open(t, w, podItem("ns", "pod-a"))
	next := update(t, w, run(t, readA)) // a1
	next = update(t, w, run(t, next))   // a2
	if got := screen(t, w); !strings.Contains(got, "a1") || !strings.Contains(got, "a2") {
		t.Fatalf("pod-a lines missing:\n%s", got)
	}
	if spec := w.Spec(); spec.Pod != "pod-a" || spec.Container != "app" {
		t.Fatalf("Spec() = %+v, want pod-a with the resolved container", spec)
	}

	readB := open(t, w, containerItem("ns", "pod-b", "sidecar"))
	if !src.wasCanceled("pod-a") {
		t.Fatal("pod-a stream was not canceled on the selection change")
	}
	update(t, w, run(t, readB))
	got := screen(t, w)
	if !strings.Contains(got, "b1") || strings.Contains(got, "a1") {
		t.Fatalf("expected only pod-b lines:\n%s", got)
	}
	if spec := w.Spec(); spec.Pod != "pod-b" || spec.Container != "sidecar" {
		t.Fatalf("Spec() = %+v, want pod-b/sidecar", spec)
	}
	if pods := src.openedPods(); len(pods) != 2 || pods[0] != "pod-a" || pods[1] != "pod-b" {
		t.Fatalf("opened %v, want [pod-a pod-b]", pods)
	}

	// The read of pod-a that was in flight returns after the switch.
	a.lines <- "a3\n"
	if cmd := update(t, w, run(t, next)); cmd == nil {
		t.Fatal("a stale read must close its stream")
	} else {
		cmd()
	}
	if !a.isClosed() {
		t.Fatal("stale pod-a body was not closed")
	}
	if got := screen(t, w); strings.Contains(got, "a3") {
		t.Fatalf("stale pod-a line shown:\n%s", got)
	}
}

func TestSameSelectionKeepsStream(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody("a1")})
	w := newWidget(t, src)
	open(t, w, podItem("ns", "pod-a"))

	if cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podItem("ns", "pod-a"), Force: true}); cmd != nil {
		t.Fatal("the same pod must keep its open stream")
	}
	if src.wasCanceled("pod-a") {
		t.Fatal("stream canceled for an unchanged selection")
	}
}

func TestDebounceDropsSkippedRows(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody(), "pod-b": newChanBody("b1")})
	w := newWidget(t, src)
	ctx := t.Context()

	tickA := w.OnSelectionChanged(ctx, panelcontent.Selection{Item: podItem("ns", "pod-a")})
	tickB := w.OnSelectionChanged(ctx, panelcontent.Selection{Item: podItem("ns", "pod-b")})

	if cmd := update(t, w, run(t, tickA)); cmd != nil {
		t.Fatal("the timer of a row the selection left must not open a stream")
	}
	update(t, w, run(t, update(t, w, run(t, tickB))))
	if pods := src.openedPods(); len(pods) != 1 || pods[0] != "pod-b" {
		t.Fatalf("opened %v, want only pod-b", pods)
	}
}

func TestStaleOpenIsClosed(t *testing.T) {
	t.Parallel()
	a := newChanBody("a1")
	src := newFakeSource(map[string]*chanBody{"pod-a": a, "pod-b": newChanBody()})
	w := newWidget(t, src)
	ctx := t.Context()

	openA := update(t, w, run(t, w.OnSelectionChanged(ctx, panelcontent.Selection{Item: podItem("ns", "pod-a")})))
	w.OnSelectionChanged(ctx, panelcontent.Selection{Item: podItem("ns", "pod-b")})

	// pod-a's open finishes after the selection moved on.
	closeCmd := update(t, w, run(t, openA))
	if closeCmd == nil {
		t.Fatal("a stale open must close its stream")
	}
	closeCmd()
	if !a.isClosed() || !src.wasCanceled("pod-a") {
		t.Fatal("stale pod-a stream was not canceled and closed")
	}
	if got := screen(t, w); strings.Contains(got, "a1") {
		t.Fatalf("stale stream shown:\n%s", got)
	}
}

func TestNonPodSelectionShowsHint(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody("a1")})
	w := newWidget(t, src)
	if got := screen(t, w); !strings.Contains(got, logswidget.Hint) {
		t.Fatalf("new widget should show the hint:\n%s", got)
	}

	open(t, w, podItem("ns", "pod-a"))
	deployment := models.NewObjectRow("d", []string{"d"}, nil, deploymentsGVR, "ns", "d", nil)
	if cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: deployment}); cmd != nil {
		t.Fatal("a deployment has no log to open")
	}
	if !src.wasCanceled("pod-a") {
		t.Fatal("moving to a deployment must stop the pod stream")
	}
	if got := screen(t, w); !strings.Contains(got, logswidget.Hint) {
		t.Fatalf("expected the hint:\n%s", got)
	}
	if w.Streaming() {
		t.Fatal("no stream should run for a deployment")
	}
}

func TestEndedStreamIsMarked(t *testing.T) {
	t.Parallel()
	a := newChanBody("a1")
	close(a.lines)
	src := newFakeSource(map[string]*chanBody{"pod-a": a})
	w := newWidget(t, src)

	read := open(t, w, podItem("ns", "pod-a"))
	read = update(t, w, run(t, read)) // a1
	if read == nil {
		t.Fatal("expected the next read")
	}
	if cmd := update(t, w, run(t, read)); cmd != nil { // EOF
		cmd()
	}
	if !a.isClosed() {
		t.Fatal("ended stream was not closed")
	}
	if w.Streaming() {
		t.Fatal("ended stream still reported as streaming")
	}
	info := w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: 80})
	if info.HeaderStatus != "ended" {
		t.Fatalf("HeaderStatus = %q, want ended", info.HeaderStatus)
	}

	// Choosing the mode again restarts an ended stream for the same pod.
	if cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podItem("ns", "pod-a"), Force: true}); cmd == nil {
		t.Fatal("a forced selection should restart an ended stream")
	}
}

func TestLeavingModeStopsStream(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody("a1")})
	w := newWidget(t, src)
	open(t, w, podItem("ns", "pod-a"))

	w.SetFocus(t.Context(), false)
	if !src.wasCanceled("pod-a") {
		t.Fatal("leaving Logs mode must cancel the stream")
	}
	if w.Streaming() {
		t.Fatal("stream still running after the mode was left")
	}
}

func TestBufferedLinesArriveTogether(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody("x1\nx2\nx3")})
	w := newWidget(t, src)

	update(t, w, run(t, open(t, w, podItem("ns", "pod-a"))))
	got := screen(t, w)
	for _, line := range []string{"x1", "x2", "x3"} {
		if !strings.Contains(got, line) {
			t.Fatalf("line %q missing after one read:\n%s", line, got)
		}
	}
}

func TestMessagesOfAnotherWidgetAreIgnored(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody("a1")})
	w1, w2 := newWidget(t, src), newWidget(t, src)

	tick := run(t, w1.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podItem("ns", "pod-a")}))
	if _, handled := w2.Update(t.Context(), tick); handled {
		t.Fatal("a widget must not take another widget's message")
	}
}
