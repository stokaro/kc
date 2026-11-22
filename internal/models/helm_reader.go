package models

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/sttts/kc/internal/helm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// helmAccess is what the access probe found out.
type helmAccess int

const (
	helmAccessUnknown helmAccess = iota
	helmAccessOK
	// helmAccessNamespaceOnly: Secrets can be listed in the namespace but
	// not cluster-wide, which the shared cache needs.
	helmAccessNamespaceOnly
	helmAccessDenied
)

// helmAccessError explains why kc cannot read Helm releases.
type helmAccessError struct {
	access    helmAccess
	namespace string
	cause     error
}

func (e *helmAccessError) Error() string {
	switch e.access {
	case helmAccessNamespaceOnly:
		return fmt.Sprintf("Secrets can be listed in %s but not in all namespaces; kc's shared cache watches Secrets cluster-wide", e.namespace)
	default:
		return "listing Secrets is forbidden; kc reads Helm releases from Secrets"
	}
}

func (e *helmAccessError) Unwrap() error { return e.cause }

// helmReader reads Helm release records from the shared cache. It is shared
// by the folders below one /helm entry, so a record is decoded once.
type helmReader struct {
	deps Deps

	mu      sync.Mutex
	access  helmAccess
	denied  *helmAccessError
	decoded map[string]decodedRelease
	// peek is the access probe; tests replace it.
	peek func(ctx context.Context, namespace string) error
}

type decodedRelease struct {
	rel *helm.Release
	err error
}

func newHelmReader(deps Deps) *helmReader {
	r := &helmReader{deps: deps, decoded: map[string]decodedRelease{}}
	r.peek = r.peekSecrets
	return r
}

// accessible reports whether the access probe succeeded. It never blocks.
func (r *helmReader) accessible() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.access == helmAccessOK
}

// checkAccess probes once whether Helm Secrets can be listed. The shared
// cache lists and watches Secrets in all namespaces, so a cluster-wide list
// must be allowed; when it is not, a second probe in namespace tells the
// user whether namespace access alone exists. A forbidden answer is kept;
// other errors are retried on the next call.
func (r *helmReader) checkAccess(ctx context.Context, namespace string) error {
	r.mu.Lock()
	switch r.access {
	case helmAccessOK:
		r.mu.Unlock()
		return nil
	case helmAccessDenied, helmAccessNamespaceOnly:
		err := r.denied
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	err := r.peek(ctx, AllNamespaces)
	if err != nil && !apierrors.IsForbidden(err) {
		return err
	}
	access := helmAccessOK
	var denied *helmAccessError
	if err != nil {
		access = helmAccessDenied
		if namespace != AllNamespaces {
			if nsErr := r.peek(ctx, namespace); nsErr == nil {
				access = helmAccessNamespaceOnly
			}
		}
		denied = &helmAccessError{access: access, namespace: namespace, cause: err}
	}
	r.mu.Lock()
	r.access = access
	r.denied = denied
	r.mu.Unlock()
	if denied != nil {
		return denied
	}
	return nil
}

// peekSecrets lists at most one Helm release Secret, bypassing the cache.
func (r *helmReader) peekSecrets(ctx context.Context, namespace string) error {
	selector := labels.SelectorFromSet(labels.Set{helm.OwnerLabel: helm.OwnerValue}).String()
	ctrllog.FromContext(ctx).WithName("helm").Info("client list", "gvr", secretsGVR.String(), "namespace", namespace, "labelSelector", selector, "limit", 1)
	res := r.deps.Cl.Dynamic().Resource(secretsGVR)
	opts := metav1.ListOptions{LabelSelector: selector, Limit: 1}
	var err error
	if namespace == AllNamespaces {
		_, err = res.List(ctx, opts)
	} else {
		_, err = res.Namespace(namespace).List(ctx, opts)
	}
	return err
}

// records lists the stored revisions in namespace (AllNamespaces for all),
// optionally of one release, from the shared cache.
func (r *helmReader) records(ctx context.Context, namespace, release string) ([]helm.Record, error) {
	if err := r.checkAccess(ctx, namespace); err != nil {
		return nil, err
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "SecretList"})
	set := labels.Set{helm.OwnerLabel: helm.OwnerValue}
	if release != "" {
		set[helm.NameLabel] = release
	}
	opts := []crclient.ListOption{crclient.MatchingLabels(set)}
	if namespace != AllNamespaces {
		opts = append(opts, crclient.InNamespace(namespace))
	}
	if err := r.deps.Cl.GetCache().List(ctx, list, opts...); err != nil {
		return nil, err
	}
	out := make([]helm.Record, 0, len(list.Items))
	for i := range list.Items {
		if rec, ok := helm.RecordFromSecret(&list.Items[i]); ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// history returns the stored revisions of one release; ok is false when
// none is stored.
func (r *helmReader) history(ctx context.Context, namespace, release string) (helm.History, bool, error) {
	recs, err := r.records(ctx, namespace, release)
	if err != nil {
		return helm.History{}, false, err
	}
	for _, h := range helm.Group(recs) {
		if h.Namespace == namespace && h.Release == release {
			return h, true, nil
		}
	}
	return helm.History{}, false, nil
}

// decode decodes a record, remembering the result by record content.
func (r *helmReader) decode(rec helm.Record) (*helm.Release, error) {
	key := rec.Key()
	r.mu.Lock()
	if d, ok := r.decoded[key]; ok {
		r.mu.Unlock()
		return d.rel, d.err
	}
	r.mu.Unlock()
	rel, err := rec.Decode()
	r.mu.Lock()
	r.decoded[key] = decodedRelease{rel: rel, err: err}
	r.mu.Unlock()
	return rel, err
}

// watchFactory watches Helm release Secrets in namespace, optionally of one
// release, through the shared cache. Until the access probe has succeeded
// it starts nothing: a cluster-wide Secret informer without permission
// would retry forever.
func (r *helmReader) watchFactory(namespace, release string) func(func(), func()) (func(), error) {
	return func(onEvent, onStop func()) (func(), error) {
		r.mu.Lock()
		access, denied := r.access, r.denied
		r.mu.Unlock()
		switch access {
		case helmAccessOK:
		case helmAccessUnknown:
			return nil, nil
		default:
			return nil, denied
		}
		filter := func(obj metav1.Object) bool {
			l := obj.GetLabels()
			return l[helm.OwnerLabel] == helm.OwnerValue && (release == "" || l[helm.NameLabel] == release)
		}
		return startFilteredInformer(r.deps, secretsGVR, namespace, "", filter, onEvent, onStop)
	}
}

// isHelmAccessError reports whether err is a denied access probe.
func isHelmAccessError(err error) (*helmAccessError, bool) {
	var ae *helmAccessError
	ok := errors.As(err, &ae)
	return ae, ok
}
