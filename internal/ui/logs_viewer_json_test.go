package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logview"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// The ui tests are white-box (package ui); LogsViewer is driven through
// Update, as the app drives it.

const jsonLogSample = `{"level":"info","msg":"start","port":8080}
plain text line
{"level":"error","msg":"boom","err":"connection refused"}
{"level":"info","msg":"GET /healthz"`

// streamAll runs the viewer's read commands until the stream ends.
func streamAll(t *testing.T, v *LogsViewer) {
	t.Helper()
	cmd := v.Init()
	for range 100 {
		if cmd == nil {
			return
		}
		_, cmd = v.Update(cmd())
	}
	t.Fatal("stream did not end")
}

// readerOpener opens r once; the viewer reads it like a pod log.
func readerOpener(r io.ReadCloser) logStreamOpener {
	return func(models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) { return r, nil, nil }
}

func newTestLogsViewer(t *testing.T, content string) *LogsViewer {
	t.Helper()
	v := NewLogsViewer("logs:ns/p/c", models.LogsSpec{}, readerOpener(io.NopCloser(strings.NewReader(content))), "")
	v.SetDimensions(80, 20)
	v.inner.Resize(80, 18)
	return v
}

func plainLines(v *LogsViewer) []string {
	return strings.Split(v.inner.PlainText(), "\n")
}

func altKey(r rune, shift bool) tea.KeyPressMsg {
	mod := tea.ModAlt
	if shift {
		mod |= tea.ModShift
	}
	return tea.KeyPressMsg{Code: r, Mod: mod}
}

func TestLogsViewerFormatsAndTogglesRaw(t *testing.T) {
	v := newTestLogsViewer(t, jsonLogSample)
	streamAll(t, v)

	want := []string{
		"INFO  start port=8080",
		"plain text line",
		`ERROR boom err="connection refused"`,
		`{"level":"info","msg":"GET /healthz"`,
	}
	if got := plainLines(v); !equalLines(got, want) {
		t.Fatalf("formatted view =\n%q\nwant\n%q", got, want)
	}

	if _, cmd := v.Update(altKey('v', false)); cmd != nil {
		t.Fatal("switching to raw must not touch the stream")
	}
	if v.LogMode() != logview.ModeRaw {
		t.Fatal("M-v must switch to raw")
	}
	if got := v.inner.PlainText(); got != jsonLogSample {
		t.Fatalf("raw view =\n%q\nwant the original lines", got)
	}
	v.Update(altKey('v', false))
	if got := plainLines(v); !equalLines(got, want) {
		t.Fatalf("back to formatted =\n%q", got)
	}
}

func TestLogsViewerFilterKeepsRecordsAndStream(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	v := NewLogsViewer("logs:ns/p/c", models.LogsSpec{}, readerOpener(pr), "")
	v.SetDimensions(80, 20)
	v.inner.Resize(80, 18)

	// Open the stream; the first read waits for a line.
	_, cmd := v.Update(v.Init()())
	feed := func(line string) {
		t.Helper()
		done := make(chan tea.Msg)
		go func(c tea.Cmd) { done <- c() }(cmd)
		if _, err := io.WriteString(pw, line+"\n"); err != nil {
			t.Fatal(err)
		}
		_, cmd = v.Update(<-done)
		if cmd == nil {
			t.Fatal("the viewer must keep reading the stream")
		}
	}
	feed(`{"level":"info","msg":"one"}`)
	feed(`{"level":"error","msg":"two"}`)

	v.SetQuery(logview.MustParseQuery("level=error .msg"))
	if got := v.inner.PlainText(); got != "two" {
		t.Fatalf("filtered view = %q, want two", got)
	}
	if got := v.logViewStatus(); got != "formatted, 1 of 2 lines, filter: level=error .msg" {
		t.Fatalf("status = %q", got)
	}

	// New records pass the same filter, and the stream goes on.
	feed(`{"level":"info","msg":"three"}`)
	feed(`{"level":"error","msg":"four"}`)
	if got := v.inner.PlainText(); got != "two\nfour" {
		t.Fatalf("after more lines = %q, want two and four", got)
	}

	v.SetQuery(nil)
	if got := len(plainLines(v)); got != 4 {
		t.Fatalf("clearing the filter shows %d lines, want all 4", got)
	}
}

