package models

import (
	"context"

	table "github.com/sttts/kc/internal/table"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// AllNamespaces is the namespace scope that covers every namespace, like
// kubectl -A.
const AllNamespaces = ""

// RootFolder is a panel's "/": the resource types visible in its namespace
// scope, then namespaces and the cluster-scoped types. The scope is a
// property of the panel (shown in its frame), not a level of the tree;
// /namespaces/<name> stays available for explicit navigation.
type RootFolder struct {
	*ClusterResourcesFolder
	namespace string
}

// NewRootFolder scaffolds a root folder scoped to namespace; AllNamespaces
// lists namespaced types across all namespaces.
func NewRootFolder(deps Deps, namespace string) *RootFolder {
	cluster := NewClusterResourcesFolder(deps, nil)
	root := &RootFolder{ClusterResourcesFolder: cluster, namespace: namespace}
	cluster.BaseFolder.SetPopulate(root.populate)
	return root
}

// NamespaceScope returns the namespace the root lists, or AllNamespaces.
func (f *RootFolder) NamespaceScope() string { return f.namespace }

func (f *RootFolder) populate(ctx context.Context) ([]table.Row, error) {
	rows := make([]table.Row, 0, 64)
	// The overview costs nothing until it is entered.
	rows = append(rows, overviewRootItem(f.Deps, f.namespace, f.Path()))
	nameStyle := WhiteStyle()

	gvrNamespaces := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	nsPath := append(append([]string{}, f.Path()...), "namespaces")
	nsPathCopy := append([]string(nil), nsPath...)
	namespacesSpec := resourceGroupSpec{
		id:        "namespaces",
		cells:     []string{"/namespaces", "v1", ""},
		path:      nsPathCopy,
		detail:    "namespaces (v1)",
		style:     nameStyle,
		gvr:       gvrNamespaces,
		gvk:       schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"},
		watchable: true,
		enter: func() (Folder, error) {
			return NewClusterObjectsFolder(f.Deps, gvrNamespaces, nsPathCopy, NamespaceResourceVerbs()), nil
		},
	}

	groupItems, err := namespacedResourceSpecs(f.ResourcesFolder, f.namespace, f.Path(), true)
	if err != nil {
		return nil, err
	}
	groupItems = append(groupItems, namespacesSpec)
	clusterItems, err := f.ClusterResourcesFolder.resourceGroupSpecs()
	if err != nil {
		return nil, err
	}
	groupItems = append(groupItems, clusterItems...)
	rows = append(rows, f.ResourcesFolder.finalize(ctx, groupItems)...)

	return rows, nil
}
