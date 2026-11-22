package logs_test

import (
	"slices"
	"testing"

	panelcontent "github.com/sttts/kc/internal/ui/panelcontent"
	logswidget "github.com/sttts/kc/internal/ui/panelcontent/logs"
)

func (s *fakeSource) lastRequest(t *testing.T) logswidget.Request {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("no stream was opened")
	}
	return s.requests[len(s.requests)-1]
}

// apply sends new settings and runs the restart through the open.
func apply(t *testing.T, w *logswidget.Widget, s logswidget.Settings) {
	t.Helper()
	debounced := run(t, w.SetSettings(s))
	update(t, w, run(t, update(t, w, debounced)))
}

func TestInitialSettingsApply(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody()})
	w := logswidget.New(panelcontent.WidgetDeps{}, logswidget.Options{
		Source:   src.Source,
		After:    immediate,
		Settings: logswidget.Settings{TailLines: 50, SinceSeconds: 300, Timestamps: true},
	})
	open(t, w, podItem("ns", "pod-a"))
	got := src.lastRequest(t).Spec
	if got.TailLines != 50 || got.SinceSeconds != 300 || !got.Timestamps || got.Previous || !got.Follow {
		t.Fatalf("opened %+v, want tail 50, since 300, timestamps, follow", got)
	}
}

func TestZeroTailKeepsRowTail(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody()})
	w := newWidget(t, src)
	open(t, w, podItem("ns", "pod-a"))
	if got := src.lastRequest(t).Spec.TailLines; got != 200 {
		t.Fatalf("tail = %d, want the default 200", got)
	}
}

func TestSetSettingsRestartsStream(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody("a1")})
	w := newWidget(t, src)
	open(t, w, podItem("ns", "pod-a"))

	if cmd := w.SetSettings(w.Settings()); cmd != nil {
		t.Fatal("unchanged settings must keep the stream")
	}
	s := w.Settings()
	s.TailLines, s.Timestamps, s.Previous = -1, true, true
	apply(t, w, s)
	if !src.wasCanceled("pod-a") {
		t.Fatal("the old stream was not canceled")
	}
	got := src.lastRequest(t)
	if got.Spec.TailLines != -1 || !got.Spec.Timestamps || !got.Spec.Previous || got.AllContainers {
		t.Fatalf("reopened with %+v, want the whole log, timestamps and previous", got)
	}
	if w.Settings() != s {
		t.Fatalf("Settings() = %+v, want %+v", w.Settings(), s)
	}
}

func TestSettingsWithoutTargetApplyToNext(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody()})
	w := newWidget(t, src)
	if cmd := w.SetSettings(logswidget.Settings{TailLines: 1000}); cmd != nil {
		t.Fatal("without a target nothing is opened")
	}
	open(t, w, podItem("ns", "pod-a"))
	if got := src.lastRequest(t).Spec.TailLines; got != 1000 {
		t.Fatalf("tail = %d, want 1000", got)
	}
}

func TestAllContainersStaysAcrossPods(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody(), "pod-b": newChanBody()})
	w := newWidget(t, src)
	open(t, w, containerItem("ns", "pod-a", "sidecar"))

	apply(t, w, logswidget.Settings{Container: logswidget.ContainerAll})
	got := src.lastRequest(t)
	if !got.AllContainers || got.Spec.Container != "" || got.Spec.Pod != "pod-a" {
		t.Fatalf("opened %+v, want all containers of pod-a", got)
	}
	if !w.AllContainers() {
		t.Fatal("AllContainers() = false")
	}
	if info := w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: 80}); info.Breadcrumb != "logs:ns/pod-a/all-containers" {
		t.Fatalf("Breadcrumb = %q", info.Breadcrumb)
	}

	open(t, w, podItem("ns", "pod-b"))
	if got := src.lastRequest(t); !got.AllContainers || got.Spec.Pod != "pod-b" {
		t.Fatalf("opened %+v, want all containers of pod-b", got)
	}

	apply(t, w, logswidget.Settings{Container: logswidget.ContainerDefault})
	if got := src.lastRequest(t); got.AllContainers || got.Spec.Container != "" {
		t.Fatalf("requested %+v, want the default container", got)
	}
}

func TestRowChoicesResetOnSelectionChange(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody(), "pod-b": newChanBody()})
	w := newWidget(t, src)
	open(t, w, podItem("ns", "pod-a"))

	apply(t, w, logswidget.Settings{Container: "sidecar", Previous: true, Timestamps: true})
	if got := src.lastRequest(t).Spec; got.Container != "sidecar" || !got.Previous {
		t.Fatalf("opened %+v, want the previous sidecar log", got)
	}
	// The same row again keeps the choices.
	if cmd := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podItem("ns", "pod-a")}); cmd != nil {
		t.Fatal("the same row must keep its stream")
	}

	open(t, w, podItem("ns", "pod-b"))
	got := src.lastRequest(t).Spec
	if got.Container != "" || got.Previous || !got.Timestamps {
		t.Fatalf("opened %+v, want the default container, no previous, timestamps kept", got)
	}
	if s := w.Settings(); s.Container != logswidget.ContainerDefault || s.Previous {
		t.Fatalf("Settings() = %+v, want the row choices cleared", s)
	}
}

func TestContainersKnownAfterOpen(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody(), "pod-b": newChanBody()})
	w := newWidget(t, src)
	if got := w.Containers(); got != nil {
		t.Fatalf("Containers() = %v before any target", got)
	}
	open(t, w, podItem("ns", "pod-a"))
	if got := w.Containers(); !slices.Equal(got, []string{"app", "sidecar"}) {
		t.Fatalf("Containers() = %v, want [app sidecar]", got)
	}
	// The list of pod-a does not apply to pod-b before pod-b opens.
	w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podItem("ns", "pod-b")})
	if got := w.Containers(); got != nil {
		t.Fatalf("Containers() = %v for a pod that has not opened", got)
	}
}

func TestThemeAndWrapPassThrough(t *testing.T) {
	t.Parallel()
	w := newWidget(t, newFakeSource(nil))
	w.SetWrapMode(true)
	if !w.WrapMode() {
		t.Fatal("WrapMode() = false after SetWrapMode(true)")
	}
	w.SetTheme("monokai")
	if got := w.Theme(); got != "monokai" {
		t.Fatalf("Theme() = %q, want monokai", got)
	}
}
