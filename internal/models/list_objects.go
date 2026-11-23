package models

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ListObjects reads the objects of gvr in namespace (AllNamespaces for all
// namespaces or a cluster-scoped type) from the same cache an object list
// uses: the shared cache, or a namespace cache for a user who may only read
// in that namespace. It returns an error, never an empty list, when the user
// may not read them. The namespace cache it may acquire is released before
// it returns; the pool keeps it for its TTL.
func ListObjects(ctx context.Context, d Deps, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error) {
	scoped, scope, release := d.resolveListScope(gvr, namespace)
	defer release()
	if scope == scopeDenied {
		where := "all namespaces"
		if namespace != AllNamespaces {
			where = "namespace " + namespace
		}
		return nil, fmt.Errorf("not permitted to list and watch %s in %s", gvr.Resource, where)
	}
	list, err := scoped.Cl.ListByGVR(ctx, gvr, namespace)
	if err != nil {
		return nil, err
	}
	if list == nil {
		return nil, nil
	}
	return list.Items, nil
}
