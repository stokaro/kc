package logview_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stokaro/kc/internal/logview"
)

func TestWindowTrim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		limits      logview.Limits
		costs       []int64
		wantTrim    int
		wantLen     int
		wantDropped int64
	}{
		{name: "under the limits", limits: logview.Limits{MaxRecords: 10, MaxBytes: 1000}, costs: repeat(10, 100), wantLen: 10},
		{name: "one record over frees a tenth", limits: logview.Limits{MaxRecords: 10}, costs: repeat(11, 1), wantTrim: 2, wantLen: 9, wantDropped: 2},
		{name: "bytes over frees a tenth of the bytes", limits: logview.Limits{MaxBytes: 1000}, costs: repeat(11, 100), wantTrim: 2, wantLen: 9, wantDropped: 2},
		{name: "no limits keep everything", costs: repeat(1000, 1000), wantLen: 1000},
		{name: "a record larger than the budget is dropped too", limits: logview.Limits{MaxBytes: 100}, costs: []int64{500}, wantTrim: 1, wantLen: 0, wantDropped: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := logview.NewWindow(tt.limits)
			for _, c := range tt.costs {
				w.Add(c)
			}
			if got := w.Trim(); got != tt.wantTrim {
				t.Fatalf("Trim() = %d, want %d", got, tt.wantTrim)
			}
			if w.Len() != tt.wantLen || w.Dropped() != tt.wantDropped {
				t.Fatalf("Len/Dropped = %d/%d, want %d/%d", w.Len(), w.Dropped(), tt.wantLen, tt.wantDropped)
			}
			if limit := tt.limits.MaxBytes; limit > 0 && w.Bytes() > limit {
				t.Fatalf("Bytes() = %d, over %d", w.Bytes(), limit)
			}
			// Right after a trim, one more record fits.
			if tt.wantTrim > 0 && tt.wantLen > 0 {
				w.Add(tt.costs[0])
				if got := w.Trim(); got != 0 {
					t.Fatalf("Trim() after one more record = %d, want 0 (batches)", got)
				}
			}
			w.Reset()
			if w.Len() != 0 || w.Bytes() != 0 || w.Dropped() != 0 {
				t.Fatal("Reset must forget everything")
			}
		})
	}
}

func repeat(n int, cost int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = cost
	}
	return out
}

func TestTruncateLine(t *testing.T) {
	t.Parallel()
	if got := logview.TruncateLine("short", 10); got != "short" {
		t.Fatalf("short line changed: %q", got)
	}
	if got := logview.TruncateLine("0123456789", 10); got != "0123456789" {
		t.Fatalf("line at the limit changed: %q", got)
	}
	got := logview.TruncateLine("0123456789abc", 10)
	if want := "0123456789 [kc] line truncated: 13 bytes, the first 10 kept"; got != want {
		t.Fatalf("TruncateLine = %q, want %q", got, want)
	}
	// "é" is two bytes; a cut inside it backs off to the rune start.
	got = logview.TruncateLine("aaaaaaaaaé", 10)
	if !strings.HasPrefix(got, "aaaaaaaaa [kc]") || !utf8.ValidString(got) {
		t.Fatalf("TruncateLine cut a rune: %q", got)
	}
	if !logview.IsTruncated(got) {
		t.Fatalf("IsTruncated(%q) = false", got)
	}
	for _, line := range []string{"", "plain line", "the job was kept", "x [kc] line truncated: 5 bytes, the first 2 kept [ok]"} {
		if logview.IsTruncated(line) {
			t.Fatalf("IsTruncated(%q) = true", line)
		}
	}
}

func TestReadLine(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 10000)
	input := "a\r\n" + "0123456789\n" + long + "\n" + "tail" + long
	r := bufio.NewReaderSize(strings.NewReader(input), 16)
	want := []string{
		"a\r\n",
		"0123456789\n",
		"xxxxxxxxxx [kc] line truncated: 10000 bytes, the first 10 kept\n",
		"tailxxxxxx [kc] line truncated: 10004 bytes, the first 10 kept",
	}
	for i, w := range want {
		got, err := logview.ReadLine(r, 10)
		if got != w {
			t.Fatalf("line %d = %q, want %q", i, got, w)
		}
		last := i == len(want)-1
		if last != errors.Is(err, io.EOF) || (!last && err != nil) {
			t.Fatalf("line %d: err = %v", i, err)
		}
	}
	if got, err := logview.ReadLine(r, 10); got != "" || !errors.Is(err, io.EOF) {
		t.Fatalf("after the end: %q, %v", got, err)
	}
}

func TestReadLineMatchesReadStringWithinLimit(t *testing.T) {
	t.Parallel()
	input := "one\ntwo\n\nthree"
	a := bufio.NewReader(strings.NewReader(input))
	b := bufio.NewReader(strings.NewReader(input))
	for {
		want, wantErr := a.ReadString('\n')
		got, gotErr := logview.ReadLine(b, 100)
		if got != want || !errors.Is(gotErr, wantErr) {
			t.Fatalf("ReadLine = %q, %v; ReadString = %q, %v", got, gotErr, want, wantErr)
		}
		if wantErr != nil {
			return
		}
	}
}

