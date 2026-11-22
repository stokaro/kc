package list

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	models "github.com/sttts/kc/internal/models"
	table "github.com/sttts/kc/internal/table"
	panelcontent "github.com/sttts/kc/internal/ui/panelcontent"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Marks live in a set inside the table; SelectedItems must still report
// them in the order the panel shows them, every time.
func TestSelectedItemsFollowListOrder(t *testing.T) {
	ctx := t.Context()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	var rows []table.Row
	for _, nn := range [][2]string{{"a", "web"}, {"b", "web"}, {"c", "api"}, {"d", "web"}, {"e", "db"}} {
		id := "v1/pods/" + nn[0] + "/" + nn[1]
		rows = append(rows, models.NewObjectRow(id, []string{nn[1]}, nil, gvr, nn[0], nn[1], nil))
	}
	w := New(panelcontent.WidgetDeps{})
	w.UseFolder(true)
	_, _ = w.Update(ctx, tea.WindowSizeMsg{Width: 40, Height: 10})
	w.SetFolder(ctx, &rowsFolder{rows: rows}, false)
	w.SelectRowIDs(ctx, []string{"v1/pods/e/db", "v1/pods/a/web", "v1/pods/d/web", "v1/pods/b/web"})

	want := []string{"a/web", "b/web", "d/web", "e/db"}
	for range 20 {
		got := w.SelectedItems(ctx)
		if len(got) != len(want) {
			t.Fatalf("SelectedItems returned %d rows, want %d", len(got), len(want))
		}
		for i, it := range got {
			obj := it.Item.(models.ObjectItem)
			if s := obj.Namespace() + "/" + obj.Name(); s != want[i] {
				t.Fatalf("row %d = %s, want %s (all: %v)", i, s, want[i], want)
			}
		}
	}
}
