package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
)

// fakeLogStreams records the specs it is asked to open and hands out
// readers over fixed text.
type fakeLogStreams struct {
	mu      sync.Mutex
	specs   []models.LogsSpec
	text    func(models.LogsSpec) string
	err     error
	readers []*trackedReader
}

type trackedReader struct {
	io.Reader
	closed bool
}

func (r *trackedReader) Close() error { r.closed = true; return nil }

func (f *fakeLogStreams) open(spec models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if f.err != nil {
		return nil, nil, f.err
	}
	r := &trackedReader{Reader: strings.NewReader(f.text(spec))}
	f.readers = append(f.readers, r)
	return r, func() {}, nil
}

// drain runs cmd and feeds the resulting stream messages back into v until
// the stream stops asking for more.
func drain(t *testing.T, v *LogsViewer, cmd tea.Cmd) {
	t.Helper()
	for i := 0; cmd != nil; i++ {
		if i > 100 {
			t.Fatal("stream did not finish after 100 messages")
		}
		_, cmd = v.Update(cmd())
	}
}

func testLogsSpec() models.LogsSpec {
	return models.LogsSpec{Namespace: "web", Pod: "p", Container: "app", Follow: true, TailLines: 200}
}

func TestLogsViewerStreamsAndRestartsWithNewOptions(t *testing.T) {
	t.Parallel()
	streams := &fakeLogStreams{text: func(s models.LogsSpec) string {
		if s.Previous {
			return "old run\n"
		}
		return "one\ntwo\n"
	}}
	v := NewLogsViewer("logs:web/p/app", testLogsSpec(), streams.open, "dracula")
	v.SetCommandBuilder(func(s models.LogsSpec) string { return "kubectl logs " + logOptionsFromSpec(s).summary() })
	v.SetDimensions(80, 20)

	drain(t, v, v.Init())
	if got := v.inner.PlainText(); !strings.Contains(got, "one\ntwo") {
		t.Fatalf("content = %q, want the first stream's lines", got)
	}
	if got, want := v.bar.command, "kubectl logs tail 200"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}

	opts := LogOptions{TailLines: -1, SinceSeconds: 3600, Timestamps: true, Previous: true}
	cmd := v.SetLogOptions(opts)
	if cmd == nil {
		t.Fatal("SetLogOptions with new options returned no command")
	}
	if !streams.readers[0].closed {
		t.Fatal("the first stream was not closed on restart")
	}
	drain(t, v, cmd)

	if len(streams.specs) != 2 {
		t.Fatalf("opened %d streams, want 2", len(streams.specs))
	}
	got := streams.specs[1]
	if got.TailLines != -1 || got.SinceSeconds != 3600 || !got.Timestamps || !got.Previous || !got.Follow || got.Container != "app" {
		t.Fatalf("second stream spec = %+v", got)
	}
	content := v.inner.PlainText()
	if strings.Contains(content, "one") || !strings.Contains(content, "old run") {
		t.Fatalf("content after restart = %q, want only the new stream", content)
	}
	if got, want := v.bar.command, "kubectl logs all lines, since 1h, timestamps, previous"; got != want {
		t.Fatalf("command after restart = %q, want %q", got, want)
	}
	if got := v.statusText(); got != "all lines, since 1h, timestamps, previous, formatted, 1 lines (ended)" {
		t.Fatalf("status = %q", got)
	}

	if cmd := v.SetLogOptions(opts); cmd != nil {
		t.Fatal("SetLogOptions with unchanged options restarted the stream")
	}
}

// stuckReconnectingReader reports a pending reconnect while its stream
// ends: the state a reader had before it cleared the flag on its end.
type stuckReconnectingReader struct {
	io.Reader
	end error
}

func (r *stuckReconnectingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) && r.end != nil {
		err = r.end
	}
	return n, err
}

func (r *stuckReconnectingReader) Close() error       { return nil }
func (r *stuckReconnectingReader) Reconnecting() bool { return true }

// TestLogsViewerTerminalStateWinsOverReconnecting ends a stream whose reader
// still says it reconnects. The footer must report the end, not a retry.
func TestLogsViewerTerminalStateWinsOverReconnecting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		end  error
		want string
	}{
		{name: "failed", end: errors.New("gave up reconnecting"), want: "(failed)"},
		{name: "ended", want: "(ended)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			open := func(models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
				return &stuckReconnectingReader{Reader: strings.NewReader("one\n"), end: tt.end}, func() {}, nil
			}
			v := NewLogsViewer("t", testLogsSpec(), open, "dracula")
			v.SetDimensions(80, 20)
			drain(t, v, v.Init())
			got := v.statusText()
			if !strings.HasSuffix(got, tt.want) {
				t.Fatalf("status = %q, want it to end with %s", got, tt.want)
			}
			if footer := v.FooterStatus(120); strings.Contains(footer, "reconnecting") {
				t.Fatalf("footer = %q, still reconnecting", footer)
			}
		})
	}
}

