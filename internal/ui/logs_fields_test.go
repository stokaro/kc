package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logview"
	"github.com/stokaro/kc/pkg/appconfig"
)

func mergedSample() string {
	line := func(pod, text string) string {
		return logmux.Format(logmux.Line{Target: logmux.Target{Pod: pod, Container: "app"}, Text: text}, false) + "\n"
	}
	return line("api-1", `{"msg":"start","trace_id":"t-1"}`) +
		line("api-2", `{"msg":"db","trace_id":"t-2"}`) +
		line("api-1", `{"msg":"query","trace_id":"t 1,\"x\" .*"}`) +
		line("api-2", `plain text t-1`) +
		line("api-1", `{"msg":"done","trace_id":"t-1"}`)
}

func newFieldsViewer(t *testing.T) *LogsViewer {
	t.Helper()
	v := newTestLogsViewer(t, mergedSample())
	v.SetStyledLines(true)
	v.SetFieldOptions(appconfig.Default().Viewer.Logs)
	streamAll(t, v)
	return v
}

func pressRune(d *logsFieldsDialog, r rune) {
	d.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
}

func seqOf(t *testing.T, v *LogsViewer, msg string) logview.Seq {
	t.Helper()
	for i := range v.log.Len() {
		seq, ok := v.log.SeqAt(i)
		if !ok {
			continue
		}
		r, _ := v.log.Record(seq)
		if strings.Contains(r.Raw, `"msg":"`+msg+`"`) || r.Raw == msg {
			return seq
		}
	}
	t.Fatalf("no shown record %q", msg)
	return 0
}

// TestLogsFieldsFollowAndReturn follows a trace ID from the line in focus,
// checks the scope in the status line and returns with M-z.
func TestLogsFieldsFollowAndReturn(t *testing.T) {
	v := newFieldsViewer(t)
	seq, ok := v.focusRecord()
	if !ok {
		t.Fatal("no record in focus")
	}
	closed := false
	d, err := newLogsFieldsDialog(v, seq, func() { closed = true })
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := d.selected(); f.Path.String() != ".trace_id" || f.Value != "t-1" {
		t.Fatalf("the dialog starts on %s=%s, want the correlation field", f.Path, f.Value)
	}
	pressRune(d, 'f')
	if !closed {
		t.Fatalf("Follow did not close the dialog: %q", d.note)
	}
	if got := v.Query(); got != `.trace_id=="t-1"` {
		t.Fatalf("query = %q", got)
	}
	if got := plainLines(v); len(got) != 2 {
		t.Fatalf("shown %q, want the two t-1 lines and not the text line that mentions it", got)
	}
	if got := v.statusText(); !strings.Contains(got, "following .trace_id=t-1 in the 5 kept lines, M-z returns") {
		t.Fatalf("statusText() = %q", got)
	}

	v.handleLogViewKey(altKey('z', false))
	if v.Query() != "" || len(plainLines(v)) != 5 || strings.Contains(v.statusText(), "following") {
		t.Fatalf("M-z did not return: query %q, %d lines", v.Query(), len(plainLines(v)))
	}
	if v.autoFollow {
		t.Fatal("M-z must keep the selected record in view, not follow the end")
	}
}

// TestLogsFieldsSpecialValues filters by a value with a space, a comma,
// quotes and regex characters, and excludes another one.
func TestLogsFieldsSpecialValues(t *testing.T) {
	v := newFieldsViewer(t)
	d, err := newLogsFieldsDialog(v, seqOf(t, v, "query"), func() {})
	if err != nil {
		t.Fatal(err)
	}
	pressRune(d, 'f')
	if got := plainLines(v); len(got) != 1 || !strings.Contains(got[0], "query") {
		t.Fatalf("shown %q, want only the query line", got)
	}
	v.back()

	d, _ = newLogsFieldsDialog(v, seqOf(t, v, "db"), func() {})
	pressRune(d, '-')
	if got := v.Query(); got != `!.trace_id=="t-2"` {
		t.Fatalf("query = %q", got)
	}
	if got := plainLines(v); len(got) != 4 {
		t.Fatalf("shown %q, want all but the t-2 line", got)
	}
	// Include adds to the filter.
	d, _ = newLogsFieldsDialog(v, seqOf(t, v, "start"), func() {})
	d.cursor = 0 // .msg
	pressRune(d, '=')
	if got := v.Query(); got != `!.trace_id=="t-2" .msg=="start"` {
		t.Fatalf("query = %q", got)
	}
	v.back()
	v.back()
	if v.Query() != "" {
		t.Fatalf("two M-z must return to no filter, got %q", v.Query())
	}
}

