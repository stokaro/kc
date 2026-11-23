package hotlist_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/hotlist"
)

func TestViewsRoundTrip(t *testing.T) {
	t.Parallel()
	off := false
	view := hotlist.View{Columns: "wide", Order: "-mem", GroupByNamespace: &off, Filter: "-n prod,stage cpu>500m", FaultsOnly: true}
	entries := bookmarks(pods)
	entries, err := hotlist.AddView(entries, " payments failures ", web, view)
	if err != nil {
		t.Fatalf("AddView: %v", err)
	}
	path := filepath.Join(t.TempDir(), hotlist.FileName)
	if err := hotlist.Save(path, entries); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := hotlist.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].View != nil || got[0].Name != "" {
		t.Fatalf("bookmark changed: %+v", got)
	}
	v := got[1]
	if v.Name != "payments failures" || v.Label() != "payments failures" || !v.Location.Equal(web) {
		t.Fatalf("view entry = %+v", v)
	}
	if v.View == nil || !reflect.DeepEqual(*v.View, view) {
		t.Fatalf("view = %+v, want %+v", v.View, view)
	}
	if got[0].Label() != "/pods" {
		t.Fatalf("bookmark label = %q", got[0].Label())
	}
}

func TestLoadIgnoresUnknownFields(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), hotlist.FileName)
	content := "entries:\n- context: mba\n  namespace: default\n  path: /pods\n  steps: [\"default//v1/pods\"]\n  name: cpu\n  color: red\n  view:\n    order: -cpu\n    panelMode: logs\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := hotlist.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Name != "cpu" || got[0].View == nil || got[0].View.Order != "-cpu" {
		t.Fatalf("loaded %+v", got)
	}
}

func TestEditViews(t *testing.T) {
	t.Parallel()
	entries, err := hotlist.AddView(bookmarks(pods), "CPU", prod, hotlist.View{Order: "-cpu"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hotlist.AddView(entries, "cpu", web, hotlist.View{}); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("duplicate name: err = %v", err)
	}
	if _, err := hotlist.AddView(entries, "  ", web, hotlist.View{}); err == nil {
		t.Fatalf("empty name accepted")
	}

	updated, err := hotlist.UpdateView(entries, 1, web, hotlist.View{Order: "-mem"})
	if err != nil {
		t.Fatalf("UpdateView: %v", err)
	}
	if updated[1].Name != "CPU" || !updated[1].Location.Equal(web) || updated[1].View.Order != "-mem" {
		t.Fatalf("updated = %+v", updated[1])
	}
	if entries[1].View.Order != "-cpu" || !entries[1].Location.Equal(prod) {
		t.Fatalf("UpdateView changed its input: %+v", entries[1])
	}
	if _, err := hotlist.UpdateView(entries, 0, web, hotlist.View{}); err == nil {
		t.Fatalf("UpdateView turned a bookmark into a view")
	}

	renamed, err := hotlist.Rename(entries, 1, "cluster CPU consumers")
	if err != nil || renamed[1].Name != "cluster CPU consumers" || entries[1].Name != "CPU" {
		t.Fatalf("Rename = %+v, %v", renamed, err)
	}
	if _, err := hotlist.Rename(entries, 1, "cpu"); err != nil {
		t.Fatalf("renaming a view to its own name in another case: %v", err)
	}
	if _, err := hotlist.Rename(entries, 5, "x"); err == nil {
		t.Fatalf("Rename out of range accepted")
	}
}
