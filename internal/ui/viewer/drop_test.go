package viewer

import (
	"fmt"
	"strings"
	"testing"
)

func numberedLines(from, to int) []string {
	out := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("line %d", i))
	}
	return out
}

func newLogWidget(lines []string) *Widget {
	w := New("")
	w.SetPlainMode(true)
	w.SetLines(nil)
	w.Resize(40, 5)
	w.AppendLines(lines)
	return w
}

func TestDropFirstLinesKeepsTheView(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		offset     int
		drop       int
		wantOffset int
		wantLost   bool
		wantTop    string
	}{
		{name: "view below the dropped lines stays on its line", offset: 50, drop: 20, wantOffset: 30, wantTop: "line 50"},
		{name: "view on a dropped line moves to the oldest kept", offset: 10, drop: 20, wantOffset: 0, wantLost: true, wantTop: "line 20"},
		{name: "dropping more than there is empties the view", offset: 0, drop: 500, wantOffset: 0, wantLost: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newLogWidget(numberedLines(0, 100))
			w.ScrollToLine(tt.offset)
			if got := w.DropFirstLines(tt.drop); got != tt.wantLost {
				t.Fatalf("DropFirstLines() lost = %v, want %v", got, tt.wantLost)
			}
			if w.Offset() != tt.wantOffset {
				t.Fatalf("Offset() = %d, want %d", w.Offset(), tt.wantOffset)
			}
			kept := max(0, 100-tt.drop)
			if len(w.rawLines) != kept || len(w.lines) != kept {
				t.Fatalf("kept %d/%d lines, want %d", len(w.rawLines), len(w.lines), kept)
			}
			if tt.wantTop != "" && w.lines[w.offset] != tt.wantTop {
				t.Fatalf("top line = %q, want %q", w.lines[w.offset], tt.wantTop)
			}
		})
	}
}

func TestDropFirstLinesMovesSearchMatches(t *testing.T) {
	t.Parallel()
	w := newLogWidget(numberedLines(0, 100))
	setSearch(t, w, "line 1", SearchOptions{})
	// Matches: 1, 10-19 and 100+ do not exist, so 11 matches.
	if len(w.searchMatches) != 11 {
		t.Fatalf("matches before = %d, want 11", len(w.searchMatches))
	}
	w.activeMatch = 5 // line 14
	w.DropFirstLines(12)
	// Lines 12-19 are left: 8 matches, the first on line 0 (was 12).
	if len(w.searchMatches) != 8 || w.searchMatches[0].line != 0 {
		t.Fatalf("matches after = %+v", w.searchMatches)
	}
	if w.activeMatch != 2 || w.lines[w.searchMatches[w.activeMatch].line] != "line 14" {
		t.Fatalf("active match = %d, want the one on line 14", w.activeMatch)
	}
	if got := w.lineMatches(0); len(got) != 1 {
		t.Fatalf("lineMatches(0) = %+v, want the moved match", got)
	}
	// Dropping the active match leaves no active one.
	w.DropFirstLines(3)
	if w.activeMatch != -1 || len(w.searchMatches) != 5 {
		t.Fatalf("after dropping the active match: active %d, %d matches", w.activeMatch, len(w.searchMatches))
	}
	// New lines are still searched, at their new positions.
	w.AppendLines([]string{"line 1x"})
	last := w.searchMatches[len(w.searchMatches)-1]
	if w.rawLines[last.line] != "line 1x" {
		t.Fatalf("new match on %q", w.rawLines[last.line])
	}
}

func TestDropFirstLinesMovesOrClearsTheSelection(t *testing.T) {
	t.Parallel()
	w := newLogWidget(numberedLines(0, 100))
	w.sel = selection{active: true, anchor: textPos{line: 40, col: 1}, cursor: textPos{line: 42, col: 3}}
	w.DropFirstLines(10)
	if w.sel.anchor.line != 30 || w.sel.cursor.line != 32 || w.selectedText() != "ine 40\nline 41\nline" {
		t.Fatalf("selection = %+v, text %q", w.sel, w.selectedText())
	}
	w.DropFirstLines(31)
	if w.HasSelection() {
		t.Fatal("a selection that starts on a dropped line must be cleared")
	}
}

func TestDropFirstLinesKeepsStyledLines(t *testing.T) {
	t.Parallel()
	w := New("")
	w.SetPlainMode(true)
	w.SetLines(nil)
	w.AppendStyledLines([]string{"a", "b", "c"}, []string{"\x1b[31ma", "\x1b[31mb", "\x1b[31mc"})
	w.DropFirstLines(1)
	if strings.Join(w.lines, ",") != "\x1b[31mb,\x1b[31mc" || strings.Join(w.rawLines, ",") != "b,c" {
		t.Fatalf("lines %q, raw %q", w.lines, w.rawLines)
	}
	// A theme change rebuilds the shown lines from the kept styled ones.
	w.SetTheme("monokai")
	if strings.Join(w.lines, ",") != "\x1b[31mb,\x1b[31mc" {
		t.Fatalf("after SetTheme: %q", w.lines)
	}
}

func TestPlainAppendDoesNotKeepJoinedText(t *testing.T) {
	t.Parallel()
	w := newLogWidget(numberedLines(0, 3))
	w.AppendLines([]string{"a much longer line than the others"})
	if w.raw != "" {
		t.Fatalf("plain mode kept the joined text: %q", w.raw)
	}
	if w.maxLineWidth != len("a much longer line than the others") {
		t.Fatalf("maxLineWidth = %d", w.maxLineWidth)
	}
	// A theme change in plain mode still has the text.
	w.SetTheme("monokai")
	if got := len(w.lines); got != 4 {
		t.Fatalf("after SetTheme: %d lines, want 4", got)
	}
}

func TestSearchMatchesAreCapped(t *testing.T) {
	t.Parallel()
	w := newLogWidget([]string{strings.Repeat("a", maxSearchMatches+10)})
	setSearch(t, w, "a", SearchOptions{})
	if len(w.searchMatches) != maxSearchMatches || !w.searchCapped {
		t.Fatalf("%d matches, capped %v; want %d and capped", len(w.searchMatches), w.searchCapped, maxSearchMatches)
	}
	if got := w.searchSummary(); !strings.HasSuffix(got, fmt.Sprintf("/%d+)", maxSearchMatches)) {
		t.Fatalf("searchSummary() = %q", got)
	}
	w.AppendLines([]string{"aaa"})
	if len(w.searchMatches) != maxSearchMatches {
		t.Fatalf("append past the cap recorded %d matches", len(w.searchMatches))
	}
	setSearch(t, w, "aaa", SearchOptions{})
	if w.searchCapped && len(w.searchMatches) < maxSearchMatches {
		t.Fatal("a new search must reset the cap")
	}
}
