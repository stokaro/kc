package ui

import (
	"context"
	"errors"
	"fmt"

	kccluster "github.com/sttts/kc/internal/cluster"
	models "github.com/sttts/kc/internal/models"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
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
	return deleteTarget{
		panelIdx:  panelIdx,
		gvr:       obj.GVR(),
		namespace: obj.Namespace(),
		name:      obj.Name(),
		uid:       obj.UID(),
		cluster:   origin.Cluster,
		context:   origin.Context,
	}, nil
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
	ref := kubectlResourceRef(msg.target.gvr, msg.target.name)
	switch {
	case apierrors.IsConflict(msg.err):
		return fmt.Sprintf("%s was replaced by a new object with the same name; nothing was deleted. Select it again to delete the new one", ref)
	case apierrors.IsNotFound(msg.err):
		return fmt.Sprintf("%s is already gone; nothing was deleted", ref)
	default:
		return fmt.Sprintf("Delete %s failed: %v", ref, msg.err)
	}
}
