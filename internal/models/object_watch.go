package models

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
)

// ObjectSighting is one notification of the shared cache about a named
// object: the instance it names, or that the object is gone.
type ObjectSighting struct {
	UID  types.UID
	Gone bool
}

// WatchObject calls notify for every notification of the shared cache
// about the object namespace/name of gvr, as the timeline recorder does
// (docs/adr/0191-object-timeline.md): an event handler on the informer kc
// already shares, no watch of its own. The informer delivers the current
// object first. notify runs on the informer's goroutine and must not
// block. The returned func removes the handler.
func WatchObject(deps Deps, gvr schema.GroupVersionResource, namespace, name string, notify func(ObjectSighting)) (func(), error) {
	match := func(obj any) (*unstructured.Unstructured, bool) {
		u, ok := obj.(*unstructured.Unstructured)
		return u, ok && u.GetNamespace() == namespace && u.GetName() == name
	}
	seen := func(obj any) {
		if u, ok := match(obj); ok {
			notify(ObjectSighting{UID: u.GetUID()})
		}
	}
	h := toolscache.ResourceEventHandlerFuncs{
		AddFunc:    seen,
		UpdateFunc: func(_, obj any) { seen(obj) },
		DeleteFunc: func(obj any) {
			if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tomb.Obj
			}
			if u, ok := match(obj); ok {
				notify(ObjectSighting{UID: u.GetUID(), Gone: true})
			}
		},
	}
	remove, _, err := addObjectHandler(deps, gvr, h)
	return remove, err
}
