package table

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A sort mark is one cell wide but three bytes long; the header must not pad
// or cut the title by bytes.
func TestScrollHeaderCountsSortMarkAsOneCell(t *testing.T) {
	ctx := t.Context()
	cols := []Column{{Title: "Name"}, {Title: "Age↓"}}
	bt := NewBigTable(cols, mkList(2, 2), 30, 4)
	bt.SetMode(ctx, ModeScroll)
	bt.Refresh(ctx)
	lines := strings.Split(ansi.Strip(bt.View().Content), "\n")
	// The last column is as wide as its widest text, "id-00", plus the
	// separator. Counting the mark as three cells widens it by two.
	for _, l := range lines {
		if i := strings.LastIndex(l, "id-00"); i > 0 {
			if got := lipgloss.Width(l[i:]); got != len("id-00 ") {
				t.Fatalf("last column is %d cells wide, want %d:\n%s", got, len("id-00 "), strings.Join(lines, "\n"))
			}
			break
		}
	}
	header := ""
	for _, l := range lines {
		if strings.Contains(l, "Age") {
			header = l
			break
		}
	}
	if !strings.Contains(header, "Age↓") {
		t.Fatalf("header lost the sort mark: %q\n%s", header, strings.Join(lines, "\n"))
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w != lipgloss.Width(lines[0]) {
			t.Fatalf("line widths differ (%d vs %d):\n%s", w, lipgloss.Width(lines[0]), strings.Join(lines, "\n"))
		}
	}
}

func TestTitleSlicePad(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s            string
		start, width int
		want         string
	}{
		{"Age↓", 0, 6, "Age↓  "},
		{"Age↓", 2, 3, "e↓ "},
		{"Age↓", 0, 3, "Age"},
		{"Age↓", 9, 2, "  "},
		{"Age↓", 0, 0, ""},
	}
	for _, tc := range cases {
		if got := titleSlicePad(tc.s, tc.start, tc.width); got != tc.want {
			t.Errorf("titleSlicePad(%q, %d, %d) = %q, want %q", tc.s, tc.start, tc.width, got, tc.want)
		}
	}
}
