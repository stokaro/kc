package ui

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/logview"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// feedLogs hands lines to v as the stream reader would, in one message.
func feedLogs(v *LogsViewer, lines ...string) {
	v.Update(logStreamMsg{viewer: v, gen: v.gen, lines: lines})
}

func newRetentionViewer(t *testing.T, limits logview.Limits) *LogsViewer {
	t.Helper()
	v := NewLogsViewer("logs:ns/p/c", models.LogsSpec{}, nil, "")
	v.SetDimensions(80, 20)
	v.inner.Resize(80, 18)
	v.SetLimits(limits)
	return v
}

func logCopyRequest(t *testing.T, cmd tea.Cmd) viewer.CopyRequestMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a copy command")
	}
	msg, ok := cmd().(viewer.CopyRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want a copy request", msg)
	}
	return msg
}

func TestLogsViewerDropsOldestRecords(t *testing.T) {
	t.Parallel()
	v := newRetentionViewer(t, logview.Limits{MaxRecords: 50})
	for i := range 200 {
		if i%2 == 0 {
			feedLogs(v, fmt.Sprintf(`{"level":"error","msg":"fail %d"}`, i))
		} else {
			feedLogs(v, fmt.Sprintf("plain %d", i))
		}
		if got, want := len(plainLines(v)), v.log.Shown(); v.log.Len() > 50 || got != want {
			t.Fatalf("after line %d: Len %d, %d display lines, Shown %d", i, v.log.Len(), got, want)
		}
	}
	kept := v.log.Len()
	lines := plainLines(v)
	oldest := fmt.Sprintf("plain %d", 200-kept)
	if (200-kept)%2 == 0 {
		oldest = fmt.Sprintf("ERROR fail %d", 200-kept)
	}
	if lines[len(lines)-1] != "plain 199" || lines[0] != oldest {
		t.Fatalf("display = %q ... %q, want the newest %d lines", lines[0], lines[len(lines)-1], kept)
	}
	status := v.statusText()
	if want := fmt.Sprintf("%d lines (%d older dropped)", kept, 200-kept); !strings.Contains(status, want) {
		t.Fatalf("statusText() = %q, want %q", status, want)
	}

	// Copies cover the kept records and say that older ones are missing.
	orig := logCopyRequest(t, func() tea.Cmd { _, cmd := v.Update(altKey('A', false)); return cmd }())
	if !strings.Contains(orig.What, "older dropped") || strings.Count(orig.Text, "\n")+1 != kept {
		t.Fatalf("M-A copied %q with %d lines", orig.What, strings.Count(orig.Text, "\n")+1)
	}
	if !strings.HasPrefix(orig.Text, `{"level":"error","msg":"fail `) && !strings.HasPrefix(orig.Text, "plain ") {
		t.Fatalf("M-A text starts with %q", orig.Text[:20])
	}
	shown := logCopyRequest(t, func() tea.Cmd { _, cmd := v.Update(altKey('a', false)); return cmd }())
	if !strings.Contains(shown.What, "older dropped") {
		t.Fatalf("M-a What = %q", shown.What)
	}

	// The filter, the raw view and wrapping work on the kept records.
	v.SetQuery(logview.MustParseQuery("level=error"))
	if got := plainLines(v); len(got) != kept/2 || !strings.HasPrefix(got[0], "ERROR fail ") {
		t.Fatalf("filtered display = %q", got)
	}
	v.Update(altKey('v', false))
	if got := plainLines(v); len(got) != kept/2 || !strings.HasPrefix(got[0], `{"level":"error"`) {
		t.Fatalf("raw filtered display = %q", got)
	}
	v.SetWrapMode(true)
	feedLogs(v, `{"level":"error","msg":"newest"}`)
	if got := plainLines(v); got[len(got)-1] != `{"level":"error","msg":"newest"}` || len(got) != v.log.Shown() {
		t.Fatalf("display after a new line under the filter: %d lines, Shown %d", len(got), v.log.Shown())
	}

	// A restarted stream starts with nothing dropped.
	v.log.Reset()
	if strings.Contains(v.statusText(), "dropped") {
		t.Fatalf("statusText() after Reset = %q", v.statusText())
	}
}

