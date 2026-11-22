package models

// White-box: orderRowIndices and rowsFromRowList are unexported, and a
// black-box test would need a live table cache to see the order.

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/tablecache"
	appconfig "github.com/stokaro/kc/pkg/appconfig"
)

func nsNames(items []tablecache.Row, idxs []int) []string {
	out := make([]string, len(idxs))
	for i, ii := range idxs {
		out[i] = items[ii].Namespace + "/" + items[ii].Name
	}
	return out
}

func TestOrderRowIndicesAcrossNamespaces(t *testing.T) {
	t.Parallel()
	cols := []metav1.TableColumnDefinition{
		{Name: "Name"},
		{Name: "Restarts"},
		{Name: "CPU"},
		{Name: "MEM"},
	}
	items := []tablecache.Row{
		sortRow("prod", "api", 1, "3 (5m ago)", "250m", "1Gi"),
		sortRow("dev", "api", 2, "0", "1500m", "300Mi"),
		sortRow("prod", "db", 3, "12", "1", "4Gi"),
		sortRow("dev", "cache", 4, "3", "n/a", "n/a"),
		sortRow("kube-system", "dns", 5, "<none>", "2m", "20Mi"),
	}
	cases := []struct {
		name   string
		order  string
		layout namespaceLayout
		want   []string
	}{
		{"memory descending across namespaces, units compared, n/a last", "-mem", mixedNamespaces,
			[]string{"prod/db", "prod/api", "dev/api", "kube-system/dns", "dev/cache"}},
		{"memory ascending, n/a still last", "mem", mixedNamespaces,
			[]string{"kube-system/dns", "dev/api", "prod/api", "prod/db", "dev/cache"}},
		{"cpu descending: 1500m over 1 over 250m", "-cpu", mixedNamespaces,
			[]string{"dev/api", "prod/db", "prod/api", "kube-system/dns", "dev/cache"}},
		{"restarts ties break by name, missing last", "-restarts", mixedNamespaces,
			[]string{"prod/db", "prod/api", "dev/cache", "dev/api", "kube-system/dns"}},
		{"same names break by namespace", "name", mixedNamespaces,
			[]string{"dev/api", "prod/api", "dev/cache", "prod/db", "kube-system/dns"}},
		{"same names keep namespace ascending when names descend", "-name", mixedNamespaces,
			[]string{"kube-system/dns", "prod/db", "dev/cache", "dev/api", "prod/api"}},
		{"namespace sort matches the grouped layout", "-namespace", mixedNamespaces,
			[]string{"prod/api", "prod/db", "kube-system/dns", "dev/api", "dev/cache"}},
		{"grouped keeps memory inside each namespace", "-mem", groupedNamespaces,
			[]string{"dev/api", "dev/cache", "kube-system/dns", "prod/db", "prod/api"}},
		{"grouped namespace sort", "-namespace", groupedNamespaces,
			[]string{"prod/api", "prod/db", "kube-system/dns", "dev/api", "dev/cache"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			idxs, _ := orderRowIndices(items, cols, tc.order, tc.layout)
			if got := nsNames(items, idxs); !slices.Equal(got, tc.want) {
				t.Errorf("order %q = %v\nwant %v", tc.order, got, tc.want)
			}
		})
	}
	t.Run("input order does not change the result", func(t *testing.T) {
		t.Parallel()
		want, _ := orderRowIndices(items, cols, "-restarts", mixedNamespaces)
		rev := slices.Clone(items)
		slices.Reverse(rev)
		got, _ := orderRowIndices(rev, cols, "-restarts", mixedNamespaces)
		if a, b := nsNames(items, want), nsNames(rev, got); !slices.Equal(a, b) {
			t.Errorf("reversed input = %v, want %v", b, a)
		}
	})
}

func folderRowIDs(t *testing.T, folder *ObjectsFolder, rl *tablecache.RowList, order string) []string {
	t.Helper()
	rows := folder.rowsFromRowList(rl, appconfig.ColumnsModeNormal, order, nil)
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i], _, _, _ = r.Columns()
	}
	return ids
}

