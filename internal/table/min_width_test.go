package table

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestMinWidthKeepsLaterColumnsVisible checks that a column with a
// MinWidth shrinks so the columns after it fit the viewport, and that its
// cut cells say so. Without MinWidth the same table pans instead.
func TestMinWidthKeepsLaterColumnsVisible(t *testing.T) {
	t.Parallel()
	rows := []Row{
		SimpleRow{ID: "a", Cells: []string{" pods not ready in the namespace for a while", "12", "warning"}},
		SimpleRow{ID: "b", Cells: []string{" nodes", "0", "ok"}},
	}
	render := func(minWidth, width int) []string {
		cols := []Column{{Title: " Name", MinWidth: minWidth}, {Title: "Value"}, {Title: "State"}}
		bt := NewBigTable(cols, NewSliceList(rows), width, 6)
		bt.Refresh(t.Context())
		return strings.Split(ansi.Strip(bt.View().Content), "\n")
	}
	tests := []struct {
		name      string
		minWidth  int
		width     int
		want      []string
		not       []string
		ellipsis  bool
		fitsWidth bool
	}{
		{name: "shrinks", minWidth: 12, width: 30, want: []string{"12", "warning", "State"}, ellipsis: true, fitsWidth: true},
		{name: "wide enough", minWidth: 12, width: 80, want: []string{"for a while", "warning"}},
		{name: "no min width pans", width: 30, not: []string{"warning", "…"}},
		{name: "min width is a floor", minWidth: 25, width: 30, want: []string{" pods not ready in the n… 12"}, not: []string{"warning"}, ellipsis: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := render(tc.minWidth, tc.width)
			view := strings.Join(lines, "\n")
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
			if got := strings.Contains(view, "…"); got != tc.ellipsis {
				t.Errorf("ellipsis = %v, want %v:\n%s", got, tc.ellipsis, view)
			}
			// The short row is not cut.
			if tc.ellipsis && strings.Contains(view, "nodes…") {
				t.Errorf("a short cell was cut:\n%s", view)
			}
			if tc.fitsWidth {
				for i, l := range lines {
					if w := ansi.StringWidth(l); w > tc.width {
						t.Errorf("line %d is %d cells wide, want at most %d: %q", i, w, tc.width, l)
					}
				}
			}
		})
	}
}
