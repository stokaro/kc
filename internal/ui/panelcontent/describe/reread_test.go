package describe

import (
	"strings"
	"testing"

	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// The test is white-box like the rest of the package: it reads the viewer
// text and the loader, which the widget does not export.

func TestDescribeRereadDescribesUnchangedObjectAgain(t *testing.T) {
	t.Parallel()
	f := &fakeCluster{
		version: map[string]string{"a": "1"},
		body:    map[string]string{"a": "body a, no events"},
	}
	w := newWidget(t, f)
	deliver(t, w, w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: podRow("a")}))

	// An event arrived; the object and its resourceVersion did not change.
	f.body["a"] = "body a, one event"
	cmd := w.Reread(t.Context())
	if got := w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: 40}).HeaderStatus; got != panelcontent.LoadingStatus {
		t.Fatalf("expected the loading status while the reread runs, got %q", got)
	}
	next := deliver(t, w, cmd)
	if len(f.describes) != 2 {
		t.Fatalf("a reread must describe again with the same resourceVersion, got %d describes", len(f.describes))
	}
	if !strings.Contains(w.viewer.PlainText(), "one event") {
		t.Fatalf("expected the reread output, got %q", w.viewer.PlainText())
	}
	if got := w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: 40}).HeaderStatus; got != "" {
		t.Fatalf("expected no status after the reread, got %q", got)
	}
	if next == nil {
		t.Fatalf("expected the reread to report that it finished")
	}
	done, ok := next().(panelcontent.RereadDoneMsg)
	if !ok || done.Err != nil {
		t.Fatalf("expected a successful RereadDoneMsg, got %#v", done)
	}
}

func TestDescribeRereadWithoutSelection(t *testing.T) {
	t.Parallel()
	w := newWidget(t, &fakeCluster{})
	cmd := w.Reread(t.Context())
	if cmd == nil {
		t.Fatalf("expected a note")
	}
	if done, ok := cmd().(panelcontent.RereadDoneMsg); !ok || done.Note != panelcontent.NothingToReread {
		t.Fatalf("expected the nothing-to-reread note, got %#v", done)
	}
}