func TestLogsViewerCopyShownAndOriginals(t *testing.T) {
	v := newTestLogsViewer(t, jsonLogSample)
	streamAll(t, v)
	v.SetQuery(logview.MustParseQuery("level=error"))

	copyMsg := func(key tea.KeyPressMsg) viewer.CopyRequestMsg {
		t.Helper()
		_, cmd := v.Update(key)
		if cmd == nil {
			t.Fatalf("%s: no copy command", key.String())
		}
		msg, ok := cmd().(viewer.CopyRequestMsg)
		if !ok {
			t.Fatalf("%s: got %T, want a copy request", key.String(), msg)
		}
		return msg
	}
	shown := copyMsg(altKey('a', false))
	if shown.What != "shown lines" || shown.Text != `ERROR boom err="connection refused"` {
		t.Fatalf("M-a copied %q (%s)", shown.Text, shown.What)
	}
	for _, key := range []tea.KeyPressMsg{altKey('a', true), {Code: 'A', Mod: tea.ModAlt}} {
		orig := copyMsg(key)
		if orig.What != "original records" || orig.Text != `{"level":"error","msg":"boom","err":"connection refused"}` {
			t.Fatalf("%s copied %q (%s)", key.String(), orig.Text, orig.What)
		}
	}
}

func TestLogsViewerSearchSurvivesToggle(t *testing.T) {
	v := newTestLogsViewer(t, jsonLogSample)
	streamAll(t, v)
	v.Update(tea.KeyPressMsg{Code: tea.KeyF7})
	for _, r := range "boom" {
		v.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	v.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !v.inner.HasSearchMatches() {
		t.Fatal("search found nothing")
	}
	v.Update(altKey('v', false))
	if !v.inner.HasSearchMatches() {
		t.Fatal("the search must survive switching to raw")
	}
	v.SetQuery(logview.MustParseQuery("level=info"))
	if v.inner.HasSearchMatches() {
		t.Fatal("the filtered view has no boom line")
	}
	v.SetQuery(nil)
	if !v.inner.HasSearchMatches() {
		t.Fatal("the search must come back with the line")
	}
}

func TestLogsViewerFilterKeyOpensDialog(t *testing.T) {
	v := newTestLogsViewer(t, "")
	opened := false
	v.SetOnFilter(func() tea.Cmd { opened = true; return nil })
	v.Update(altKey('g', false))
	if !opened {
		t.Fatal("M-g must open the filter dialog")
	}
}

func TestLogsFilterDialog(t *testing.T) {
	v := newTestLogsViewer(t, jsonLogSample)
	streamAll(t, v)
	closed := 0
	d := newLogsFilterDialog(v, func() { closed++ })
	d.SetDimensions(60, 5)

	typeText := func(s string) {
		for _, r := range s {
			d.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	enter := func() tea.Cmd {
		_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return cmd
	}

	typeText("level~(")
	if cmd := enter(); cmd != nil {
		if _, ok := cmd().(logsFilterResultMsg); ok {
			t.Fatal("an invalid query must not be accepted")
		}
	}
	if d.input.Error() == "" {
		t.Fatal("an invalid query must show an error")
	}

	d.input.SetValue("!level=info")
	cmd := enter()
	if cmd == nil {
		t.Fatal("a valid query must be accepted")
	}
	d.Update(cmd())
	if closed != 1 {
		t.Fatalf("dialog closed %d times, want 1", closed)
	}
	if got := v.Query(); got != "!level=info" {
		t.Fatalf("viewer query = %q", got)
	}
	if got := len(plainLines(v)); got != 3 {
		t.Fatalf("filtered view has %d lines, want 3", got)
	}

	// The dialog opens with the current query, and an empty one clears it.
	d = newLogsFilterDialog(v, func() { closed++ })
	if got := d.input.Value(); got != "!level=info" {
		t.Fatalf("dialog starts with %q", got)
	}
	d.input.SetValue("")
	d.Update(enter()())
	if v.Query() != "" || len(plainLines(v)) != 4 {
		t.Fatal("an empty query must clear the filter")
	}
}

// A merged log keeps the [pod/container] prefix as the record's source: the
// JSON after it is formatted, the prefix stays in front, and the original
// records are copied without it.
func TestLogsViewerMergedLogFormatsJSONAfterThePrefix(t *testing.T) {
	src := logmux.Target{Pod: "api-1", Container: "app"}
	content := logmux.Format(logmux.Line{Target: src, Text: `{"level":"error","msg":"boom"}`}, true) + "\n" +
		logmux.Format(logmux.Line{Notice: true, Text: "+ api-1/app"}, true) + "\n"
	v := newTestLogsViewer(t, content)
	v.SetStyledLines(true)
	streamAll(t, v)

	want := []string{"[api-1/app] ERROR boom", "[kc] + api-1/app"}
	if got := plainLines(v); !equalLines(got, want) {
		t.Fatalf("merged view =\n%q\nwant\n%q", got, want)
	}
	if view := v.View().Content; !strings.Contains(view, logmux.StyledPrefix(src)) {
		t.Fatalf("the prefix is not shown in its color:\n%q", view)
	}
	if got := v.log.Originals(); got[0] != `{"level":"error","msg":"boom"}` {
		t.Fatalf("original record = %q, want the line without the prefix", got[0])
	}
}

// Through the app: M-g opens the dialog over the viewer, lines keep arriving
// behind it, and Enter applies the filter and returns to the viewer.
func TestAppLogsFilterDialogOverViewer(t *testing.T) {
	v := newTestLogsViewer(t, "")
	mm := NewModalManager(nil)
	app := &App{ctx: t.Context(), modalManager: mm, width: 100, height: 30}
	v.SetOnFilter(func() tea.Cmd { return app.showLogsFilterModal(v) })
	logs := NewModal("logs", v)
	logs.SetMode(ModalModeFullscreen)
	logs.SetDimensions(100, 30)
	mm.Register("logs_viewer", logs)
	app.showModal("logs_viewer")

	app.Update(altKey('g', false))
	if top := mm.GetActiveModal(); top == nil || top != mm.modals[logsFilterModalName] {
		t.Fatal("M-g must open the filter dialog on top of the viewer")
	}
	if got := viewString(mm.View()); !strings.Contains(got, "Filter") {
		t.Fatalf("the dialog is not drawn:\n%s", got)
	}

	app.Update(logStreamMsg{viewer: v, gen: v.gen, lines: []string{`{"level":"info","msg":"a"}`, `{"level":"error","msg":"b"}`}})
	if got := v.log.Len(); got != 2 {
		t.Fatalf("the viewer kept %d lines behind the dialog, want 2", got)
	}

	for _, r := range "level=error" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if res := findCmdMsg[logsFilterResultMsg](cmd); res == nil {
		t.Fatal("Enter must answer the dialog")
	} else {
		app.Update(*res)
	}
	if top := mm.GetActiveModal(); top != logs {
		t.Fatal("Enter must return to the viewer")
	}
	if got := v.inner.PlainText(); got != "ERROR b" {
		t.Fatalf("filtered view = %q, want ERROR b", got)
	}
}

// findCmdMsg runs cmd, and the commands of a batch, and returns the first T.
func findCmdMsg[T any](cmd tea.Cmd) *T {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case T:
		return &msg
	case tea.BatchMsg:
		for _, c := range msg {
			if got := findCmdMsg[T](c); got != nil {
				return got
			}
		}
	}
	return nil
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
