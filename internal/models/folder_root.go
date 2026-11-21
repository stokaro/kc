package models

import (
	"context"
	"fmt"

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
	enterContext func(name string, basePath []string) (Folder, error)
	namespace    string
}

// NewRootFolder scaffolds a root folder scoped to namespace; AllNamespaces
// lists namespaced types across all namespaces.
func NewRootFolder(deps Deps, enterContext func(name string, basePath []string) (Folder, error), namespace string) *RootFolder {
	cluster := NewClusterResourcesFolder(deps, nil)
	root := &RootFolder{ClusterResourcesFolder: cluster, enterContext: enterContext, namespace: namespace}
	cluster.BaseFolder.SetPopulate(root.populate)
	return root
}

// NamespaceScope returns the namespace the root lists, or AllNamespaces.
func (f *RootFolder) NamespaceScope() string { return f.namespace }

func (f *RootFolder) populate(ctx context.Context) ([]table.Row, error) {
	cfg := f.Deps.AppConfig
	showNonEmpty := cfg.Resources.ShowNonEmptyOnly

	rows := make([]table.Row, 0, 64)
	nameStyle := WhiteStyle()

	if count := len(f.Deps.KubeConfig.Contexts); count > 0 {
		itemPath := append(append([]string{}, f.Path()...), "contexts")
		enter := func() (Folder, error) {
			return NewContextsFolder(f.Deps, f.enterContext), nil
		}
		item := NewContextListItem("contexts", []string{"/contexts", "", ""}, itemPath, GreenStyle(), count, enter)
		if !(showNonEmpty && item.Empty()) {
			item.Cells[2] = fmt.Sprintf("%d", item.Count())
			rows = append(rows, item)
		}
	}

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
