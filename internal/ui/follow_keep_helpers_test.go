package ui

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// logStreams records the streams the Logs mode of a panel opens through a
// fake opener (useFakeLogStreams).
type logStreams struct {
	mu       sync.Mutex
	opened   []logswidget.Request
	canceled int
}

// counts returns how many streams were opened and canceled.
func (s *logStreams) counts() (opened, canceled int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.opened), s.canceled
}

// useFakeLogStreams makes the Logs mode of panel open fake streams. Each
// sends one line, "log of <namespace>/<pod>", and stays open until it is
// canceled. The debounce delay is left out.
func useFakeLogStreams(t *testing.T, panel *Panel) *logStreams {
	t.Helper()
	s := &logStreams{}
	panel.RegisterMode(PanelModeLogs, func(p *Panel) PanelWidget {
		return logswidget.New(p.manifestWidgetDeps(), logswidget.Options{
			Source: func(req logswidget.Request) logswidget.Open {
				return func() (logswidget.Stream, error) {
					body := newFakeLogBody("log of " + req.Spec.Namespace + "/" + req.Spec.Pod + "\n")
					t.Cleanup(body.close)
					s.mu.Lock()
					s.opened = append(s.opened, req)
					s.mu.Unlock()
					spec := req.Spec
					if spec.Container == "" && !req.AllContainers {
						spec.Container = "app"
					}
					return logswidget.Stream{Spec: spec, Containers: []string{"app"}, Body: body, Cancel: func() {
						s.mu.Lock()
						s.canceled++
						s.mu.Unlock()
						body.close()
					}}, nil
				}
			},
			After: func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
				return func() tea.Msg { return fn(time.Time{}) }
			},
		})
	})
	return s
}

// fakeLogBody yields its line, then blocks until it is closed.
type fakeLogBody struct {
	mu   sync.Mutex
	rest []byte
	done chan struct{}
	once sync.Once
}

func newFakeLogBody(line string) *fakeLogBody {
	return &fakeLogBody{rest: []byte(line), done: make(chan struct{})}
}

func (b *fakeLogBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if len(b.rest) > 0 {
		n := copy(p, b.rest)
		b.rest = b.rest[n:]
		b.mu.Unlock()
		return n, nil
	}
	b.mu.Unlock()
	<-b.done
	return 0, io.EOF
}

func (b *fakeLogBody) Close() error {
	b.close()
	return nil
}

func (b *fakeLogBody) close() { b.once.Do(func() { close(b.done) }) }

// assertFrameNames fails unless the right panel's frame names context
// conn and namespace scope ns.
func assertFrameNames(t *testing.T, app *App, conn *panelConn, ns string) {
	t.Helper()
	if s := app.rightPanel.scope; s.Context != conn.name() || s.Namespace != ns {
		t.Fatalf("the follower's frame names %s : %s, want %s : %s", s.Context, s.Namespace, conn.name(), ns)
	}
	text := followerText(t, app)
	if !strings.Contains(text, " "+conn.name()+" ") || !strings.Contains(text, " "+namespaceScopeLabel(ns)+" ") {
		t.Fatalf("the follower's frame does not read %s : %s:\n%s", conn.name(), ns, text)
	}
}

// assertActsOn fails unless the right panel acts on pod ns/name of conn:
// the row it hands to actions and the connection that row is read from.
func assertActsOn(t *testing.T, app *App, conn *panelConn, ns, name string) {
	t.Helper()
	cur := app.rightPanel.GetCurrentItem()
	if cur == nil {
		t.Fatalf("the follower acts on nothing, want %s/%s", ns, name)
	}
	if obj, ok := cur.Item.(models.ObjectItem); !ok || obj.Namespace() != ns || obj.Name() != name {
		t.Fatalf("the follower acts on %v, want %s/%s", cur.Item, ns, name)
	}
	item, c := app.selectedRow(t.Context(), app.rightPanel)
	if item == nil || c != conn {
		t.Fatalf("the follower's row is %v of connection %q, want %s/%s of %q", item, c.connID(), ns, name, conn.connID())
	}
}

// assertLogsStream fails unless the right panel streams the log of pod
// ns/name read from conn, under a frame that names conn and ns, after
// opened streams of which canceled were stopped.
func assertLogsStream(t *testing.T, app *App, streams *logStreams, conn *panelConn, ns, name string, opened, canceled int) {
	t.Helper()
	w := app.rightPanel.logsWidget()
	if w == nil {
		t.Fatal("the right panel has no Logs widget")
	}
	if got := w.Target(); got.Spec.Namespace != ns || got.Spec.Pod != name || got.Source != conn.connID() {
		t.Fatalf("Logs follows %s/%s of connection %q, want %s/%s of %q", got.Spec.Namespace, got.Spec.Pod, got.Source, ns, name, conn.connID())
	}
	if !w.Streaming() {
		t.Fatalf("Logs does not stream %s/%s", ns, name)
	}
	if o, c := streams.counts(); o != opened || c != canceled {
		t.Fatalf("Logs opened %d streams and canceled %d, want %d and %d", o, c, opened, canceled)
	}
	assertFrameNames(t, app, conn, ns)
	if text := followerText(t, app); !strings.Contains(text, "log of "+ns+"/"+name) {
		t.Fatalf("Logs does not show the log of %s/%s:\n%s", ns, name, text)
	}
	assertActsOn(t, app, conn, ns, name)
}
