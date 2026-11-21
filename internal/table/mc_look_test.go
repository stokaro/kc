package table

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func mcLookTable(t *testing.T, mode GridMode) *BigTable {
	t.Helper()
	ctx := t.Context()
	rows := []Row{
		SimpleRow{ID: "a", Cells: []string{"alpha", "1"}},
		SimpleRow{ID: "b", Cells: []string{"beta", "2"}},
	}
	cols := []Column{{Title: "Name", Width: 10}, {Title: "Count", Width: 6}}
	bt := NewBigTable(cols, NewSliceList(rows), 30, 8)
	st := DefaultStyles()
	st.Header = st.Header.Align(lipgloss.Center)
	bt.SetStyles(st)
	bt.SetMode(ctx, mode)
	bt.BorderVertical(ctx, true)
	return &bt
}

func TestColumnSeparatorsReachBottom(t *testing.T) {
	for _, mode := range []GridMode{ModeScroll, ModeFit} {
		bt := mcLookTable(t, mode)
		lines := strings.Split(ansi.Strip(bt.View().Content), "\n")
		if len(lines) != 8 {
			t.Fatalf("mode %v: expected 8 lines (header + 7 rows), got %d:\n%s", mode, len(lines), strings.Join(lines, "\n"))
		}
		for i, line := range lines {
			if !strings.Contains(line, "│") {
				t.Fatalf("mode %v: line %d has no column separator: %q", mode, i, line)
			}
		}
	}
}

func TestCursorLineHighlightsSeparators(t *testing.T) {
	bt := mcLookTable(t, ModeFit)
	lines := strings.Split(bt.View().Content, "\n")
	cursor := lines[1]
	want := bt.styles.Selector.Render(ansi.Strip(cursor))
	if cursor != want {
		t.Fatalf("cursor line should be one selector-styled run including separators\n got: %q\nwant: %q", cursor, want)
	}

	bt.SetFocused(t.Context(), false)
	lines = strings.Split(bt.View().Content, "\n")
	if lines[1] == bt.styles.Selector.Render(ansi.Strip(lines[1])) {
		t.Fatalf("unfocused table should not highlight the cursor line: %q", lines[1])
	}
}

func TestHeaderTitlesCentered(t *testing.T) {
	for _, mode := range []GridMode{ModeScroll, ModeFit} {
		bt := mcLookTable(t, mode)
		header := strings.Split(ansi.Strip(bt.View().Content), "\n")[0]
		first := strings.Split(header, "│")[0]
		if strings.HasPrefix(first, "Name") {
			t.Fatalf("mode %v: expected centered title, got %q", mode, header)
		}
		if !strings.Contains(first, "Name") {
			t.Fatalf("mode %v: missing title in %q", mode, header)
		}
	}
}

func TestAlignASCII(t *testing.T) {
	tests := []struct {
		pos  lipgloss.Position
		want string
	}{
		{lipgloss.Left, "ab    "},
		{lipgloss.Center, "  ab  "},
		{lipgloss.Right, "    ab"},
	}
	for _, tt := range tests {
		if got := alignASCII("ab", 6, tt.pos); got != tt.want {
			t.Errorf("alignASCII(ab, 6, %v) = %q, want %q", tt.pos, got, tt.want)
		}
	}
	if got := alignASCII("abcdef", 3, lipgloss.Center); got != "abcdef" {
		t.Errorf("alignASCII should not truncate, got %q", got)
	}
}

func TestScrollModeGivesSpareWidthToFirstColumn(t *testing.T) {
	bt := mcLookTable(t, ModeScroll)
	lines := strings.Split(ansi.Strip(bt.View().Content), "\n")
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 30 {
			t.Fatalf("line %d is %d columns wide, want 30: %q", i, got, line)
		}
		cells := strings.Split(line, "│")
		if len(cells) != 2 {
			t.Fatalf("line %d: expected 2 cells, got %q", i, line)
		}
		// "Count" is the widest value in the second column.
		if got := lipgloss.Width(cells[1]); got != len("Count") {
			t.Fatalf("line %d: second column is %d wide, want %d: %q", i, got, len("Count"), line)
		}
	}
}

func TestScrollModeStyledCellsKeepWidth(t *testing.T) {
	ctx := t.Context()
	green := lipgloss.NewStyle().Foreground(lipgloss.Green)
	rows := []Row{
		SimpleRow{ID: "back", Cells: []string{"/..", "", ""}, Styles: []*lipgloss.Style{&green, nil, nil}},
		SimpleRow{ID: "cm", Cells: []string{"/configmaps", "v1", "1"}, Styles: []*lipgloss.Style{&green, &green, &green}},
		SimpleRow{ID: "eps", Cells: []string{"/endpointslices", "discovery.k8s.io/v1", "1"}, Styles: []*lipgloss.Style{&green, &green, &green}},
	}
	cols := []Column{{Title: "Name"}, {Title: "Group"}, {Title: "Count"}}
	bt := NewBigTable(cols, NewSliceList(rows), 78, 8)
	st := DefaultStyles()
	st.Header = st.Header.Align(lipgloss.Center)
	bt.SetStyles(st)
	bt.SetFocused(ctx, false)
	bt.BorderVertical(ctx, true)
	for i, line := range strings.Split(ansi.Strip(bt.View().Content), "\n") {
		if got := lipgloss.Width(line); got != 78 {
			t.Fatalf("line %d is %d columns wide, want 78: %q", i, got, line)
		}
		cells := strings.Split(line, "│")
		if len(cells) != 3 || len(cells[1]) != len("discovery.k8s.io/v1") || len(cells[2]) != len("Count") {
			t.Fatalf("line %d has unexpected cell widths: %q", i, line)
		}
	}
}
