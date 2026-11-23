package models

import (
	"context"
	"errors"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/helm"
)

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// helmAccessError explains why kc cannot read Helm releases in a namespace
// scope. It unwraps to the API server's Forbidden error.
type helmAccessError struct {
	namespace string
	cause     error
}

func (e *helmAccessError) Error() string {
	if e.namespace == AllNamespaces {
		return "listing and watching Secrets in all namespaces is forbidden; kc reads Helm releases from Secrets"
	}
	return fmt.Sprintf("listing and watching Secrets in %s is forbidden; kc reads Helm releases from Secrets", e.namespace)
}

func (e *helmAccessError) Unwrap() error { return e.cause }

// helmScope is the access decision for one namespace scope: the Deps whose
// cache serves its Secrets (shared or namespace-scoped), or why none can.
type helmScope struct {
	deps    Deps
	denied  *helmAccessError
	release func()
}

func (sc *helmScope) result() (Deps, error) {
	if sc.denied != nil {
		return Deps{}, sc.denied
	}
	return sc.deps, nil
}

// helmReader reads Helm release records from the informer cache the user
// may use for Secrets in a namespace scope. It is shared by the folders
// below one /helm entry, so a record is decoded once and a namespace cache
// is acquired once; the releases folder closes it.
type helmReader struct {
	// resolveMu serializes decisions, so a namespace cache is acquired once
	// even when two folders load at the same time.
	resolveMu sync.Mutex

	mu      sync.Mutex
	scopes  map[string]*helmScope
	closed  bool
	decoded map[string]decodedRelease

	// resolve picks the cache for Secrets in a namespace scope (see
	// resolveListScope); peek confirms the choice with one direct list.
	// Tests replace both.
	resolve func(namespace string) (Deps, listScope, func())
	peek    func(ctx context.Context, deps Deps, namespace string) error
}

type decodedRelease struct {
	rel *helm.Release
	err error
}

func newHelmReader(deps Deps) *helmReader {
	r := &helmReader{scopes: make(map[string]*helmScope), decoded: make(map[string]decodedRelease)}
	r.resolve = func(namespace string) (Deps, listScope, func()) {
		return deps.resolveListScope(secretsGVR, namespace)
	}
	r.peek = peekHelmSecrets
	return r
}

// decided returns the decision for namespace if one was made. It never
// blocks on the network.
func (r *helmReader) decided(namespace string) (*helmScope, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sc, ok := r.scopes[namespace]
	return sc, ok
}

// errHelmReaderClosed is returned by a load that finished after the view
// closed.
var errHelmReaderClosed = errors.New("helm view closed")

// scopeFor decides once per namespace scope which cache serves its Secrets,
// as every namespaced folder does (docs/adr/0056-namespace-scoped-access.md):
// the shared cache when Secrets may be listed and watched in all namespaces,
// a namespace cache when only the namespace allows it. One direct list then
// confirms the choice, because an unknown review answer is no permission.
// A forbidden answer is kept for the scope; other errors are retried on the
// next call. A denial in one namespace never affects another.
func (r *helmReader) scopeFor(ctx context.Context, namespace string) (Deps, error) {
	if sc, ok := r.decided(namespace); ok {
		return sc.result()
	}
	r.resolveMu.Lock()
	defer r.resolveMu.Unlock()
	if sc, ok := r.decided(namespace); ok {
		return sc.result()
	}

	deps, scope, release := r.resolve(namespace)
	sc := &helmScope{deps: deps, release: release}
	if scope == scopeDenied {
		sc = &helmScope{denied: &helmAccessError{namespace: namespace, cause: r.deniedCause(ctx, deps, namespace)}, release: noRelease}
		release()
	} else if err := r.peek(ctx, deps, namespace); err != nil {
		release()
		if !apierrors.IsForbidden(err) {
			return Deps{}, err
		}
		sc = &helmScope{denied: &helmAccessError{namespace: namespace, cause: err}, release: noRelease}
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		sc.release()
		return Deps{}, errHelmReaderClosed
	}
	r.scopes[namespace] = sc
	r.mu.Unlock()
	return sc.result()
}

// deniedCause returns the API server's refusal for a denied scope, so the
// notice can quote it. When the list itself is allowed (the review denied
// watch), it says that instead.
func (r *helmReader) deniedCause(ctx context.Context, deps Deps, namespace string) error {
	if err := r.peek(ctx, deps, namespace); apierrors.IsForbidden(err) {
		return err
	}
	return apierrors.NewForbidden(secretsGVR.GroupResource(), "", errors.New("an access review denied list or watch"))
}

// Close gives up the namespace caches the reader acquired. A load that
// finishes afterwards releases what it acquired at once.
func (r *helmReader) Close() {
	r.mu.Lock()
	scopes := r.scopes
	r.scopes = make(map[string]*helmScope)
	r.closed = true
	r.mu.Unlock()
	for _, sc := range scopes {
		sc.release()
	}
}

// peekHelmSecrets lists at most one Helm release Secret, bypassing the cache.
func peekHelmSecrets(ctx context.Context, deps Deps, namespace string) error {
	if deps.Cl == nil {
		return nil
	}
	selector := labels.SelectorFromSet(labels.Set{helm.OwnerLabel: helm.OwnerValue}).String()
	ctrllog.FromContext(ctx).WithName("helm").Info("client list", "gvr", secretsGVR.String(), "namespace", namespace, "labelSelector", selector, "limit", 1)
	res := deps.Cl.Dynamic().Resource(secretsGVR)
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
// optionally of one release, from the cache chosen for namespace.
func (r *helmReader) records(ctx context.Context, namespace, release string) ([]helm.Record, error) {
	deps, err := r.scopeFor(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if deps.Cl == nil {
		return nil, nil
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
	if err := deps.Cl.GetCache().List(ctx, list, opts...); err != nil {
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
// release, through the cache chosen for namespace. Until that decision is
// made it starts nothing: an informer without permission would retry
// forever.
func (r *helmReader) watchFactory(namespace, release string) func(func(), func()) (func(), error) {
	return func(onEvent, onStop func()) (func(), error) {
		sc, ok := r.decided(namespace)
		if !ok {
			return nil, nil
		}
		deps, err := sc.result()
		if err != nil {
			return nil, err
		}
		filter := func(obj metav1.Object) bool {
			l := obj.GetLabels()
			return l[helm.OwnerLabel] == helm.OwnerValue && (release == "" || l[helm.NameLabel] == release)
		}
		return startFilteredInformer(deps, secretsGVR, namespace, "", filter, onEvent, onStop)
	}
}

// isHelmAccessError reports whether err is a denied access decision.
func isHelmAccessError(err error) (*helmAccessError, bool) {
	var ae *helmAccessError
	ok := errors.As(err, &ae)
	return ae, ok
}