func TestLogsViewerReportsAMovedView(t *testing.T) {
	t.Parallel()
	v := newRetentionViewer(t, logview.Limits{MaxRecords: 40})
	for i := range 30 {
		feedLogs(v, fmt.Sprintf("line %d", i))
	}
	v.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if v.autoFollow {
		t.Fatal("home must pause following")
	}
	for i := 30; i < 41; i++ {
		feedLogs(v, fmt.Sprintf("line %d", i))
	}
	if !strings.Contains(v.statusText(), "view moved") {
		t.Fatalf("statusText() = %q, want the moved-view notice", v.statusText())
	}
	if got := plainLines(v)[v.inner.Offset()]; got != "line 5" {
		t.Fatalf("top line = %q, want the oldest kept, line 5", got)
	}
	v.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if strings.Contains(v.statusText(), "view moved") {
		t.Fatalf("a scroll must clear the notice: %q", v.statusText())
	}
	// A view below the dropped lines keeps its line.
	for i := 41; i < 60; i++ {
		feedLogs(v, fmt.Sprintf("line %d", i))
	}
	top := plainLines(v)[v.inner.Offset()]
	for i := 60; i < 80; i++ {
		feedLogs(v, fmt.Sprintf("line %d", i))
	}
	if got := plainLines(v)[v.inner.Offset()]; got != top && !strings.Contains(v.statusText(), "view moved") {
		t.Fatalf("top line moved from %q to %q without a notice", top, got)
	}
}

func TestLogsViewerTruncatesOversizedLines(t *testing.T) {
	t.Parallel()
	v := newRetentionViewer(t, logview.DefaultLimits())
	huge := `{"msg":"` + strings.Repeat("x", logview.ReadLimit) + `"}`
	feedLogs(v, huge, "next")
	recs := v.log.Records()
	if len(recs) != 2 || !logview.IsTruncated(recs[0].Raw) || recs[0].IsJSON() || recs[1].Raw != "next" {
		t.Fatalf("records = %d, first truncated %v", len(recs), logview.IsTruncated(recs[0].Raw))
	}
	if !strings.Contains(v.statusText(), "1 truncated") {
		t.Fatalf("statusText() = %q, want the truncated count", v.statusText())
	}
}

// TestLogsViewerMemoryStaysBounded streams far more log than the budget
// through a viewer and checks the heap after a collection. Without the
// limits the viewer would keep all of it. The test is not parallel so that
// other tests do not allocate while it measures.
func TestLogsViewerMemoryStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 64 MiB of log")
	}
	const budget = 8 << 20
	v := newRetentionViewer(t, logview.Limits{MaxRecords: 1 << 30, MaxBytes: budget})
	// Search and filter indexes take part: a search marks matches on
	// every new line, and a filter decides what is shown.
	v.SetQuery(logview.MustParseQuery("!healthz"))
	setViewerSearch(t, v.inner, "request")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	huge := strings.Repeat("h", 400<<10)
	pad := strings.Repeat("p", 200)
	streamed := 0
	batch := make([]string, 0, 1000)
	for i := 0; streamed < 64<<20; i++ {
		var line string
		switch {
		case i%5000 == 0:
			line = "oversized " + huge
		case i%3 == 0:
			line = fmt.Sprintf(`{"time":"2026-11-22T10:00:00Z","level":"info","msg":"request %d","pad":"%s"}`, i, pad)
		default:
			line = fmt.Sprintf("text request %d %s", i, pad)
		}
		streamed += len(line)
		batch = append(batch, line)
		if len(batch) == cap(batch) {
			feedLogs(v, batch...)
			batch = batch[:0]
		}
	}
	feedLogs(v, batch...)
	if v.log.Bytes() > budget {
		t.Fatalf("log accounts %d bytes, over the budget of %d", v.log.Bytes(), budget)
	}
	if v.log.Dropped() == 0 {
		t.Fatal("nothing was dropped; the stream did not reach the budget")
	}

	runtime.GC()
	runtime.ReadMemStats(&after)
	var growth uint64
	if after.HeapAlloc > before.HeapAlloc {
		growth = after.HeapAlloc - before.HeapAlloc
	}
	t.Logf("streamed %d MiB; kept %d records, %d MiB accounted; heap grew %d MiB", streamed>>20, v.log.Len(), v.log.Bytes()>>20, growth>>20)
	if limit := uint64(3 * budget); growth > limit {
		t.Fatalf("heap grew by %d bytes for a budget of %d; want at most %d", growth, budget, limit)
	}
	runtime.KeepAlive(v)
}

// setViewerSearch commits a search on w as the search dialog does.
func setViewerSearch(t *testing.T, w *viewer.Widget, query string) {
	t.Helper()
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: tea.KeyF7}} {
		w.Update(msg)
	}
	for _, r := range query {
		w.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	w.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !w.HasSearchMatches() && !strings.Contains(w.FooterStatusText(80), query) {
		t.Fatalf("search %q was not committed", query)
	}
}
