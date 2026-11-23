package models

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// rowObjects maps "namespace/name" to the full object of a row.
type rowObjects map[string]*unstructured.Unstructured

// object returns the object of the row namespace/name, or nil.
func (m rowObjects) object(namespace, name string) *unstructured.Unstructured {
	if m == nil {
		return nil
	}
	return m[namespace+"/"+name]
}

// hasRowHealthRule reports whether rows of gvr are colored and filtered by
// their object: a built-in health rule or a configured adapter.
func hasRowHealthRule(gvr schema.GroupVersionResource) bool {
	if _, ok := healthRules[gvr]; ok {
		return true
	}
	return healthAdapterFor(gvr) != nil
}

// loadRowObjects reads the full objects behind a server table list. The
// table rows carry metadata only (the reader asks for includeObject=Metadata
// to keep manifests out of memory), so health rules, which read status and
// spec, would see nothing. The objects come from the list's own informer
// (startInformerForObjectsFolder), whose events trigger the refresh that
// calls this, so the cache already holds the state the table shows. Events
// are read too, for their occurrence times (eventColumnTimes). Other kinds
// without a health rule read nothing.
func loadRowObjects(ctx context.Context, deps Deps, gvr schema.GroupVersionResource, namespace string) rowObjects {
	if deps.Cl == nil || (!hasRowHealthRule(gvr) && !isEventGVR(gvr)) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, customColumnsLookupTimeout)
	defer cancel()
	list, err := deps.Cl.ListByGVR(ctx, gvr, namespace)
	if err != nil {
		ctrllog.FromContext(ctx).WithName("rowHealth").V(1).Info("objects for row health not available", "gvr", gvr.String(), "namespace", namespace, "error", err.Error())
		return nil
	}
	out := make(rowObjects, len(list.Items))
	for i := range list.Items {
		out[list.Items[i].GetNamespace()+"/"+list.Items[i].GetName()] = &list.Items[i]
	}
	return out
}
