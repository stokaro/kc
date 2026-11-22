package models

import (
	"context"
	"fmt"

	table "github.com/sttts/kc/internal/table"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
)

var podGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

// ForNodePods derives Deps whose cluster watches only the pods on node
// (field selector spec.nodeName). The node's /pods folder and the drain
// share this cluster, so neither starts a cluster-wide pod watch. The
// caller owns the returned release and calls it when done with the Deps.
func (d Deps) ForNodePods(nodeName string) (Deps, func()) {
	return d.ForSelectorScope(map[schema.GroupVersionResource]crcache.ByObject{
		podGVR: {Field: fields.OneTermEqualSelector("spec.nodeName", nodeName)},
	})
}

// NewNodePodsFolder lists pods scheduled on a specific node using a
// selector-scoped cluster. The folder holds that cluster until Close.
func NewNodePodsFolder(deps Deps, path []string, nodeName string) *ObjectsFolder {
	scoped, release := deps.ForNodePods(nodeName)
	folder := NewObjectsFolder(scoped, podGVR, "", path, nil)
	folder.release = release
	folder.kubectlArgs = []string{"--field-selector", "spec.nodeName=" + nodeName}
	folder.FilterUnstructured = func(u *unstructured.Unstructured) bool {
		if u == nil {
			return false
		}
		n, _, _ := unstructured.NestedString(u.Object, "spec", "nodeName")
		return n == nodeName
	}
	return folder
}

// NodeChildFolder exposes child entries under a node (currently only pods).
type NodeChildFolder struct {
	*BaseFolder
	nodeName string
}

func NewNodeChildFolder(deps Deps, nodeName string, path []string) *NodeChildFolder {
	base := NewBaseFolder(deps, []table.Column{{Title: " Name"}}, path)
	folder := &NodeChildFolder{BaseFolder: base, nodeName: nodeName}
	base.SetPopulate(folder.populate)
	return folder
}

func (f *NodeChildFolder) populate(ctx context.Context) ([]table.Row, error) {
	podsPath := append(append([]string{}, f.Path()...), "pods")
	item := NewChildResourceItem(
		fmt.Sprintf("%s/%s", f.nodeName, podGVR.String()),
		[]string{"/pods"},
		podsPath,
		func() (Folder, error) {
			return NewNodePodsFolder(f.Deps, podsPath, f.nodeName), nil
		},
	)
	return append([]table.Row{item}, f.nodeShellRows()...), nil
}
