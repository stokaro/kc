package models

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
)

func objectRowOf(t *testing.T, row table.Row) *ObjectRow {
	t.Helper()
	switch r := row.(type) {
	case *ObjectRow:
		return r
	case *ObjectWithChildItem:
		return r.ObjectRow
	}
	t.Fatalf("unexpected row type %T", row)
	return nil
}

func TestObjectsFolderAllNamespacesUsesObjectNamespace(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	list := &unstructured.UnstructuredList{}
	for _, ns := range []string{"kube-system", "default"} {
		u := unstructured.Unstructured{}
		u.SetAPIVersion("v1")
		u.SetKind("Pod")
		u.SetNamespace(ns)
		u.SetName("coredns")
		list.Items = append(list.Items, u)
	}

	all := NewObjectsFolder(Deps{}, gvr, "", []string{"pods"}, nil)
	rows := all.rowsFromList(list, "", nil)
	if len(rows) != 2 {
		t.Fatalf("expected one row per object, got %d", len(rows))
	}
	seen := map[string]string{}
	for _, row := range rows {
		obj := objectRowOf(t, row)
		id, _, _, _ := row.Columns()
		seen[id] = obj.Namespace()
		if obj.Name() != "coredns" {
			t.Fatalf("row %s: name = %q", id, obj.Name())
		}
	}
	if seen["kube-system/coredns"] != "kube-system" || seen["default/coredns"] != "default" {
		t.Fatalf("expected namespaced IDs and object namespaces, got %v", seen)
	}

	scoped := NewObjectsFolder(Deps{}, gvr, "kube-system", []string{"pods"}, nil)
	rows = scoped.rowsFromList(&unstructured.UnstructuredList{Items: list.Items[:1]}, "", nil)
	id, _, _, _ := rows[0].Columns()
	if obj := objectRowOf(t, rows[0]); id != "coredns" || obj.Namespace() != "kube-system" {
		t.Fatalf("namespaced folder: id=%q namespace=%q", id, obj.Namespace())
	}
}