func TestLogDropsOldestRecords(t *testing.T) {
	t.Parallel()
	l := logview.NewLog(plain())
	l.SetLimits(logview.Limits{MaxRecords: 10})
	l.SetQuery(logview.MustParseQuery("keep"))
	display := 0
	for i := range 25 {
		word := "skip"
		if i%2 == 0 {
			word = "keep"
		}
		shown, dropped := l.Append(logview.Source{}, fmt.Sprintf("%s %d", word, i))
		display += len(shown) - dropped
		if l.Len() > 10 {
			t.Fatalf("after line %d: Len() = %d, over 10", i, l.Len())
		}
		if display != l.Shown() || display != len(l.Lines()) {
			t.Fatalf("after line %d: display has %d lines, Shown() = %d, Lines() = %d", i, display, l.Shown(), len(l.Lines()))
		}
	}
	if got := l.Dropped(); got != int64(25-l.Len()) {
		t.Fatalf("Dropped() = %d, want %d", got, 25-l.Len())
	}
	// The kept records are the newest ones, unchanged.
	records := l.Records()
	if got, want := records[len(records)-1].Raw, "keep 24"; got != want {
		t.Fatalf("newest record = %q, want %q", got, want)
	}
	if got, want := records[0].Raw, fmt.Sprintf("%s %d", map[bool]string{true: "keep", false: "skip"}[(25-len(records))%2 == 0], 25-len(records)); got != want {
		t.Fatalf("oldest record = %q, want %q", got, want)
	}
	// Clearing the filter shows every kept record, none of the dropped.
	l.SetQuery(nil)
	if got := l.Lines(); len(got) != l.Len() || got[0] != records[0].Raw {
		t.Fatalf("Lines() after clearing the filter = %q", got)
	}
}

func TestLogAppendLargerThanTheLimits(t *testing.T) {
	t.Parallel()
	l := logview.NewLog(plain())
	l.SetLimits(logview.Limits{MaxRecords: 10})
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = fmt.Sprint(i)
	}
	shown, dropped := l.Append(logview.Source{}, lines...)
	if len(shown) != 50 {
		t.Fatalf("Append returned %d lines, want 50", len(shown))
	}
	if got := len(shown) - dropped; got != l.Len() || got != 9 {
		t.Fatalf("display keeps %d lines, Len() = %d, want 9", got, l.Len())
	}
	if got := l.Lines(); !equalStrings(got, shown[dropped:]) {
		t.Fatalf("Lines() = %q, want the newest %q", got, shown[dropped:])
	}
}

func TestLogSetLimitsDropsAtOnce(t *testing.T) {
	t.Parallel()
	l := logview.NewLog(plain())
	for i := range 100 {
		l.Append(logview.Source{}, fmt.Sprint(i))
	}
	if dropped := l.SetLimits(logview.Limits{MaxRecords: 20}); dropped != 82 || l.Len() != 18 {
		t.Fatalf("SetLimits dropped %d, Len() = %d, want 82 and 18", dropped, l.Len())
	}
}

// TestLogLongStreamStaysBounded streams a mix of JSON, text and oversized
// lines through small limits. The log must stay within both limits after
// every line, and the kept records must be the newest input lines,
// unchanged except for truncated ones.
func TestLogLongStreamStaysBounded(t *testing.T) {
	t.Parallel()
	limits := logview.Limits{MaxRecords: 2000, MaxBytes: 2 << 20}
	l := logview.NewLog(logview.DefaultFormat())
	l.SetLimits(limits)
	huge := strings.Repeat("y", logview.ReadLimit+5000)
	var input []string
	for i := range 50_000 {
		var line string
		switch {
		case i%997 == 0:
			line = fmt.Sprintf(`{"msg":"%d %s"}`, i, huge)
		case i%3 == 0:
			line = fmt.Sprintf(`{"level":"info","msg":"request %d","http":{"status":200}}`, i)
		default:
			line = fmt.Sprintf("plain text line %d", i)
		}
		input = append(input, line)
		l.Append(logview.Source{}, line)
		if l.Len() > limits.MaxRecords || l.Bytes() > limits.MaxBytes {
			t.Fatalf("after line %d: Len %d, Bytes %d, over %+v", i, l.Len(), l.Bytes(), limits)
		}
	}
	if l.Dropped()+int64(l.Len()) != int64(len(input)) {
		t.Fatalf("Dropped %d + Len %d != %d lines", l.Dropped(), l.Len(), len(input))
	}
	kept := input[len(input)-l.Len():]
	truncated := 0
	for i, r := range l.Records() {
		want := kept[i]
		if len(want) > logview.ReadLimit {
			want = logview.TruncateLine(want, logview.ReadLimit)
			truncated++
		}
		if r.Raw != want {
			t.Fatalf("record %d differs from input line", i)
		}
		if len(r.Raw) > logview.ReadLimit+200 {
			t.Fatalf("record %d has %d bytes, over the line limit", i, len(r.Raw))
		}
	}
	if truncated == 0 || truncated != l.Truncated() {
		t.Fatalf("Truncated() = %d, counted %d; the stream must keep at least one", l.Truncated(), truncated)
	}
}
