package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
)

// deleteTarget is the object instance the user confirmed for deletion: its
// identity at the time the row was read and the cluster it was read from.
type deleteTarget struct {
	panelIdx  int
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	uid       types.UID
	cluster   *kccluster.Cluster
	context   string
	// noun names the object's kind for the user, as kubectl's output
	// does: "pod", "deployment.apps"; label adds namespace/name:
	// "pod shop/web-1".
	noun  string
	label string
	// rowID is the ID of the marked row the target came from, so the row
	// can be unmarked after the delete; empty for the cursor row.
	rowID string
}

// errNoInstance means the row does not say which object instance it shows,
// so a delete by name could hit a different object.
var errNoInstance = errors.New("the row does not carry the object's UID or cluster; refresh the panel and try again")

// deleteTargetFor captures the object instance item shows. It refuses items
// without a UID or origin cluster instead of falling back to a delete by name.
func deleteTargetFor(panelIdx int, item models.Item) (deleteTarget, error) {
	obj, ok := item.(models.InstanceItem)
	if !ok {
		return deleteTarget{}, errNoInstance
	}
	origin := obj.Origin()
	if obj.UID() == "" || origin.Cluster == nil {
		return deleteTarget{}, errNoInstance
	}
	gvr := obj.GVR()
	return deleteTarget{
		panelIdx:  panelIdx,
		gvr:       gvr,
		namespace: obj.Namespace(),
		name:      obj.Name(),
		uid:       obj.UID(),
		cluster:   origin.Cluster,
		context:   origin.Context,
		noun:      objectNoun(gvr, itemKind(obj)),
		label:     objectLabel(gvr, itemKind(obj), obj.Namespace(), obj.Name()),
	}, nil
}

// itemKind returns the kind the row knows, or "". It reads no mapper:
// this runs on the UI thread.
func itemKind(item models.Item) string {
	if k, ok := item.(interface{ Kind() string }); ok {
		return k.Kind()
	}
	return ""
}

// objectLabel names an object as kubectl's output does: the kind in lower
// case, with the API group unless it is the core group, then
// namespace/name. Without a kind it uses the resource name.
func objectLabel(gvr schema.GroupVersionResource, kind, namespace, name string) string {
	return objectNoun(gvr, kind) + " " + objectRef(namespace, name)
}

// objectNoun names a kind as kubectl's output does: in lower case, with
// the API group unless it is the core group. Without a kind it uses the
// resource name.
func objectNoun(gvr schema.GroupVersionResource, kind string) string {
	noun := strings.ToLower(kind)
	if noun == "" {
		noun = gvr.Resource
	}
	if gvr.Group != "" {
		noun += "." + gvr.Group
	}
	return noun
}

// objectRef is namespace/name, or name for a cluster-scoped object.
func objectRef(namespace, name string) string {
	if namespace != "" {
		return namespace + "/" + name
	}
	return name
}

// executeDelete deletes target on the cluster it was read from. It never
// uses the app's current cluster, which may have changed since the
// confirmation was shown.
func executeDelete(ctx context.Context, target deleteTarget) resourceDeletedMsg {
	if target.cluster == nil {
		return resourceDeletedMsg{target: target, err: errNoInstance}
	}
	kind, err := target.cluster.RESTMapper().KindFor(target.gvr)
	if err != nil {
		return resourceDeletedMsg{target: target, err: err}
	}
	return resourceDeletedMsg{target: target, err: deleteObjectInstance(ctx, target.cluster.GetClient(), kind, target)}
}

// deleteObjectInstance deletes the object only while it still has target's
// UID. The API server answers 409 Conflict when the name now belongs to a
// replacement, and 404 when nothing has the name.
func deleteObjectInstance(ctx context.Context, c crclient.Client, kind schema.GroupVersionKind, target deleteTarget) error {
	if target.uid == "" {
		return errNoInstance
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(kind)
	obj.SetName(target.name)
	obj.SetNamespace(target.namespace)
	uid := target.uid
	log := ctrllog.FromContext(ctx).WithName("delete")
	log.Info("deleting object", "context", target.context, "gvr", target.gvr.String(), "namespace", target.namespace, "name", target.name, "uid", target.uid)
	return c.Delete(ctx, obj, crclient.Preconditions{UID: &uid})
}

// deleteResultText describes a failed delete for the user. Conflict and
// NotFound mean nothing was deleted; both are reported as such so the user
// selects the object again rather than assuming the delete went through.
func deleteResultText(msg resourceDeletedMsg) string {
	ref := msg.target.label
	switch {
	case apierrors.IsConflict(msg.err):
		return fmt.Sprintf("%s was replaced by a new object with the same name; nothing was deleted. Select it again to delete the new one", ref)
	case apierrors.IsNotFound(msg.err):
		return fmt.Sprintf("%s is already gone; nothing was deleted", ref)
	default:
		return fmt.Sprintf("Delete %s failed: %v", ref, msg.err)
	}
}
