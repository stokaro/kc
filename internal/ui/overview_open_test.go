package ui

import (
	"testing"

	"github.com/sttts/kc/internal/models"
	navui "github.com/sttts/kc/internal/navigation"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// listFolder is an object list of one namespace (or a cluster-scoped list).
type listFolder struct {
	models.Folder
	namespace  string
	namespaced bool
}

func (f listFolder) ObjectListMeta() (schema.GroupVersionResource, string, bool) {
	return schema.GroupVersionResource{Version: "v1", Resource: "pods"}, f.namespace, f.namespaced
}

// scopedFolder is a folder that knows its namespace scope (a root).
type scopedFolder struct {
	models.Folder
	namespace string
}

func (f scopedFolder) NamespaceScope() string { return f.namespace }

func TestOverviewNamespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		folder models.Folder
		scope  string
		want   string
	}{
		{name: "a namespace's list in an all-namespaces panel", folder: listFolder{namespace: "shop", namespaced: true}, scope: models.AllNamespaces, want: "shop"},
		{name: "a list across namespaces", folder: listFolder{namespace: models.AllNamespaces, namespaced: true}, scope: "shop", want: models.AllNamespaces},
		{name: "a cluster-scoped list keeps the panel scope", folder: listFolder{namespaced: false}, scope: "shop", want: "shop"},
		{name: "a root", folder: scopedFolder{namespace: "web"}, scope: "web", want: "web"},
		{name: "another folder keeps the panel scope", folder: models.NewBaseFolder(models.Deps{}, nil, nil), scope: "db", want: "db"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &App{}
			a.panelScope[0] = tc.scope
			a.leftNav = navui.NewNavigator(tc.folder)
			if got := a.overviewNamespace(0); got != tc.want {
				t.Fatalf("overviewNamespace = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOverviewPathRescopesToTheRootRow(t *testing.T) {
	t.Parallel()
	p := overviewPath("shop")
	if !p.Rescope || p.Namespace != "shop" || p.Anchor != AnchorRoot || len(p.Elems) != 1 || p.Elems[0].ID != models.OverviewRowID {
		t.Fatalf("overviewPath = %+v", p)
	}
}
