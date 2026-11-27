package list

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// configMapsFolder lists the configmaps names in namespace shop.
func configMapsFolder(names ...string) *rowsFolder {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	var rows []table.Row
	for _, n := range names {
		rows = append(rows, models.NewObjectRow(n, []string{n}, nil, gvr, "shop", n, nil))
	}
	return &rowsFolder{rows: rows}
}

// marksWidget shows the configmaps names in a folder widget.
func marksWidget(t *testing.T, names ...string) *Widget {
	t.Helper()
	w := New(panelcontent.WidgetDeps{})
	w.UseFolder(true)
	_, _ = w.Update(t.Context(), tea.WindowSizeMsg{Width: 40, Height: 10})
	w.SetFolder(t.Context(), configMapsFolder(names...), false)
	return w
}

func markedIDs(t *testing.T, w *Widget) []string {
	t.Helper()
	var out []string
	for _, it := range w.SelectedItems(t.Context()) {
		id, _, _, _ := it.Item.Columns()
		out = append(out, id)
	}
	return out
}

// The bottom border says how many rows are marked, first, so a status cut
// in a narrow panel keeps it (#373).
func TestFrameInfoCountsMarks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marked []string
		want   string
	}{
		{name: "no marks", want: "1/3 • 100%"},
		{name: "one mark", marked: []string{"ux-b"}, want: "1 marked • 1/3 • 100%"},
		{name: "two marks", marked: []string{"ux-a", "ux-c"}, want: "2 marked • 1/3 • 100%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			w := marksWidget(t, "ux-a", "ux-b", "ux-c")
			if len(tc.marked) > 0 {
				w.SelectRowIDs(ctx, tc.marked)
			}
			// SelectRowIDs moves the cursor to the first marked row.
			w.ResetSelection(ctx)
			if got := w.MarkedCount(ctx); got != len(tc.marked) {
				t.Fatalf("MarkedCount = %d, want %d", got, len(tc.marked))
			}
			if got := w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: 40}).FooterStatus; got != tc.want {
				t.Fatalf("FooterStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

// Unmark takes the marks off rows of the folder they were marked in, and
// off no row of a folder shown later.
func TestUnmarkKeepsToItsGeneration(t *testing.T) {
	ctx := t.Context()
	w := marksWidget(t, "ux-a", "ux-b", "ux-c")
	w.SelectRowIDs(ctx, []string{"ux-a", "ux-b", "ux-c"})
	gen := w.MarksGeneration()

	if n := w.Unmark(ctx, gen, []string{"ux-a", "ux-c", "gone"}); n != 2 {
		t.Fatalf("Unmark removed %d marks, want 2", n)
	}
	if got := markedIDs(t, w); !slices.Equal(got, []string{"ux-b"}) {
		t.Fatalf("marked after Unmark = %v, want [ux-b]", got)
	}

	// Another folder with rows of the same IDs: the old generation's
	// unmark must not reach them.
	w.SetFolder(ctx, configMapsFolder("ux-a", "ux-b"), false)
	if w.MarksGeneration() == gen {
		t.Fatal("a new folder kept the mark generation")
	}
	w.SelectRowIDs(ctx, []string{"ux-a", "ux-b"})
	if n := w.Unmark(ctx, gen, []string{"ux-a", "ux-b"}); n != 0 {
		t.Fatalf("Unmark of an old generation removed %d marks", n)
	}
	if got := markedIDs(t, w); !slices.Equal(got, []string{"ux-a", "ux-b"}) {
		t.Fatalf("marked after a stale Unmark = %v, want [ux-a ux-b]", got)
	}
	if got := w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: 40}).FooterStatus; !strings.HasPrefix(got, "2 marked • ") {
		t.Fatalf("FooterStatus = %q, want the two marks first", got)
	}
}
