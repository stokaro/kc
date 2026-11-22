package models

import (
	"context"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// relatedScope is where one (type, namespace) pair of a related lookup is
// read: the shared cache, a namespace cache, or nowhere because the user
// may not list and watch it there.
type relatedScope struct {
	reader RelatedReader
	deps   Deps // the Deps of reader, for watches
	scope  listScope
}

// relatedResolver decides the scope of one (type, namespace) pair. release
// gives up a namespace cache it acquired and is never nil.
type relatedResolver func(gvr schema.GroupVersionResource, namespace string) (relatedScope, func())

// scopedReader is the RelatedReader of a related folder. A related lookup
// reads other types than the folder it starts from, so each (type,
// namespace) pair goes through the same decision as an objects folder
// (ADR 0056): the shared cache when the user may list the type everywhere,
// a namespace cache when only there, and a Forbidden error without any
// request when not at all. A shared informer for a type the user cannot
// list would never sync and would retry its list in the background.
//
// Decisions and namespace caches are kept until Close.
type scopedReader struct {
	resolve relatedResolver

	mu       sync.Mutex
	scopes   map[relatedScopeKey]relatedScope
	releases []func()
	closed   bool
}

type relatedScopeKey struct {
	gvr       schema.GroupVersionResource
	namespace string
}

var _ RelatedReader = (*scopedReader)(nil)

func newScopedReader(resolve relatedResolver) *scopedReader {
	return &scopedReader{resolve: resolve, scopes: make(map[relatedScopeKey]relatedScope)}
}

// depsResolver resolves through deps.resolveListScope; deps must be the
// unscoped Deps of the context.
func depsResolver(deps Deps) relatedResolver {
	return func(gvr schema.GroupVersionResource, namespace string) (relatedScope, func()) {
		d, scope, release := deps.resolveListScope(gvr, namespace)
		var r RelatedReader
		if d.Cl != nil {
			r = d.Cl
		}
		return relatedScope{reader: r, deps: d, scope: scope}, release
	}
}

// scopeFor returns the decision for gvr in namespace, deciding on first use.
// The first decision for a pair may send access reviews, so call it off the
// UI thread.
func (s *scopedReader) scopeFor(gvr schema.GroupVersionResource, namespace string) relatedScope {
	key := relatedScopeKey{gvr: gvr, namespace: namespace}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return relatedScope{scope: scopeDenied}
	}
	if sc, ok := s.scopes[key]; ok {
		s.mu.Unlock()
		return sc
	}
	s.mu.Unlock()
	// Decide without the lock: it may wait on access reviews.
	sc, release := s.resolve(gvr, namespace)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		release()
		return relatedScope{scope: scopeDenied}
	}
	if prev, ok := s.scopes[key]; ok {
		// Another lookup decided meanwhile; keep the first decision.
		release()
		return prev
	}
	s.scopes[key] = sc
	s.releases = append(s.releases, release)
	return sc
}

// decided returns the decision for a pair if one was made, without deciding.
// It is safe on the UI thread.
func (s *scopedReader) decided(gvr schema.GroupVersionResource, namespace string) (relatedScope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.scopes[relatedScopeKey{gvr: gvr, namespace: namespace}]
	return sc, ok
}

// deniedError is the Forbidden error of a pair the user may not list and
// watch. It is a real API status so callers treat it like a denial by the
// API server.
func deniedError(gvr schema.GroupVersionResource, namespace, name string) error {
	where := "in all namespaces"
	if namespace != "" {
		where = "in namespace " + namespace
	}
	return apierrors.NewForbidden(gvr.GroupResource(), name, fmt.Errorf("the user may not list and watch %s %s", gvr.Resource, where))
}

func (s *scopedReader) ListByGVR(ctx context.Context, gvr schema.GroupVersionResource, namespace string) (*unstructured.UnstructuredList, error) {
	sc := s.scopeFor(gvr, namespace)
	if sc.scope == scopeDenied || sc.reader == nil {
		return nil, deniedError(gvr, namespace, "")
	}
	return sc.reader.ListByGVR(ctx, gvr, namespace)
}

func (s *scopedReader) GetByGVR(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	sc := s.scopeFor(gvr, namespace)
	if sc.scope == scopeDenied || sc.reader == nil {
		return nil, deniedError(gvr, namespace, name)
	}
	return sc.reader.GetByGVR(ctx, gvr, namespace, name)
}

// Close gives up the namespace caches. Later reads are denied.
func (s *scopedReader) Close() {
	s.mu.Lock()
	releases := s.releases
	s.releases = nil
	s.closed = true
	s.mu.Unlock()
	for _, r := range releases {
		r()
	}
}
