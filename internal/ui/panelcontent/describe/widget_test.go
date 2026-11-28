package describe

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	describe "github.com/stokaro/kc/pkg/describe"
)

var podsGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

func podRow(name string) *models.ObjectRow {
	style := lipgloss.NewStyle()
	return models.NewObjectRow("pod/"+name, []string{name}, []string{"namespaces", "default", "pods", name}, podsGVR, "default", name, &style)
}

// fakeCluster answers describe reads and counts the describe calls.
type fakeCluster struct {
	version   map[string]string
	body      map[string]string
	describes []describe.Target
}

func (f *fakeCluster) describeFunc() panelcontent.DescribeFunc {
	return func(_ string, target describe.Target) panelcontent.DescribeReads {
		return panelcontent.DescribeReads{
			Describe: func() (describe.Result, error) {
				f.describes = append(f.describes, target)
				return describe.Result{Title: "Pod " + target.Name, Body: f.body[target.Name]}, nil
			},
			Version: func() string { return f.version[target.Name] },
		}
	}
}

func newWidget(t *testing.T, f *fakeCluster) *Widget {
	t.Helper()
	w := New(panelcontent.WidgetDeps{Describe: f.describeFunc()})
	// Fire refresh ticks at once instead of waiting a second.
	w.loader.After = func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return fn(time.Now()) }
	}
	w.SetFocus(t.Context(), true)
	w.Resize(t.Context(), panelcontent.Size{Width: 40, Height: 3})
	return w
}

// deliver runs cmd and hands its message to the widget, returning the
// widget's follow-up command.
func deliver(t *testing.T, w *Widget, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatalf("expected a load command, got nil")
	}
	next, handled := w.Update(t.Context(), cmd())
	if !handled {
		t.Fatalf("widget did not handle its load result")
	}
	return next
}

func TestDescribeRunsInTheCommandNotOnSelection(t *testing.T) {
	t.Parallel()
	f := &fakeCluster{body: map[string]string{"a": "body a"}}
	w := newWidget(t, f)

	cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a")})
	if len(f.describes) != 0 {
		t.Fatalf("describe ran on the UI goroutine")
	}
	if got := w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: 40}).HeaderStatus; got != panelcontent.LoadingStatus {
		t.Fatalf("expected loading status while describe runs, got %q", got)
	}
	deliver(t, w, cmd)
	if len(f.describes) != 1 {
		t.Fatalf("expected one describe, got %d", len(f.describes))
	}
	got := f.describes[0]
	if got.GVR != podsGVR || got.Namespace != "default" || got.Name != "a" {
		t.Fatalf("unexpected describe target: %#v", got)
	}
	if !strings.Contains(w.viewer.PlainText(), "body a") {
		t.Fatalf("expected describe output in the viewer, got %q", w.viewer.PlainText())
	}
	if got := w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: 40}).HeaderStatus; got != "" {
		t.Fatalf("expected no status after the load, got %q", got)
	}
}

// A selection without an object shows the hint and the followed location;
// the widget does not keep the last object or look for another row.
func TestDescribeWithoutItemShowsHint(t *testing.T) {
	t.Parallel()
	f := &fakeCluster{body: map[string]string{"a": "body a"}}
	w := New(panelcontent.WidgetDeps{Describe: f.describeFunc(), Path: func() string { return "/own" }})
	w.SetFocus(t.Context(), true)
	w.Resize(t.Context(), panelcontent.Size{Width: 40, Height: 3})
	deliver(t, w, w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a"), Source: "conn-1"}))

	if cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{ID: "__back__", Path: "/pods", Source: "conn-1"}); cmd != nil {
		t.Fatalf("expected no load without an object")
	}
	if got := w.viewer.PlainText(); !strings.Contains(got, "Select a resource to describe.") || strings.Contains(got, "body a") {
		t.Fatalf("expected the hint instead of the last object, got %q", got)
	}
	if got := w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: 40}).Breadcrumb; got != "/pods" {
		t.Fatalf("expected the followed location /pods in the header, got %q", got)
	}
	if len(f.describes) != 1 {
		t.Fatalf("expected no describe after the first, got %#v", f.describes)
	}
}

