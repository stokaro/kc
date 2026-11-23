package logs_test

import (
	"strings"
	"testing"

	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// rereadNote runs the widget's Reread and returns its note.
func rereadNote(t *testing.T, w *logswidget.Widget) string {
	t.Helper()
	done, ok := run(t, w.Reread(t.Context())).(panelcontent.RereadDoneMsg)
	if !ok || done.Err != nil {
		t.Fatalf("expected a RereadDoneMsg without error, got %#v", done)
	}
	return done.Note
}

func TestRereadKeepsLiveAndEndedStreams(t *testing.T) {
	t.Parallel()
	w := newWidget(t, newFakeSource(nil))
	if got := rereadNote(t, w); got != panelcontent.NothingToReread {
		t.Fatalf("idle note = %q", got)
	}

	a := newChanBody("a1", "a2")
	src := newFakeSource(map[string]*chanBody{"pod-a": a})
	w = newWidget(t, src)
	read := open(t, w, podItem("ns", "pod-a"))
	read = update(t, w, run(t, read))
	if got := rereadNote(t, w); !strings.Contains(got, "live") {
		t.Fatalf("live note = %q", got)
	}
	if !w.Streaming() || len(src.openedPods()) != 1 || src.wasCanceled("pod-a") {
		t.Fatalf("C-r touched the live stream: streaming %v, opened %v", w.Streaming(), src.openedPods())
	}

	close(a.lines)
	for i := 0; w.Streaming() && read != nil && i < 10; i++ {
		read = update(t, w, run(t, read))
	}
	if w.Streaming() {
		t.Fatal("the stream did not end")
	}
	before := screen(t, w)
	if got := rereadNote(t, w); !strings.Contains(got, "ended") {
		t.Fatalf("ended note = %q", got)
	}
	if after := screen(t, w); after != before || !strings.Contains(after, "a1") {
		t.Fatalf("C-r changed the kept lines:\nbefore %q\nafter  %q", before, after)
	}
	if len(src.openedPods()) != 1 {
		t.Fatalf("C-r reopened the stream: %v", src.openedPods())
	}
}
