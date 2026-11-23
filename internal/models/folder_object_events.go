package models

import (
	"context"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	table "github.com/stokaro/kc/internal/table"
)

// EventsSegment is the path segment of an object's Events.
const EventsSegment = "events"

// hasObjectEvents reports whether objects of gvr in namespace offer
// /events. A cluster-scoped object's Events are in another namespace
// (usually default), so they would need a read across namespaces; an
// Event is not the subject of Events.
func hasObjectEvents(gvr schema.GroupVersionResource, namespace string) bool {
	return namespace != "" && !isEventGVR(gvr)
}

// eventsRow is the /events row inside an object at path.
func eventsRow(deps Deps, gvr schema.GroupVersionResource, namespace, name string, path []string) table.Row {
	p := append(slices.Clone(path), EventsSegment)
	item := NewChildResourceItem(namespace+"/"+name+"/"+EventsSegment, []string{"/" + EventsSegment}, p, func() (Folder, error) {
		return NewObjectEventsFolder(deps, gvr, namespace, name, p), nil
	})
	item.RowItem.details = "The Events about this object, newest first"
	return item
}

// NewObjectEventsFolder lists the core/v1 Events in namespace whose
// subject (involvedObject.uid) is the object gvr namespace/name now
// (docs/adr/0272-object-events.md). It matches by UID, so the Events of
// an earlier object of the same name are not shown. It lists newest Last Seen first until the order is changed.
// ObjectEventsFolder is the /events folder of one object: an Event list
// filtered to that object.
type ObjectEventsFolder struct {
	*ObjectsFolder
}

func NewObjectEventsFolder(deps Deps, gvr schema.GroupVersionResource, namespace, name string, path []string) *ObjectEventsFolder {
	// The Events are read as the namespace's events list reads them: from
	// a namespace cache when the identity may not list them across
	// namespaces.
	scoped, _, release := deps.resolveListScope(ovEventsGVR, namespace)
	folder := NewObjectsFolder(scoped, ovEventsGVR, namespace, path, nil)
	folder.release = release
	folder.objectOrder, folder.hasObjOrder = eventLastSeenKey, true
	// The UID is known only once the object is read; kubectl selects by
	// name, so the command says what kc matches on top.
	folder.kubectlArgs = []string{"--field-selector", "involvedObject.name=" + name}
	folder.kubectlNote = "about " + kubectlResource(gvr) + "/" + name + "; kc also matches its UID"
	// populate runs off the UI goroutine and reads the subject's UID on
	// every load, so a replaced object shows its own Events.
	var (
		mu  sync.Mutex
		uid types.UID
	)
	folder.FilterObject = func(u *unstructured.Unstructured) bool {
		mu.Lock()
		want := uid
		mu.Unlock()
		return eventAbout(u, want)
	}
	listAll := folder.rows.populate
	folder.rows.populate = func(ctx context.Context) ([]table.Row, error) {
		got, err := getParentUID(ctx, deps, gvr, namespace, name)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		uid = got
		mu.Unlock()
		return listAll(ctx)
	}
	return &ObjectEventsFolder{ObjectsFolder: folder}
}

// eventAbout reports whether the Event u is about the object uid. An
// Event without a subject UID is about no object.
func eventAbout(u *unstructured.Unstructured, uid types.UID) bool {
	got, _, _ := unstructured.NestedString(u.Object, "involvedObject", "uid")
	return uid != "" && types.UID(got) == uid
}
