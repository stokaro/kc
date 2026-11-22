package models

// White-box: the filter lives in liveObjectRowSource, and a real
// ObjectsFolder needs a cluster to load rows. These tests drive the source
// with a fake populate to check that the filter survives list reloads.

import (
	"context"
	"testing"

	table "github.com/sttts/kc/internal/table"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func factRow(name string, health RowHealth, lbls map[string]string) table.Row {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	row := NewObjectRow(name, []string{name}, []string{"pods", name}, gvr, "default", name, WhiteStyle())
	row.setRowFacts(RowFacts{Name: name, ID: name, Labels: lbls, Health: health})
	return row
}

func rowIDs(rows []table.Row) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		id, _, _, _ := r.Columns()
		ids = append(ids, id)
	}
	return ids
}

func TestLiveObjectRowSourceFilterSurvivesReload(t *testing.T) {
	loads := [][]table.Row{
		{
			factRow("api", RowHealthOK, map[string]string{"app": "api"}),
			factRow("web-1", RowHealthNotReady, map[string]string{"app": "web"}),
			factRow("web-2", RowHealthOK, map[string]string{"app": "web"}),
		},
		{
			factRow("api", RowHealthNotReady, map[string]string{"app": "api"}),
			factRow("web-2", RowHealthOK, map[string]string{"app": "web"}),
			factRow("web-3", RowHealthOK, map[string]string{"app": "web"}),
		},
	}
	load := 0
	src := newLiveObjectRowSourceWithHooks(func(context.Context) ([]table.Row, error) {
		return loads[load], nil
	}, nil, nil)
	ctx := t.Context()

	if got := src.Len(ctx); got != 3 {
		t.Fatalf("Len() = %d before filtering, want 3", got)
	}
	f, err := ParseRowFilter("-l app=web")
	if err != nil {
		t.Fatal(err)
	}
	src.setRowFilter(f)
	assertIDs(t, src.Lines(ctx, 0, 10), "web-1", "web-2")
	if _, ok := src.ItemByID(ctx, "api"); ok {
		t.Errorf("ItemByID(api) found a hidden row")
	}
	if idx, _, ok := src.Find(ctx, "web-2"); !ok || idx != 1 {
		t.Errorf("Find(web-2) = %d, %v; want 1, true", idx, ok)
	}

	// A live update: rows appear and disappear, the filter stays.
	load = 1
	src.MarkDirty()
	assertIDs(t, src.Lines(ctx, 0, 10), "web-2", "web-3")

	// Faults only on top of the selector.
	f.FaultsOnly = true
	src.setRowFilter(f)
	assertIDs(t, src.Lines(ctx, 0, 10))

	f, _ = ParseRowFilter("")
	f.FaultsOnly = true
	src.setRowFilter(f)
	assertIDs(t, src.Lines(ctx, 0, 10), "api")
	if got := src.rowFilter(); !got.FaultsOnly {
		t.Errorf("rowFilter() lost FaultsOnly")
	}

	src.setRowFilter(RowFilter{})
	assertIDs(t, src.Lines(ctx, 0, 10), "api", "web-2", "web-3")
}

func TestRowFilterKeepsRowsWithoutFacts(t *testing.T) {
	f, err := ParseRowFilter("nothing-matches")
	if err != nil {
		t.Fatal(err)
	}
	if !f.keepRow(BackItem{}) {
		t.Errorf("filter hid the back row")
	}
	plain := NewObjectRow("x", []string{"x"}, nil, schema.GroupVersionResource{}, "", "x", WhiteStyle())
	if !f.keepRow(plain) {
		t.Errorf("filter hid a row without facts")
	}
}

func TestObjectsFolderRowsCarryFilterFacts(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	folder := NewObjectsFolder(Deps{}, gvr, "default", []string{"namespaces", "default", "pods"}, nil)
	list := &unstructured.UnstructuredList{}
	pending := unstructured.Unstructured{Object: map[string]interface{}{"status": map[string]interface{}{"phase": "Pending"}}}
	pending.SetName("pending")
	pending.SetLabels(map[string]string{"app": "web"})
	list.Items = append(list.Items, pending)

	rows := folder.rowsFromList(list, "name")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	c, ok := rows[0].(rowFactsCarrier)
	if !ok {
		t.Fatalf("row %T carries no filter facts", rows[0])
	}
	facts, ok := c.rowFacts()
	if !ok || facts.Name != "pending" || facts.ID != "pending" || facts.Labels["app"] != "web" || facts.Health != RowHealthNotReady {
		t.Fatalf("facts = %+v, %v", facts, ok)
	}
}

func assertIDs(t *testing.T, rows []table.Row, want ...string) {
	t.Helper()
	got := rowIDs(rows)
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
}