// The output of one object stays while the next object of the same
// connection loads (ADR 0047). Output read from another connection does
// not: the frame already names the new connection.
func TestDescribeReplacesOutputOfAnotherConnection(t *testing.T) {
	t.Parallel()
	f := &fakeCluster{body: map[string]string{"a": "body a", "b": "body b"}}
	w := newWidget(t, f)
	deliver(t, w, w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a"), Source: "conn-1"}))

	cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("b"), Source: "conn-1"})
	if !strings.Contains(w.viewer.PlainText(), "body a") {
		t.Fatalf("expected a's output while b loads from the same connection, got %q", w.viewer.PlainText())
	}
	deliver(t, w, cmd)

	cmd = w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("b"), Source: "conn-2"})
	if got := w.viewer.PlainText(); strings.Contains(got, "body b") || !strings.Contains(got, "Loading...") {
		t.Fatalf("expected Loading... instead of the output of conn-1, got %q", got)
	}
	deliver(t, w, cmd)
	if !strings.Contains(w.viewer.PlainText(), "body b") {
		t.Fatalf("expected b's output from conn-2, got %q", w.viewer.PlainText())
	}
}

func TestDescribeDropsResultForOldSelection(t *testing.T) {
	t.Parallel()
	f := &fakeCluster{body: map[string]string{"a": "body a", "b": "body b"}}
	w := newWidget(t, f)

	cmdA := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a")})
	if cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("b")}); cmd != nil {
		t.Fatalf("expected b to wait for the describe in flight")
	}
	next := deliver(t, w, cmdA)
	if strings.Contains(w.viewer.PlainText(), "body a") {
		t.Fatalf("output for a must not replace the view after moving to b")
	}
	deliver(t, w, next)
	if !strings.Contains(w.viewer.PlainText(), "body b") {
		t.Fatalf("expected output for b, got %q", w.viewer.PlainText())
	}
}

func TestDescribeRefreshSkipsUnchangedObject(t *testing.T) {
	t.Parallel()
	f := &fakeCluster{
		version: map[string]string{"a": "1"},
		body:    map[string]string{"a": "body a v1"},
	}
	w := newWidget(t, f)
	deliver(t, w, w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a")}))

	// The refresh tick fires within a second; run it and the load it starts.
	refresh := func() {
		t.Helper()
		load := deliver(t, w, w.Refresh(t.Context()))
		deliver(t, w, load)
	}
	refresh()
	if len(f.describes) != 1 {
		t.Fatalf("an unchanged resourceVersion must not describe again, got %d describes", len(f.describes))
	}

	f.version["a"] = "2"
	f.body["a"] = "body a v2"
	refresh()
	if len(f.describes) != 2 {
		t.Fatalf("a new resourceVersion must describe again, got %d describes", len(f.describes))
	}
	if !strings.Contains(w.viewer.PlainText(), "body a v2") {
		t.Fatalf("expected refreshed output, got %q", w.viewer.PlainText())
	}
}

func TestDescribeSameSelectionDoesNotReloadAtOnce(t *testing.T) {
	t.Parallel()
	f := &fakeCluster{body: map[string]string{"a": "body a"}}
	w := newWidget(t, f)
	deliver(t, w, w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a")}))

	// A forced replay of the same object becomes a rate-limited refresh.
	cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a"), Force: true})
	if cmd == nil {
		t.Fatalf("expected a refresh tick")
	}
	if len(f.describes) != 1 {
		t.Fatalf("expected no describe before the tick, got %d", len(f.describes))
	}
	if w.loader.Loading() {
		t.Fatalf("a refresh of shown output must not show the loading status")
	}
}

func TestDescribeWithoutProvider(t *testing.T) {
	t.Parallel()
	w := New(panelcontent.WidgetDeps{})
	w.SetFocus(t.Context(), true)
	if cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a")}); cmd != nil {
		t.Fatalf("expected no load without a provider")
	}
	if !strings.Contains(w.viewer.PlainText(), "unavailable") {
		t.Fatalf("expected an unavailable notice, got %q", w.viewer.PlainText())
	}
}
