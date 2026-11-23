package models

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
)

func TestChildResourceTypesFolderListsEntries(t *testing.T) {
	deps := Deps{
		AppConfig: appconfig.Default(),
	}
	parentGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	childGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	path := []string{"namespaces", "demo", "deployments", "demo-deploy"}

	folder := NewChildResourceTypesFolder(deps, parentGVR, "demo", "demo-deploy", []schema.GroupVersionResource{childGVR}, path)
	rows, err := folder.populate(t.Context())
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	// A Deployment also gets its logs row, its /diagnosis, its /events
	// and its /timeline, after the child types.
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	if id, _, _, _ := rows[3].Columns(); id != "demo/demo-deploy/"+EventsSegment {
		t.Fatalf("fourth row = %q, want the /events row", id)
	}
	if id, _, _, _ := rows[4].Columns(); id != "demo/demo-deploy/"+TimelineSegment {
		t.Fatalf("last row = %q, want the /timeline row", id)
	}
	if _, ok := rows[1].(WorkloadLogsProvider); !ok {
		t.Fatalf("second row type = %T, want a WorkloadLogsProvider", rows[1])
	}
	if id, _, _, _ := rows[2].Columns(); id != "demo/demo-deploy/"+DiagnosisSegment {
		t.Fatalf("third row = %q, want the /diagnosis row", id)
	}
	item, ok := rows[0].(*ChildResourceItem)
	if !ok {
		t.Fatalf("row type = %T, want *ChildResourceItem", rows[0])
	}
	wantPath := append(path, childGVR.Resource)
	if got := item.Path(); !reflect.DeepEqual(got, wantPath) {
		t.Fatalf("Path() = %v, want %v", got, wantPath)
	}
	if got := folder.Columns(); len(got) != 1 || got[0].Title != " Name" {
		t.Fatalf("Columns() = %v, want single Name column", got)
	}
}

func TestChildResourceTypesFolderMultipleEntries(t *testing.T) {
	deps := Deps{
		AppConfig: appconfig.Default(),
	}
	parentGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	childA := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	childB := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	path := []string{"namespaces", "demo", "services", "svc-a"}

	folder := NewChildResourceTypesFolder(deps, parentGVR, "demo", "svc-a", []schema.GroupVersionResource{childA, childB}, path)
	rows, err := folder.populate(t.Context())
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	// The two child types, then the Service's /diagnosis, /events and
	// /timeline.
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	if id, _, _, _ := rows[2].Columns(); id != "demo/svc-a/"+DiagnosisSegment {
		t.Fatalf("third row = %q, want the diagnosis row", id)
	}
	paths := [][]string{rows[0].(*ChildResourceItem).Path(), rows[1].(*ChildResourceItem).Path()}
	want0 := append(path, childA.Resource)
	want1 := append(path, childB.Resource)
	if !reflect.DeepEqual(paths[0], want0) || !reflect.DeepEqual(paths[1], want1) {
		t.Fatalf("paths = %v, want [%v %v]", paths, want0, want1)
	}
}

func TestChildResourceTypesFolderAppendsRegisteredObjectRows(t *testing.T) {
	deps := Deps{AppConfig: appconfig.Default(), CtxName: "mba"}
	widgets := schema.GroupVersionResource{Group: "rows.example.test", Version: "v1", Resource: "widgets"}
	gadgets := schema.GroupVersionResource{Group: "rows.example.test", Version: "v1", Resource: "gadgets"}
	child := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	RegisterObjectRows(widgets, func(d Deps, namespace, name string, path []string) []table.Row {
		return []table.Row{NewSimpleItem(namespace+"/"+name+"/act", []string{"Act on " + name + " in " + d.CtxName}, path, nil)}
	})
	// The registry is global; a second run (-count=2) must not see two rows.
	t.Cleanup(func() {
		objectRowsMu.Lock()
		delete(objectRows, widgets)
		objectRowsMu.Unlock()
	})

	path := []string{"namespaces", "demo", "widgets", "w1"}
	rows, err := NewChildResourceTypesFolder(deps, widgets, "demo", "w1", []schema.GroupVersionResource{child}, path).populate(t.Context())
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	var cells []string
	for _, r := range rows {
		_, c, _, _ := r.Columns()
		cells = append(cells, c[0])
	}
	if want := []string{"/pods", "/events", "/timeline", "Act on w1 in mba"}; !reflect.DeepEqual(cells, want) {
		t.Fatalf("rows = %q, want %q", cells, want)
	}

	// Rows registered for one resource do not appear under another.
	rows, err = NewChildResourceTypesFolder(deps, gadgets, "demo", "g1", []schema.GroupVersionResource{child}, path).populate(t.Context())
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("gadget rows = %d, want only /pods, /events and /timeline", len(rows))
	}
}