func TestLogsFieldsContext(t *testing.T) {
	v := newFieldsViewer(t)
	q, _ := logview.ParseQuery(`.trace_id=="t-1"`)
	v.SetQuery(q)
	anchor := seqOf(t, v, "done")
	if err := v.showContext(anchor, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"* [api-1/app] start trace_id=t-1",
		"  [api-2/app] db trace_id=t-2",
		`  [api-1/app] query trace_id="t 1,\"x\" .*"`,
		"  [api-2/app] plain text t-1",
		"> [api-1/app] done trace_id=t-1",
	}
	if got := plainLines(v); !equalLines(got, want) {
		t.Fatalf("context =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(v.statusText(), "context: 5 lines around the line, all sources, M-z returns") {
		t.Fatalf("statusText() = %q", v.statusText())
	}
	// New lines, even ones that pass the filter, do not move the context
	// view.
	v.appendRecords([]string{logmux.Format(logmux.Line{Target: logmux.Target{Pod: "api-1", Container: "app"}, Text: `{"msg":"late","trace_id":"t-1"}`}, false)})
	if got := plainLines(v); len(got) != 5 {
		t.Fatalf("the context changed with a new line: %q", got)
	}

	v.back()
	if v.Query() != `.trace_id=="t-1"` || len(plainLines(v)) != 3 {
		t.Fatalf("M-z after the context: query %q, lines %q", v.Query(), plainLines(v))
	}

	if err := v.showContext(anchor, true); err != nil {
		t.Fatal(err)
	}
	for _, l := range plainLines(v) {
		if strings.Contains(l, "api-2") {
			t.Fatalf("same-source context shows another source: %q", plainLines(v))
		}
	}
	if got := plainLines(v); len(got) != 4 || !strings.Contains(got[3], "late") {
		t.Fatalf("same-source context = %q, want the api-1 lines including the later one", got)
	}
}

func TestLogsFieldsTextLineAndDroppedRecord(t *testing.T) {
	v := newFieldsViewer(t)
	var seq logview.Seq
	for i := range 5 {
		s, _ := v.log.SeqAt(i)
		if r, _ := v.log.Record(s); !r.IsJSON() {
			seq = s
		}
	}
	d, err := newLogsFieldsDialog(v, seq, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.fields) != 0 || !strings.Contains(d.note, "not a JSON line") {
		t.Fatalf("a text line: fields %v, note %q", d.fields, d.note)
	}
	pressRune(d, 'f') // nothing to follow
	if v.Query() != "" {
		t.Fatalf("Follow on a text line set %q", v.Query())
	}

	// The record the user returns to was dropped meanwhile.
	start := seqOf(t, v, "start")
	if err := v.showContext(start, false); err != nil {
		t.Fatal(err)
	}
	v.SetLimits(logview.Limits{MaxRecords: 2})
	v.back()
	if !strings.Contains(v.statusText(), "the selected line was dropped") {
		t.Fatalf("statusText() = %q", v.statusText())
	}
	if _, err := newLogsFieldsDialog(v, start, func() {}); err == nil {
		t.Fatal("a dialog opened on a dropped record")
	}
}

func TestTraceURL(t *testing.T) {
	t.Parallel()
	got, err := traceURL("https://tempo.example/trace/{trace}?q={trace}", "a b&c=d/é?#+")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://tempo.example/trace/a%20b%26c%3Dd%2F%C3%A9%3F%23%2B?q=a%20b%26c%3Dd%2F%C3%A9%3F%23%2B"; got != want {
		t.Fatalf("traceURL = %q, want %q", got, want)
	}
	for _, tc := range []struct{ template, value, want string }{
		{"", "x", "set viewer.logs.traceURL"},
		{"https://x/", "x", "has no {trace}"},
		{"https://x/{trace}", "", "no trace ID"},
	} {
		if _, err := traceURL(tc.template, tc.value); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("traceURL(%q, %q) = %v, want %q", tc.template, tc.value, err, tc.want)
		}
	}
}

// Through the app: M-d opens the fields dialog over the viewer; t without
// a template says so and stays open; c copies the value.
func TestAppLogsFieldsDialogOverViewer(t *testing.T) {
	v := newFieldsViewer(t)
	mm := NewModalManager(nil)
	app := &App{ctx: t.Context(), modalManager: mm, width: 100, height: 30}
	app.setupLogFields(v)
	logs := NewModal("logs", v)
	logs.SetMode(ModalModeFullscreen)
	logs.SetDimensions(100, 30)
	mm.Register("logs_viewer", logs)
	app.showModal("logs_viewer")

	app.Update(altKey('d', false))
	if top := mm.GetActiveModal(); top == nil || top != mm.modals[logsFieldsModalName] {
		t.Fatal("M-d must open the fields dialog")
	}
	if got := viewString(mm.View()); !strings.Contains(got, ".trace_id = t-1") {
		t.Fatalf("the dialog is not drawn:\n%s", got)
	}
	app.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if top := mm.GetActiveModal(); top != mm.modals[logsFieldsModalName] {
		t.Fatal("t without a template must keep the dialog open")
	}
	if got := viewString(mm.View()); !strings.Contains(got, "set viewer.logs.traceURL") {
		t.Fatalf("no unavailable note:\n%s", got)
	}
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if top := mm.GetActiveModal(); top != logs {
		t.Fatal("c must copy and return to the viewer")
	}
	if cmd == nil {
		t.Fatal("c did not ask to copy")
	}
}
