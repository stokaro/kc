package models

import "k8s.io/apimachinery/pkg/runtime/schema"

// ResourceTypeItem is a row that stands for a resource type in a namespace
// (or cluster-wide when the namespace is empty), such as "pods" in a
// namespace's resource list. The access view inspects it like an object
// without a name.
type ResourceTypeItem interface {
	Item
	ResourceType() (gvr schema.GroupVersionResource, namespace string)
}

var _ ResourceTypeItem = (*ResourceGroupItem)(nil)

// ResourceType returns the resource and namespace the row lists.
func (r *ResourceGroupItem) ResourceType() (schema.GroupVersionResource, string) {
	return r.gvr, r.namespace
}
