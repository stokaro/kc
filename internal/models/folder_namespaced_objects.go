package models

import (
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NamespacedObjectsFolder lists namespace-scoped resources for a GVR.
type NamespacedObjectsFolder struct {
	*ObjectsFolder
	release func()
}

// NewNamespacedObjectsFolder constructs a namespaced objects folder. In a
// namespace, a user who may not list the type in all namespaces gets a
// cache limited to that namespace; the folder holds it until Close.
func NewNamespacedObjectsFolder(deps Deps, gvr schema.GroupVersionResource, namespace string, path, verbs []string) *NamespacedObjectsFolder {
	release := noRelease
	if namespace != AllNamespaces {
		deps, _, release = deps.resolveListScope(gvr, namespace)
	}
	folder := &NamespacedObjectsFolder{
		ObjectsFolder: NewObjectsFolder(deps, gvr, namespace, path, verbs),
		release:       sync.OnceFunc(release),
	}
	return folder
}

// Close stops the folder's watch and releases its namespace cache.
func (f *NamespacedObjectsFolder) Close() {
	f.ObjectsFolder.Close()
	f.release()
}