func TestObjectsFolderNamespaceGrouping(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	rl := &tablecache.RowList{
		Columns: []metav1.TableColumnDefinition{{Name: "Name"}, {Name: "MEM"}},
		Items: []tablecache.Row{
			sortRow("a", "web", 1, "100Mi"),
			sortRow("b", "web", 2, "2Gi"),
			sortRow("a", "db", 3, "1Gi"),
		},
	}

	t.Run("configured default groups", func(t *testing.T) {
		t.Parallel()
		cfg := appconfig.Default()
		folder := NewObjectsFolder(Deps{AppConfig: cfg}, gvr, "", []string{"pods"}, nil)
		if got, want := folderRowIDs(t, folder, rl, "-mem"), []string{"a/db", "a/web", "b/web"}; !slices.Equal(got, want) {
			t.Errorf("grouped rows = %v, want %v", got, want)
		}
		if on, applies := folder.NamespaceGrouping(); !on || !applies {
			t.Errorf("NamespaceGrouping = %v, %v; want true, true", on, applies)
		}
	})

	t.Run("config off sorts across namespaces", func(t *testing.T) {
		t.Parallel()
		cfg := appconfig.Default()
		cfg.Objects.GroupByNamespace = false
		folder := NewObjectsFolder(Deps{AppConfig: cfg}, gvr, "", []string{"pods"}, nil)
		if got, want := folderRowIDs(t, folder, rl, "-mem"), []string{"b/web", "a/db", "a/web"}; !slices.Equal(got, want) {
			t.Errorf("rows = %v, want %v", got, want)
		}
	})

	t.Run("toggle keeps rows, ids and sort; live values reorder", func(t *testing.T) {
		t.Parallel()
		folder := NewObjectsFolder(Deps{}, gvr, "", []string{"pods"}, nil)
		grouped := folderRowIDs(t, folder, rl, "-mem")
		folder.ApplyNamespaceGrouping(false)
		mixed := folderRowIDs(t, folder, rl, "-mem")
		if want := []string{"b/web", "a/db", "a/web"}; !slices.Equal(mixed, want) {
			t.Fatalf("mixed rows = %v, want %v", mixed, want)
		}
		// Same rows with the same identities: the cursor and marks follow
		// the ids, and same-named objects stay apart.
		if a, b := slices.Sorted(slices.Values(grouped)), slices.Sorted(slices.Values(mixed)); !slices.Equal(a, b) {
			t.Errorf("rows changed with the grouping: %v vs %v", a, b)
		}
		if got := folder.ObjectOrder(); got != "-mem" {
			t.Errorf("ObjectOrder = %q, want -mem", got)
		}
		if mark, ok := folder.SortMark(); !ok || mark != (SortMark{Column: 2, Desc: true}) {
			t.Errorf("SortMark = %+v, %v", mark, ok)
		}

		// A live update: a/web now uses the most memory.
		next := &tablecache.RowList{Columns: rl.Columns, Items: slices.Clone(rl.Items)}
		next.Items[0] = sortRow("a", "web", 1, "8Gi")
		if got, want := folderRowIDs(t, folder, next, "-mem"), []string{"a/web", "b/web", "a/db"}; !slices.Equal(got, want) {
			t.Errorf("after update rows = %v, want %v", got, want)
		}
	})

	t.Run("one namespace has nothing to group", func(t *testing.T) {
		t.Parallel()
		folder := NewObjectsFolder(Deps{}, gvr, "a", []string{"pods"}, nil)
		one := &tablecache.RowList{Columns: rl.Columns, Items: []tablecache.Row{rl.Items[0], rl.Items[2]}}
		folder.ApplyNamespaceGrouping(false)
		if got, want := folderRowIDs(t, folder, one, "-mem"), []string{"db", "web"}; !slices.Equal(got, want) {
			t.Errorf("rows = %v, want %v", got, want)
		}
		if _, applies := folder.NamespaceGrouping(); applies {
			t.Errorf("NamespaceGrouping applies to a one-namespace list")
		}
	})
}
