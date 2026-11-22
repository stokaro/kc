package models

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

// listScope says which informer cache may serve a list of one resource type
// in one namespace scope. See docs/adr/0056-namespace-scoped-access.md.
type listScope int

const (
	// scopeShared uses the shared cache of the context: the user may list
	// and watch the type in all namespaces.
	scopeShared listScope = iota
	// scopeNamespace uses a cache limited to the namespace: the user may not
	// list the type cluster-wide, so a shared informer would never sync.
	scopeNamespace
	// scopeDenied means no informer can serve it: the user may not list and
	// watch the type where the folder looks.
	scopeDenied
)

func (s listScope) String() string {
	switch s {
	case scopeShared:
		return "shared"
	case scopeNamespace:
		return "namespace"
	default:
		return "denied"
	}
}

// access is the answer to one "may the user list and watch this?" review.
type access int

const (
	// accessUnknown: the review failed or was not asked. It is no denial.
	accessUnknown access = iota
	accessAllowed
	accessDenied
)

func accessOf(allowed bool, err error) access {
	switch {
	case err != nil:
		return accessUnknown
	case allowed:
		return accessAllowed
	default:
		return accessDenied
	}
}

// chooseListScope decides the cache for a list in namespace (AllNamespaces
// for all, and for cluster-scoped types). all answers "may the user list
// and watch the type in all namespaces?", inNamespace the same question for
// namespace; inNamespace is only asked when all is not allowed.
//
// An unknown answer is no denial. In a namespace the namespace cache works
// for every user who may read there, so it is the safe choice; without a
// namespace the shared cache is the only choice and the API server decides.
func chooseListScope(namespace string, all, inNamespace access) listScope {
	switch {
	case all == accessAllowed:
		return scopeShared
	case namespace == AllNamespaces && all == accessDenied:
		return scopeDenied
	case namespace == AllNamespaces:
		return scopeShared
	case inNamespace == accessDenied:
		return scopeDenied
	default:
		return scopeNamespace
	}
}

// listScopeTimeout bounds the access reviews behind one decision.
const listScopeTimeout = 10 * time.Second

// resolveListScope returns the Deps whose cluster may list and watch gvr in
// namespace, the decision, and a release for a namespace cache it acquired.
// release is never nil. The answers come from SelfSubjectAccessReviews,
// cached per cluster, so only the first call for a type does network work.
func (d Deps) resolveListScope(gvr schema.GroupVersionResource, namespace string) (Deps, listScope, func()) {
	if d.Cl == nil {
		return d, scopeShared, noRelease
	}
	ctx, cancel := context.WithTimeout(d.Ctx, listScopeTimeout)
	defer cancel()
	log := crlog.FromContext(d.Ctx).WithValues("gvr", gvr.String(), "namespace", namespace)
	ok, err := d.Cl.CanListWatchAllNamespaces(ctx, gvr)
	if err != nil {
		log.Info("access review failed; treating as unknown", "error", err.Error())
	}
	all, inNamespace := accessOf(ok, err), accessUnknown
	if all != accessAllowed && namespace != AllNamespaces {
		ok, err := d.Cl.CanListWatchInNamespace(ctx, gvr, namespace)
		if err != nil {
			log.Info("access review failed; treating as unknown", "error", err.Error())
		}
		inNamespace = accessOf(ok, err)
	}
	scope := chooseListScope(namespace, all, inNamespace)
	if scope != scopeShared {
		log.V(1).Info("list scope decided", "scope", scope.String())
	}
	if scope != scopeNamespace {
		return d, scope, noRelease
	}
	// The pool's lease keeps the namespace cluster running until release.
	scoped, release := d.ForNamespace(namespace)
	return scoped, scope, release
}
