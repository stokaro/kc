package logs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/investigation"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

var spanNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func newSpanWidget(t *testing.T, src *fakeSource) *logswidget.Widget {
	t.Helper()
	w := logswidget.New(panelcontent.WidgetDeps{}, logswidget.Options{Source: src.Source, After: immediate, Now: func() time.Time { return spanNow }})
	w.Resize(t.Context(), panelcontent.Size{Width: 120, Height: 10})
	return w
}

func header(t *testing.T, w *logswidget.Widget) string {
	t.Helper()
	return w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: 120}).HeaderStatus
}

// A live window reads the log from its start and keeps following; the
// panel's own since and tail give way to it.
func TestLiveWindowStartsTheStream(t *testing.T) {
	t.Parallel()
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody("a1")})
	w := newSpanWidget(t, src)
	_ = w.SetSettings(logswidget.Settings{SinceSeconds: 60, TailLines: 50})
	_ = w.SetSpan(logswidget.Span{Window: investigation.Window{Last: time.Hour}, Loc: time.UTC})

	update(t, w, run(t, open(t, w, podItem("ns", "pod-a"))))
	spec := src.opened[0]
	if !spec.SinceTime.Equal(spanNow.Add(-time.Hour)) || spec.SinceSeconds != 0 || spec.TailLines != -1 || !spec.Follow {
		t.Fatalf("spec = %+v, want since-time 11:00, whole window, follow", spec)
	}
	if got := header(t, w); !strings.Contains(got, "window last 1h") {
		t.Fatalf("header = %q", got)
	}
	// Turning the window off goes back to the panel's settings.
	restart := w.SetSpan(logswidget.Span{})
	update(t, w, run(t, update(t, w, run(t, restart))))
	if spec := src.opened[1]; !spec.SinceTime.IsZero() || spec.SinceSeconds != 60 || spec.TailLines != 50 {
		t.Fatalf("spec after off = %+v", spec)
	}
}

// A window that ended does not follow, turns timestamps on and hides the
// lines after its end. kc's own notes stay; a merged stream's prefix is
// read past.
func TestFixedWindowHidesLinesAfterTheEnd(t *testing.T) {
	t.Parallel()
	body := newChanBody(
		"2026-11-23T10:00:05Z first",
		"[pod-a/app] 2026-11-23T10:59:59.5Z inside",
		"2026-11-23T11:00:01Z after",
		"no timestamp",
	)
	close(body.lines)
	src := newFakeSource(map[string]*chanBody{"pod-a": body})
	w := newSpanWidget(t, src)
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	_ = w.SetSpan(logswidget.Span{Window: investigation.Window{Start: start, End: start.Add(time.Hour)}, Loc: time.UTC})

	next := open(t, w, podItem("ns", "pod-a"))
	for next != nil {
		msg := run(t, next)
		if msg == nil { // the closed stream's cleanup
			break
		}
		next = update(t, w, msg)
	}
	spec := src.opened[0]
	if !spec.SinceTime.Equal(start) || spec.Follow || !spec.Timestamps {
		t.Fatalf("spec = %+v, want since 10:00, no follow, timestamps", spec)
	}
	got := screen(t, w)
	for _, want := range []string{"first", "inside", "no timestamp", "[the log stream ended]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("screen lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "after") {
		t.Fatalf("a line after the window's end is shown:\n%s", got)
	}
	if h := header(t, w); !strings.Contains(h, "window 2026-11-23 10:00:00 .. 11:00:00 +00:00") || !strings.Contains(h, "1 after the end hidden") {
		t.Fatalf("header = %q", h)
	}
}

// When the log starts well after the window, the header says so: the
// kubelet no longer has the beginning.
func TestWindowCoverageNamesTheFirstLine(t *testing.T) {
	t.Parallel()
	body := newChanBody("2026-11-23T11:30:00Z late")
	src := newFakeSource(map[string]*chanBody{"pod-a": body})
	w := newSpanWidget(t, src)
	_ = w.SetSettings(logswidget.Settings{Timestamps: true})
	_ = w.SetSpan(logswidget.Span{Window: investigation.Window{Last: 2 * time.Hour}, Loc: time.UTC})
	update(t, w, run(t, open(t, w, podItem("ns", "pod-a"))))
	if h := header(t, w); !strings.Contains(h, "first line 11:30:00") {
		t.Fatalf("header = %q", h)
	}
}
