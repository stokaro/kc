package ui

import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"
)

// trimRight trims only spaces on the right to ignore lipgloss width padding in comparisons.
func trimRight(s string) string { return strings.TrimRight(s, " ") }

func TestRenderInlineFrom_TwoLines_NilCursor_LastTwoLines(t *testing.T) {
	view := "L0\nL1\nL2"
	out, cur := renderInlineFrom(view, nil, 20, 2)
	if cur != nil {
		t.Fatalf("expected nil cursor, got %+v", cur)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if got, want := trimRight(lines[0]), "L1"; got != want {
		t.Errorf("line0=%q want %q", got, want)
	}
	if got, want := trimRight(lines[1]), "L2"; got != want {
		t.Errorf("line1=%q want %q", got, want)
	}
}

func TestRenderInlineFrom_TwoLines_ClampCursorBeyondEnd(t *testing.T) {
	view := "A\nB\nC"
	c := tea.NewCursor(0, 10)
	out, cur := renderInlineFrom(view, c, 20, 2)
	if cur == nil {
		t.Fatalf("expected cursor, got nil")
	}
	if cur.Y != 1 {
		t.Errorf("expected adjusted cursor Y=1, got %d", cur.Y)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if got, want := trimRight(lines[0]), "B"; got != want {
		t.Errorf("line0=%q want %q", got, want)
	}
	if got, want := trimRight(lines[1]), "C"; got != want {
		t.Errorf("line1=%q want %q", got, want)
	}
}

func TestRenderInlineFrom_TwoLines_CursorAtZero(t *testing.T) {
	view := "X\nY"
	c := tea.NewCursor(0, 0)
	out, cur := renderInlineFrom(view, c, 20, 2)
	if cur == nil {
		t.Fatalf("expected cursor, got nil")
	}
	if cur.Y != 1 {
		t.Errorf("expected adjusted cursor Y=1, got %d", cur.Y)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if got, want := trimRight(lines[0]), ""; got != want {
		t.Errorf("line0=%q want %q", got, want)
	}
	if got, want := trimRight(lines[1]), "X"; got != want {
		t.Errorf("line1=%q want %q", got, want)
	}
}

func TestRenderInlineFrom_TwoLines_SingleLineClamp(t *testing.T) {
	view := "Only"
	c := tea.NewCursor(0, 99)
	out, cur := renderInlineFrom(view, c, 20, 2)
	if cur == nil {
		t.Fatalf("expected cursor, got nil")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if got, want := trimRight(lines[0]), ""; got != want {
		t.Errorf("line0=%q want %q", got, want)
	}
	if got, want := trimRight(lines[1]), "Only"; got != want {
		t.Errorf("line1=%q want %q", got, want)
	}
}

func TestRenderInlineFrom_TwoLines_EmptyView(t *testing.T) {
	view := ""
	out, cur := renderInlineFrom(view, nil, 10, 2)
	if cur != nil {
		t.Fatalf("expected nil cursor, got %+v", cur)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if got, want := trimRight(lines[0]), ""; got != want {
		t.Errorf("line0=%q want %q", got, want)
	}
	if got, want := trimRight(lines[1]), ""; got != want {
		t.Errorf("line1=%q want %q", got, want)
	}
}

func TestRenderInlineFrom_SingleLineShowsCursorLine(t *testing.T) {
	view := "$ ls\nfile\n$ \n\n"
	c := tea.NewCursor(2, 2)
	out, cur := renderInlineFrom(view, c, 20, 1)
	if strings.Contains(out, "\n") {
		t.Fatalf("expected a single line, got %q", out)
	}
	if got := trimRight(out); got != "$" {
		t.Fatalf("expected the cursor line, got %q", got)
	}
	if cur == nil || cur.Y != 0 || cur.X != 2 {
		t.Fatalf("expected cursor at (2,0), got %+v", cur)
	}
}

func TestRenderInlineFrom_SingleLineNoCursor(t *testing.T) {
	out, cur := renderInlineFrom("a\nb", nil, 10, 1)
	if cur != nil || trimRight(out) != "b" {
		t.Fatalf("expected last line without cursor, got %q %+v", out, cur)
	}
}
