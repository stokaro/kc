package models

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ProblemFolder is a folder that can say why it lists nothing, or nothing
// of what the user looked for: the error of its last load when that load
// left the list empty, or a namespace that does not exist. The UI shows
// it in the list, where the user looks, instead of an empty list that
// reads as "there is nothing here".
//
// ListProblem reads memory only; the UI calls it while drawing. It is nil
// when there is nothing to say.
type ProblemFolder interface {
	ListProblem() error
}

// problemSource is a row source that keeps why its list is empty.
type problemSource interface {
	listProblem() error
}

// ListProblem is the problem of the folder's row source, if it keeps one.
func (b *BaseFolder) ListProblem() error {
	if ps, ok := b.rowSource().(problemSource); ok {
		return ps.listProblem()
	}
	return nil
}

var namespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// namespaceCheckTimeout bounds the request behind one namespace check.
const namespaceCheckTimeout = 5 * time.Second

// namespaceMissing asks the API server whether namespace exists. It
// returns the NotFound answer when it does not, and nil when it exists,
// when the user may not read it, and on any other failure: only a missing
// namespace is worth naming. It sends a request; call it off the UI
// goroutine.
func namespaceMissing(ctx context.Context, deps Deps, namespace string) error {
	if namespace == AllNamespaces || deps.Cl == nil || deps.Cl.Dynamic() == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, namespaceCheckTimeout)
	defer cancel()
	log := ctrllog.FromContext(ctx).WithName("rows")
	log.Info("client get", "gvr", namespacesGVR.String(), "name", namespace, "reason", "the list is empty; does the namespace exist")
	_, err := deps.Cl.Dynamic().Resource(namespacesGVR).Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
