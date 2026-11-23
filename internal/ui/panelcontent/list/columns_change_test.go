package list

import (
	"context"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// growingFolder lists rows whose header can change, as an object list does
// when the CPU and memory usage columns arrive.
type growingFolder struct {
	rowsFolder
	cols []table.Column
}

func (f *growingFolder) Columns() []table.Column { return f.cols }

// TestColumnsChangeKeepsCursorAndMarks covers a panel whose header changes
// while the user is on a row: the cursor stays on that row, not on the
// first one, and the marked rows stay marked.
func TestColumnsChangeKeepsCursorAndMarks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	var rows []table.Row
	for i := range 12 {
		name := fmt.Sprintf("pod-%02d", i)
		rows = append(rows, models.NewObjectRow("v1/pods/shop/"+name, []string{name}, nil, gvr, "shop", name, nil))
	}
	f := &growingFolder{rows: rows, cols: []table.Column{{Title: " Name"}}}
	w := New(panelcontent.WidgetDeps{})
	w.UseFolder(true)
	_, _ = w.Update(ctx, tea.WindowSizeMsg{Width: 60, Height: 8})
	w.SetFolder(ctx, f, false)
	w.SelectRowIDs(ctx, []string{"v1/pods/shop/pod-03"})
	w.SelectByRowID(ctx, "v1/pods/shop/pod-07")

	f.cols = []table.Column{{Title: " Name"}, {Title: "CPU"}, {Title: "MEM"}}
	w.RefreshFolder(ctx)

	if got := cursorName(ctx, t, w); got != "pod-07" {
		t.Errorf("cursor on %q after the columns changed, want pod-07", got)
	}
	marked := w.SelectedItems(ctx)
	if len(marked) != 1 || marked[0].Item.(models.ObjectItem).Name() != "pod-03" {
		t.Errorf("marked rows after the columns changed = %v, want pod-03", marked)
	}
}

func cursorName(ctx context.Context, t *testing.T, w *Widget) string {
	t.Helper()
	item, ok := w.SelectedNavItem(ctx)
	if !ok {
		return ""
	}
	obj, ok := item.(models.ObjectItem)
	if !ok {
		t.Fatalf("cursor row is %T", item)
	}
	return obj.Name()
}
