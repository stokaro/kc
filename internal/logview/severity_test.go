package logview_test

import (
	"testing"

	"github.com/stokaro/kc/internal/logview"
)

func TestFormatSeverity(t *testing.T) {
	t.Parallel()
	f := logview.DefaultFormat()
	for _, tc := range []struct {
		line string
		want logview.Severity
	}{
		{`{"level":"info","msg":"a"}`, logview.SeverityInfo},
		{`{"level":"WARNING"}`, logview.SeverityWarn},
		{`{"severity":"error"}`, logview.SeverityError},
		{`{"level":"fatal"}`, logview.SeverityError},
		{`{"level":50}`, logview.SeverityError},
		{`{"level":40}`, logview.SeverityWarn},
		{`{"level":"loud"}`, logview.SeverityUnknown},
		{`{"msg":"no level"}`, logview.SeverityUnknown},
		{`ERROR plain text is not read for a level`, logview.SeverityUnknown},
		{`2026-11-23T10:00:00Z {"level":"warn"}`, logview.SeverityWarn},
	} {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			r := logview.NewRecord(tc.line, logview.Source{})
			if got := f.Severity(&r); got != tc.want {
				t.Fatalf("Severity = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLogNext checks the search over shown records in both directions,
// from a kept, a dropped and a past-the-end record.
func TestLogNext(t *testing.T) {
	t.Parallel()
	l := logview.NewLog(logview.DefaultFormat())
	l.SetLimits(logview.Limits{MaxRecords: 4, MaxBytes: 1 << 20})
	l.Append(logview.Source{}, "x0", "x1", "a2", "b3", "a4", "a5") // keeps 2..5
	l.SetQuery(logview.MustParseQuery("!b"))
	isA := func(seq logview.Seq) bool {
		r, err := l.Record(seq)
		return err == nil && r.Raw[0] == 'a'
	}
	for _, tc := range []struct {
		name     string
		from     logview.Seq
		backward bool
		want     logview.Seq
		ok       bool
	}{
		{"forward from a dropped record starts at the oldest kept", 0, false, 2, true},
		{"forward skips the hidden record", 2, false, 4, true},
		{"forward from the last finds none", 5, false, 0, false},
		{"backward from past the end", 9, true, 5, true},
		{"backward skips the hidden record", 4, true, 2, true},
		{"backward from a dropped record finds none", 1, true, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := l.Next(tc.from, tc.backward, isA)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("Next(%d, %v) = %d, %v; want %d, %v", tc.from, tc.backward, got, ok, tc.want, tc.ok)
			}
		})
	}
}
