package ui

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// lineBody is a followed log body fed line by line through a channel.
type lineBody struct {
	lines chan string
	buf   string
}

func (b *lineBody) Read(p []byte) (int, error) {
	if b.buf == "" {
		s, ok := <-b.lines
		if !ok {
			return 0, io.EOF
		}
		b.buf = s + "\n"
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

func (b *lineBody) Close() error { return nil }

// expandFixture is an app whose right panel is in Logs mode and streams a
// pod log from body.
type expandFixture struct {
	app    *App
	w      *logswidget.Widget
	body   *lineBody
	read   tea.Cmd
	mu     sync.Mutex
	opens  int
	cancel bool
}

func newExpandFixture(t *testing.T, lines ...string) *expandFixture {
	t.Helper()
	f := &expandFixture{app: NewApp(), body: &lineBody{lines: make(chan string, 64)}}
	t.Cleanup(f.app.cancel)
	f.app.width, f.app.height = 100, 30
	f.app.setPanelMode(t.Context(), f.app.rightPanel, PanelModeLogs, "test")
	f.w = logswidget.New(panelcontent.WidgetDeps{}, logswidget.Options{
		Source: func(req logswidget.Request) logswidget.Open {
			return func() (logswidget.Stream, error) {
				f.mu.Lock()
				defer f.mu.Unlock()
				f.opens++
				spec := req.Spec
				spec.Container = "app"
				return logswidget.Stream{Spec: spec, Body: f.body, Cancel: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					f.cancel = true
				}}, nil
			}
		},
		After: func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			return func() tea.Msg { return fn(time.Time{}) }
		},
	})
	f.w.Resize(t.Context(), panelcontent.Size{Width: 80, Height: 10})
	f.app.rightPanel.widgets[PanelModeLogs] = f.w
	debounced := f.w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: testPodItem()})()
	openCmd, _ := f.w.Update(t.Context(), debounced)
	f.read, _ = f.w.Update(t.Context(), openCmd())
	for _, l := range lines {
		f.panelReads(t, l)
	}
	return f
}

// panelReads has the panel read line from its stream.
func (f *expandFixture) panelReads(t *testing.T, line string) {
	t.Helper()
	f.body.lines <- line
	f.read, _ = f.w.Update(t.Context(), f.read())
}

func (f *expandFixture) streamState() (opens int, canceled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.cancel
}

// expandedViewer returns the full-screen viewer F3 opened.
func expandedViewer(t *testing.T, app *App) *LogsViewer {
	t.Helper()
	m := app.modalManager.GetActiveModal()
	if m == nil {
		t.Fatal("no full-screen viewer open")
	}
	v, ok := m.content.(*LogsViewer)
	if !ok {
		t.Fatalf("top modal shows %T, want the logs viewer", m.content)
	}
	return v
}

// runWithin runs cmd and fails when it does not return in time.
func runWithin(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("the command did not return within 5s")
	}
	return nil
}

