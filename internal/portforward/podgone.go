package portforward

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// PodGoneError ends a forward whose pod was deleted. A pod created again
// under the same name (a StatefulSet pod) is another pod: the forward's
// connection reached the old one, so it ends too.
type PodGoneError struct {
	Namespace string
	Pod       string
}

func (e *PodGoneError) Error() string { return "pod " + e.Pod + " was deleted" }

// Waits between attempts to watch the pod of a forward: after a watch
// that failed, and after one the server closed.
const (
	podWatchRetry  = 5 * time.Second
	podWatchReopen = time.Second
)

// endWhenPodGone runs forward until it returns, and ends it early with a
// *PodGoneError when the pod of spec is deleted. Without the watch a
// forward to a deleted pod looks alive until something connects.
func endWhenPodGone(ctx context.Context, log logr.Logger, pods corev1client.PodInterface, spec Spec, forward func(context.Context) error) error {
	fwdCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		if err := waitPodGone(fwdCtx, log, pods, spec.Namespace, spec.Pod, spec.PodUID); err != nil {
			cancel(err)
		}
	}()
	err := forward(fwdCtx)
	cancel(nil)
	<-watched
	if gone, ok := errors.AsType[*PodGoneError](context.Cause(fwdCtx)); ok {
		return gone
	}
	return err
}

// waitPodGone returns a *PodGoneError once the pod namespace/name with uid
// is gone: deleted, or replaced by a pod with the same name. An empty uid
// is the uid of the first pod it sees. It returns nil when ctx ends, and
// when the user may not list or watch pods: the forward then goes on
// without the check, as before.
func waitPodGone(ctx context.Context, log logr.Logger, pods corev1client.PodInterface, namespace, name string, uid types.UID) error {
	selector := fields.OneTermEqualSelector("metadata.name", name).String()
	log = log.WithValues("gvr", "v1/pods", "namespace", namespace, "fieldSelector", selector)
	gone := &PodGoneError{Namespace: namespace, Pod: name}
	for {
		log.Info("client list", "reason", "end the port forward when its pod is deleted")
		list, err := pods.List(ctx, metav1.ListOptions{FieldSelector: selector})
		wait := podWatchRetry
		if err == nil {
			if uid == "" && len(list.Items) == 1 {
				uid = list.Items[0].UID
			}
			if !holds(list.Items, uid) {
				return gone
			}
			log.Info("client watch", "resourceVersion", list.ResourceVersion)
			var w watch.Interface
			w, err = pods.Watch(ctx, metav1.ListOptions{FieldSelector: selector, ResourceVersion: list.ResourceVersion, AllowWatchBookmarks: true})
			if err == nil {
				var deleted bool
				deleted, err = podDeleted(w, uid)
				w.Stop()
				if deleted {
					return gone
				}
				wait = podWatchReopen
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			log.Info("cannot watch the pod of a port forward; the forward ends only when a connection to it fails", "error", err.Error())
			return nil
		}
		if err != nil {
			log.Info("watching the pod of a port forward failed; trying again", "error", err.Error(), "retry", wait.String())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// holds reports whether pods, the pods with the forward's name, still
// hold the pod with uid.
func holds(pods []corev1.Pod, uid types.UID) bool {
	for _, p := range pods {
		if uid == "" || p.UID == uid {
			return true
		}
	}
	return false
}

// podDeleted reads the watch of the forward's pod until it reports the pod
// with uid deleted or replaced (true), or the watch ends (false, and the
// error of an error event).
func podDeleted(w watch.Interface, uid types.UID) (bool, error) {
	for ev := range w.ResultChan() {
		switch ev.Type {
		case watch.Error:
			return false, apierrors.FromObject(ev.Object)
		case watch.Bookmark:
			continue
		}
		pod, ok := ev.Object.(*corev1.Pod)
		if !ok {
			continue
		}
		if uid != "" && pod.UID != uid {
			// Another pod took the name.
			return true, nil
		}
		if ev.Type == watch.Deleted {
			return true, nil
		}
	}
	return false, nil
}
