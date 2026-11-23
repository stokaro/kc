package logview_test

import (
	"testing"

	"github.com/stokaro/kc/internal/logview"
)

func TestLogKeepsRecordsAcrossFilters(t *testing.T) {
	t.Parallel()
	l := logview.NewLog(plain())
	shown, _ := l.Append(logview.Source{},
		`{"level":"info","msg":"start"}`,
		`plain line`,
		`{"level":"error","msg":"boom"}`,
	)
	if want := []string{"INFO  start", "plain line", "ERROR boom"}; !equalStrings(shown, want) {
		t.Fatalf("Append() = %q, want %q", shown, want)
	}

	l.SetQuery(logview.MustParseQuery("level=error"))
	if got, want := l.Lines(), []string{"ERROR boom"}; !equalStrings(got, want) {
		t.Fatalf("Lines() = %q, want %q", got, want)
	}
	if l.Len() != 3 || l.Shown() != 1 {
		t.Fatalf("Len/Shown = %d/%d, want 3/1", l.Len(), l.Shown())
	}

	// New lines pass through the same filter.
	shown, _ = l.Append(logview.Source{}, `{"level":"info","msg":"tick"}`, `{"level":"error","msg":"again"}`)
	if want := []string{"ERROR again"}; !equalStrings(shown, want) {
		t.Fatalf("Append() under a filter = %q, want %q", shown, want)
	}
	if l.Shown() != 2 {
		t.Fatalf("Shown() = %d, want 2", l.Shown())
	}

	// Raw keeps the filter and shows the originals.
	l.SetMode(logview.ModeRaw)
	want := []string{`{"level":"error","msg":"boom"}`, `{"level":"error","msg":"again"}`}
	if got := l.Lines(); !equalStrings(got, want) {
		t.Fatalf("raw Lines() = %q, want %q", got, want)
	}
	if got := l.Originals(); !equalStrings(got, want) {
		t.Fatalf("Originals() = %q, want %q", got, want)
	}

	// Clearing the filter brings every record back.
	l.SetQuery(nil)
	l.SetMode(logview.ModeFormatted)
	if got := l.Lines(); len(got) != 5 || l.Shown() != 5 {
		t.Fatalf("after clearing: %d lines, Shown %d, want 5", len(got), l.Shown())
	}
}

func TestLogOriginalsDifferFromDisplay(t *testing.T) {
	t.Parallel()
	l := logview.NewLog(plain())
	line := "{\"level\":\"info\",\"msg\":\"bad \xff\",\"user\":\"bob\"}"
	l.Append(logview.Source{Pod: "p", Container: "c"}, line)
	l.SetQuery(logview.MustParseQuery(".msg"))
	if got, want := l.Lines(), []string{"[p/c] bad �"}; !equalStrings(got, want) {
		t.Fatalf("Lines() = %q, want %q", got, want)
	}
	if got := l.Originals(); !equalStrings(got, []string{line}) {
		t.Fatalf("Originals() = %q, want the exact original bytes", got)
	}
	if got := l.Records()[0].Source; got != (logview.Source{Pod: "p", Container: "c"}) {
		t.Fatalf("Source = %+v", got)
	}
}

func TestLogReset(t *testing.T) {
	t.Parallel()
	l := logview.NewLog(plain())
	l.SetQuery(logview.MustParseQuery("x"))
	l.SetMode(logview.ModeRaw)
	l.Append(logview.Source{}, "x1", "y")
	l.Reset()
	if l.Len() != 0 || l.Shown() != 0 || len(l.Lines()) != 0 {
		t.Fatal("Reset must drop the records")
	}
	if l.Mode() != logview.ModeRaw || l.Query().String() != "x" {
		t.Fatal("Reset must keep mode and query")
	}
	if got := l.Mode().String(); got != "raw" {
		t.Fatalf("Mode().String() = %q", got)
	}
}

func BenchmarkLogLines(b *testing.B) {
	l := logview.NewLog(logview.DefaultFormat())
	for i := range 10000 {
		if i%3 == 0 {
			l.Append(logview.Source{}, "plain text line number something")
			continue
		}
		l.Append(logview.Source{}, `{"time":"2026-11-22T10:00:00Z","level":"info","msg":"request served","http":{"method":"GET","status":200},"dur":0.0123}`)
	}
	l.SetQuery(logview.MustParseQuery("!healthz level=info,error"))
	b.ReportAllocs()
	for b.Loop() {
		_ = l.Lines()
	}
}