// TestExpandLogsAdoptsThePanelSession presses F3 in a Logs panel: the
// full-screen viewer starts with the panel's lines and follows its stream
// without opening another, and closing the viewer leaves the panel as it
// was.
func TestExpandLogsAdoptsThePanelSession(t *testing.T) {
	f := newExpandFixture(t, "a1", "a2")
	app := f.app
	app.activePanel = 1
	if caps := app.capabilitiesForPanel(app.rightPanel); !caps.CanView {
		t.Fatal("F3 must be enabled in a Logs panel with a stream")
	}
	keys := panelFunctionKeys(functionBarState{PanelMode: PanelModeLogs, Capabilities: app.capabilitiesForPanel(app.rightPanel)})
	if k := keys[2]; k.Label != "Expand" || !k.Enabled {
		t.Fatalf("F3 is %+v, want an enabled Expand", k)
	}

	if item := app.fileMenuItems()[0]; item.Label != "&Expand logs" || item.Disabled {
		t.Fatalf("File menu starts with %+v, want an enabled Expand logs", item)
	}

	start := app.openViewerForPanel(app.rightPanel)
	v := expandedViewer(t, app)
	if got := v.inner.PlainText(); got != "a1\na2" {
		t.Fatalf("viewer starts with %q, want the panel's a1 a2", got)
	}
	if cmd := v.bar.command; !strings.Contains(cmd, "logs web -n ns -c app") || strings.Contains(cmd, "--all-containers") {
		t.Fatalf("command %q, want the panel's container stream", cmd)
	}
	_, readCmd := v.Update(runWithin(t, start))

	f.panelReads(t, "a3")
	_, readCmd = v.Update(runWithin(t, readCmd))
	if got := v.inner.PlainText(); got != "a1\na2\na3" {
		t.Fatalf("viewer shows %q, want a3 after a1 a2, once", got)
	}
	if opens, _ := f.streamState(); opens != 1 {
		t.Fatalf("%d streams opened, want the panel's one", opens)
	}

	// Closing the viewer closes only the follower.
	app.modalManager.GetActiveModal().onClose()
	app.hideModal()
	if msg := runWithin(t, readCmd); msg != nil {
		v.Update(msg)
	}
	f.panelReads(t, "a4")
	if _, canceled := f.streamState(); canceled || !f.w.Streaming() {
		t.Fatal("closing the expanded viewer stopped the panel stream")
	}
	panel := ansi.Strip(f.w.View(t.Context(), panelcontent.Frame{Size: panelcontent.Size{Width: 80, Height: 10}}).Content)
	for _, want := range []string{"a1", "a2", "a3", "a4"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("panel lost %s:\n%s", want, panel)
		}
	}
}

// TestExpandLogsWithoutSession checks F3 in a Logs panel that shows the
// hint: it is disabled.
func TestExpandLogsWithoutSession(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.cancel)
	app.setPanelMode(t.Context(), app.rightPanel, PanelModeLogs, "test")
	if caps := app.capabilitiesForPanel(app.rightPanel); caps.CanView {
		t.Fatal("F3 must be disabled while the Logs panel shows no log")
	}
}

// TestExpandedViewerSoloKeepsCollecting checks M-m on a merged log: only
// the focused source is shown, the other sources' records are still kept,
// and M-m again shows them, with no stream reopened.
func TestExpandedViewerSoloKeepsCollecting(t *testing.T) {
	t.Parallel()
	v := NewLogsViewer("t", testLogsSpec(), nil, "dracula")
	v.SetDimensions(100, 20)
	v.SetStyledLines(true)
	v.appendRecords([]string{"[p/app] a1", "[p/side] s1", "[p/app] a2"})

	// Focus the last line: the viewer follows, so it is a2 of p/app.
	v.Update(altKey('m', false))
	if got := v.inner.PlainText(); strings.Contains(got, "s1") || !strings.Contains(got, "a1") {
		t.Fatalf("solo p/app shows %q", got)
	}
	if !strings.Contains(v.statusText(), "solo: p/app") || !strings.Contains(v.statusText(), "2 of 3 lines") {
		t.Fatalf("status %q does not name the solo source", v.statusText())
	}
	v.appendRecords([]string{"[p/side] s2", "[p/app] a3"})
	if got := v.inner.PlainText(); strings.Contains(got, "s2") || !strings.Contains(got, "a3") {
		t.Fatalf("new records under solo: %q", got)
	}
	if v.log.Len() != 5 {
		t.Fatalf("kept %d records, want all 5 while solo hides 2", v.log.Len())
	}

	v.Update(altKey('m', false))
	if got := v.inner.PlainText(); !strings.Contains(got, "s1") || !strings.Contains(got, "s2") {
		t.Fatalf("clearing solo shows %q, want the side records back", got)
	}
	if _, on := v.log.Solo(); on {
		t.Fatal("solo still on")
	}

	single := NewLogsViewer("t", testLogsSpec(), nil, "dracula")
	single.appendRecords([]string{"a"})
	single.Update(altKey('m', false))
	if _, on := single.log.Solo(); on || !strings.Contains(single.note, "merged") {
		t.Fatalf("solo on a single log: on=%v note=%q", on, single.note)
	}
}
