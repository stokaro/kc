package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/logview"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// letter is a plain key press, as a terminal reports a typed letter.
func letter(r rune) tea.KeyPressMsg {
	k := tea.KeyPressMsg{Code: r, Text: string(r)}
	if r >= 'A' && r <= 'Z' {
		k.Code, k.ShiftedCode, k.Mod = r+('a'-'A'), r, tea.ModShift
	}
	return k
}

// markedViewer is a viewer that read the JSON records of lines, scrolled
// to the top so jumps start at the first record. It shows two lines, so
// the top line is the one in focus.
func markedViewer(t *testing.T, lines ...string) *LogsViewer {
	t.Helper()
	v := NewLogsViewer("t", testLogsSpec(), nil, "dracula")
	v.SetDimensions(100, 4)
	v.inner.Resize(100, 2)
	v.appendRecords(lines)
	v.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if v.autoFollow {
		t.Fatal("Home must stop following")
	}
	return v
}

// pointedRaw returns the original line of the record the viewer points
// at.
func pointedRaw(t *testing.T, v *LogsViewer) string {
	t.Helper()
	seq, ok := v.pointedRecord()
	if !ok {
		t.Fatal("the viewer points at no record")
	}
	r, err := v.log.Record(seq)
	if err != nil {
		t.Fatal(err)
	}
	return r.Raw
}

func jsonLine(level, msg string) string {
	return `{"level":"` + level + `","msg":"` + msg + `"}`
}

// TestLogsIssueJumps checks e and E: they go to the next and previous
// record whose level field is warn or worse, among the shown records,
// and say when there is none. Text lines are never taken for issues.
func TestLogsIssueJumps(t *testing.T) {
	t.Parallel()
	v := markedViewer(t,
		jsonLine("info", "start"),
		"ERROR text without a level field",
		jsonLine("warn", "slow"),
		jsonLine("info", "ok"),
		jsonLine("error", "boom"),
	)
	v.Update(letter('e'))
	if got := pointedRaw(t, v); !strings.Contains(got, "slow") {
		t.Fatalf("e points at %q, want the warn record", got)
	}
	v.Update(letter('e'))
	if got := pointedRaw(t, v); !strings.Contains(got, "boom") {
		t.Fatalf("second e points at %q, want the error record", got)
	}
	v.Update(letter('e'))
	if !strings.Contains(v.note, "No warning or error") || !strings.Contains(pointedRaw(t, v), "boom") {
		t.Fatalf("e past the last issue: note %q", v.note)
	}
	v.Update(letter('E'))
	if got := pointedRaw(t, v); !strings.Contains(got, "slow") {
		t.Fatalf("E points at %q, want the warn record", got)
	}

	// A filter that hides the error record leaves it out of the jumps.
	v.SetQuery(logview.MustParseQuery("!boom"))
	v.Update(letter('e'))
	if !strings.Contains(v.note, "No warning or error") {
		t.Fatalf("e reached a hidden record: note %q, pointer %q", v.note, pointedRaw(t, v))
	}
}

// TestLogsMarksKeepTheirRecords marks records and checks that the marks
// stay on them while the filter changes, that u and U walk them, that c
// copies them in record order with their source, and that a mark whose
// record was dropped is reported and forgotten.
func TestLogsMarksKeepTheirRecords(t *testing.T) {
	t.Parallel()
	v := markedViewer(t,
		jsonLine("info", "a"),
		jsonLine("warn", "b"),
		jsonLine("info", "c"),
		jsonLine("error", "d"),
	)
	v.Update(letter('e')) // b
	v.Update(letter('m'))
	v.Update(letter('e')) // d
	v.Update(letter('m'))
	if !strings.Contains(v.statusText(), "2 marked, this line marked") {
		t.Fatalf("status %q", v.statusText())
	}

	// The filter moves the display lines; the marks stay on b and d.
	v.SetQuery(logview.MustParseQuery("level=warn,error"))
	v.marks.clearPointer()
	v.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	v.Update(letter('u'))
	if got := pointedRaw(t, v); !strings.Contains(got, `"b"`) {
		t.Fatalf("u from the top points at %q, want b", got)
	}
	v.Update(letter('u'))
	if got := pointedRaw(t, v); !strings.Contains(got, `"d"`) {
		t.Fatalf("second u points at %q, want d", got)
	}
	v.Update(letter('U'))
	if got := pointedRaw(t, v); !strings.Contains(got, `"b"`) {
		t.Fatalf("U points at %q, want b", got)
	}
	v.SetQuery(nil)

	cmd, _ := v.handleMarkKey(letter('c'))
	if cmd == nil {
		t.Fatal("c copies nothing")
	}
	msg, ok := cmd().(viewer.CopyRequestMsg)
	if !ok || msg.Text != jsonLine("warn", "b")+"\n"+jsonLine("error", "d") || msg.What != "2 marked lines" {
		t.Fatalf("c copies %+v", msg)
	}

	// m on a marked record unmarks it.
	v.Update(letter('m'))
	if len(v.marks.set) != 1 {
		t.Fatalf("%d marks after unmarking b, want 1", len(v.marks.set))
	}

	// Records go to stay within the limits: the mark of d goes with
	// its record and is reported once, never moved to another record.
	v.SetLimits(logview.Limits{MaxRecords: 2, MaxBytes: 1 << 20})
	v.appendRecords([]string{jsonLine("info", "e"), jsonLine("info", "f")})
	v.Update(letter('u'))
	if !strings.Contains(v.note, "No marked lines") || !strings.Contains(v.note, "1 marked lines were dropped") {
		t.Fatalf("u after the marked record was dropped: note %q", v.note)
	}
	if len(v.marks.set) != 0 {
		t.Fatalf("a mark survived its record: %v", v.marks.set)
	}
}

// TestLogsMarksAreBounded checks the limit of marks.
func TestLogsMarksAreBounded(t *testing.T) {
	t.Parallel()
	lines := make([]string, logsMaxMarks+1)
	for i := range lines {
		lines[i] = jsonLine("warn", "w")
	}
	v := markedViewer(t, lines...)
	v.Update(letter('m')) // the top line
	for range logsMaxMarks {
		v.Update(letter('e'))
		v.Update(letter('m'))
	}
	if len(v.marks.set) != logsMaxMarks || !strings.Contains(v.note, "At most 100 lines") {
		t.Fatalf("%d marks, note %q", len(v.marks.set), v.note)
	}
}

// TestLogsMarkSourcesAndPointer checks the copy of a merged log, which
// names each record's source, and that scrolling by hand leaves the
// pointed line.
func TestLogsMarkSourcesAndPointer(t *testing.T) {
	t.Parallel()
	v := NewLogsViewer("t", testLogsSpec(), nil, "dracula")
	v.SetDimensions(100, 8)
	v.inner.Resize(100, 6)
	v.SetStyledLines(true)
	v.appendRecords([]string{"[p/app] " + jsonLine("error", "x"), "[p/side] plain"})
	v.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	v.Update(letter('e'))
	v.Update(letter('m'))
	cmd, _ := v.handleMarkKey(letter('c'))
	if msg := cmd().(viewer.CopyRequestMsg); msg.Text != "[p/app] "+jsonLine("error", "x") {
		t.Fatalf("copy of a merged mark %q", msg.Text)
	}
	if _, ok := v.pointedRecord(); !ok {
		t.Fatal("m must point at the marked line")
	}
	v.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if _, ok := v.pointedRecord(); ok {
		t.Fatal("scrolling by hand must leave the pointed line")
	}
}
