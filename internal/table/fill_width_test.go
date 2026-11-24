package table_test

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/table"
)

// renderLines renders the rows with centered titles, as kc's panels do.
func renderLines(t *testing.T, cols []table.Column, cells [][]string, width int) []string {
	t.Helper()
	rows := make([]table.Row, len(cells))
	for i, c := range cells {
		rows[i] = table.SimpleRow{ID: c[0], Cells: c}
	}
	bt := table.NewBigTable(cols, table.NewSliceList(rows), width, 6)
	st := table.DefaultStyles()
	st.Header = st.Header.Align(lipgloss.Center)
	bt.SetStyles(st)
	bt.Refresh(t.Context())
	return strings.Split(ansi.Strip(bt.View().Content), "\n")
}

// TestFillColumnGivesUpWidthFirst checks the width allocation of a line
// wider than the viewport: the Fill column narrows first, so the columns
// that name things keep their natural width, and they narrow only once the
// Fill column is at its floor.
func TestFillColumnGivesUpWidthFirst(t *testing.T) {
	t.Parallel()
	reason := "volumes[kube-api-access-jjcsc].projected.sources[configMap]"
	// Without Fill the reason never shrinks, as before Fill existed.
	cols := func(fill bool) []table.Column {
		last := table.Column{Title: "Relation"}
		if fill {
			last = table.Column{Title: "Relation", Fill: true, MinWidth: 16}
		}
		return []table.Column{
			{Title: " Name", MinWidth: 12},
			{Title: "Resource", MinWidth: 7},
			{Title: "Namespace", MinWidth: 7},
			last,
		}
	}
	cells := [][]string{
		{"/kube-root-ca.crt", "configmaps", "shop", reason},
		{"/web-6989b69646-dgqgj", "pods", "shop", "spec.nodeName"},
	}
	tests := []struct {
		name  string
		fill  bool
		width int
		want  []string
		not   []string
	}{
		{
			name:  "names keep their width",
			fill:  true,
			width: 72,
			want:  []string{"/kube-root-ca.crt", "configmaps", "Namespace", "/web-6989b69646-dgqgj", "Relation", "volumes[kube-api-acc"},
		},
		{
			name:  "the reason takes the spare width",
			fill:  true,
			width: 120,
			want:  []string{reason},
			not:   []string{"…"},
		},
		{
			name:  "names narrow below the floor of the reason",
			fill:  true,
			width: 50,
			want:  []string{"volumes[kube-ap…", "Relation"},
		},
		{
			name:  "without Fill the names give up width",
			width: 72,
			want:  []string{"/web-6989b6…"},
			not:   []string{"/web-6989b69646-dgqgj"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := renderLines(t, cols(tc.fill), cells, tc.width)
			view := strings.Join(lines, "\n")
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > tc.width {
					t.Errorf("line %d is %d cells wide, want at most %d: %q", i, w, tc.width, l)
				}
			}
			for _, w := range tc.want {
				if !strings.Contains(view, w) {
					t.Errorf("view misses %q:\n%s", w, view)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(view, w) {
					t.Errorf("view shows %q:\n%s", w, view)
				}
			}
		})
	}
}

// TestLongNameIsCappedBeforeTheFillColumnNarrows checks that one long name
// cannot take most of the line: a column with a MinWidth first narrows to
// two fifths of the viewport.
func TestLongNameIsCappedBeforeTheFillColumnNarrows(t *testing.T) {
	t.Parallel()
	name := "/" + strings.Repeat("very-long-name-", 5)
	cols := []table.Column{{Title: " Name", MinWidth: 12}, {Title: "Message", Fill: true, MinWidth: 16}}
	lines := renderLines(t, cols, [][]string{{name, strings.Repeat("why it is listed ", 4)}}, 60)
	row := lines[1]
	shown, _, ok := strings.Cut(row, "…")
	if !ok {
		t.Fatalf("the long name is not cut:\n%s", strings.Join(lines, "\n"))
	}
	if got, want := ansi.StringWidth(shown)+1, 60*2/5; got != want {
		t.Errorf("name column is %d cells wide, want %d:\n%s", got, want, strings.Join(lines, "\n"))
	}
	if !strings.Contains(row, "why it is listed why") {
		t.Errorf("the message lost more than the rest of the line:\n%s", strings.Join(lines, "\n"))
	}
}

// TestTitleOfAColumnWiderThanTheViewportShows checks that a column cut by
// the right edge of the viewport places its title within the visible part.
// A centered title used to land beyond the edge, so the header was blank
// ("Evidence") or cut ("De" of "Description").
func TestTitleOfAColumnWiderThanTheViewportShows(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a long description of the field ", 6)
	for _, fill := range []bool{false, true} {
		cols := []table.Column{{Title: " Name"}, {Title: "Type"}, {Title: "Description", Fill: fill}}
		lines := renderLines(t, cols, [][]string{{"/spec", "DeploymentSpec", long}}, 60)
		if !strings.Contains(lines[0], "Description") {
			t.Errorf("fill=%v: header misses the title:\n%s", fill, strings.Join(lines, "\n"))
		}
	}
}