func TestLogsViewerDropsMessagesOfAReplacedStream(t *testing.T) {
	t.Parallel()
	streams := &fakeLogStreams{text: func(models.LogsSpec) string { return "line\n" }}
	v := NewLogsViewer("t", testLogsSpec(), streams.open, "dracula")

	opened := v.Init()()
	// The user changes options before the first open returns.
	restart := v.SetLogOptions(LogOptions{TailLines: 50})
	if _, cmd := v.Update(opened); cmd != nil {
		t.Fatal("a stale open result started reading")
	}
	if !streams.readers[0].closed {
		t.Fatal("the stale stream was not closed")
	}
	// A line of the old stream must not land in the new one.
	if _, cmd := v.Update(logStreamMsg{viewer: v, gen: 1, lines: []string{"stale"}}); cmd != nil {
		t.Fatal("a stale line asked for more")
	}
	// Nor may a line of another viewer.
	other := NewLogsViewer("o", testLogsSpec(), streams.open, "dracula")
	v.Update(logStreamMsg{viewer: other, gen: v.gen, lines: []string{"foreign"}})
	drain(t, v, restart)
	if got := v.inner.PlainText(); strings.Contains(got, "stale") || strings.Contains(got, "foreign") || !strings.Contains(got, "line") {
		t.Fatalf("content = %q", got)
	}
}

func TestLogsViewerShowsOpenFailure(t *testing.T) {
	t.Parallel()
	streams := &fakeLogStreams{err: errors.New("previous terminated container \"app\" not found")}
	spec := testLogsSpec()
	spec.Previous = true
	v := NewLogsViewer("t", spec, streams.open, "dracula")
	drain(t, v, v.Init())
	if got := v.inner.PlainText(); !strings.Contains(got, "logs stream failed: previous terminated container") {
		t.Fatalf("content = %q, want the error", got)
	}
	if got := v.statusText(); got != "tail 200, previous, formatted, 1 lines (failed)" {
		t.Fatalf("status = %q", got)
	}
}

func TestLogsViewerCloseDropsLateOpen(t *testing.T) {
	t.Parallel()
	streams := &fakeLogStreams{text: func(models.LogsSpec) string { return "x\n" }}
	v := NewLogsViewer("t", testLogsSpec(), streams.open, "dracula")
	opened := v.Init()()
	v.Close()
	if _, cmd := v.Update(opened); cmd != nil {
		t.Fatal("a closed viewer started reading")
	}
	if !streams.readers[0].closed {
		t.Fatal("the stream that opened after Close was not closed")
	}
	if cmd := v.SetLogOptions(LogOptions{TailLines: 50}); cmd != nil {
		t.Fatal("a closed viewer restarted")
	}
}

// TestAppRoutesLogStreamPastTheOptionsDialog proves the stream keeps
// running while F2 covers the viewer: the message reaches the viewer, not
// the dialog on top.
func TestAppRoutesLogStreamPastTheOptionsDialog(t *testing.T) {
	t.Parallel()
	app := NewApp()
	defer app.cancel()
	streams := &fakeLogStreams{text: func(models.LogsSpec) string { return "a\nb\n" }}
	v := NewLogsViewer("t", testLogsSpec(), streams.open, "dracula")
	logs := NewModal("logs", v)
	app.modalManager.Register("logs_viewer", logs)
	app.showModal("logs_viewer")
	app.showViewerOptionsModal(v)
	if top := app.modalManager.GetActiveModal(); top == logs {
		t.Fatal("the options dialog is not on top")
	}

	cmd := v.Init()
	for i := 0; cmd != nil; i++ {
		if i > 100 {
			t.Fatal("stream did not finish")
		}
		_, cmd = app.Update(cmd())
		cmd = onlyStreamCmd(cmd)
	}
	if got := v.inner.PlainText(); !strings.Contains(got, "a\nb") {
		t.Fatalf("content = %q, want both lines", got)
	}
}

// onlyStreamCmd unwraps the batch App.Update returns down to the stream
// command, dropping the app's own background commands.
func onlyStreamCmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch m := msg.(type) {
	case logStreamMsg, logStreamOpenedMsg:
		return func() tea.Msg { return m }
	case tea.BatchMsg:
		for _, c := range m {
			if next := onlyStreamCmd(c); next != nil {
				return next
			}
		}
	}
	return nil
}

func TestApplyViewerOptionsRestartsTheLogsViewer(t *testing.T) {
	t.Parallel()
	app := NewApp()
	defer app.cancel()
	streams := &fakeLogStreams{text: func(models.LogsSpec) string { return "x\n" }}
	v := NewLogsViewer("t", testLogsSpec(), streams.open, "dracula")
	drain(t, v, v.Init())
	app.showViewerOptionsModal(v)

	cmd := app.applyViewerOptions(ViewOptionsCommittedMsg{
		Accept: true,
		Close:  true,
		Viewer: &ViewOptionsViewerPayload{Theme: "dracula", WrapMode: "scroll"},
		Logs:   &ViewOptionsLogsPayload{TailLines: 500, SinceSeconds: 900, Timestamps: true},
	})
	if cmd == nil {
		t.Fatal("changed log options did not restart the stream")
	}
	// Accept also returns the session-only notice; keep the stream command.
	drain(t, v, onlyStreamCmd(cmd))
	if got := streams.specs[len(streams.specs)-1]; got.TailLines != 500 || got.SinceSeconds != 900 || !got.Timestamps || got.Previous {
		t.Fatalf("restarted spec = %+v", got)
	}
	if app.cfg.Viewer.Logs.Tail != 200 {
		t.Fatalf("Accept without save changed the default tail to %d", app.cfg.Viewer.Logs.Tail)
	}
}
