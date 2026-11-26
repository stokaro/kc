package models

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// overviewAccess answers the access reviews behind the overview's cache
// choice. A *kccluster.Cluster fits; its answers are cached per identity.
type overviewAccess interface {
	CanListWatchAllNamespaces(ctx context.Context, gvr schema.GroupVersionResource) (bool, error)
	CanListWatchInNamespace(ctx context.Context, gvr schema.GroupVersionResource, namespace string) (bool, error)
}

// chooseOverviewScope decides which cache serves one overview kind
// (docs/adr/0182-overview-with-namespace-access.md). all answers "may the
// user list and watch the kind in all namespaces?", inNamespace the same
// question for namespace; inNamespace is only asked after all was denied.
//
// It differs from chooseListScope in one case: an unknown answer for all
// namespaces keeps the cluster-wide cache even in a namespace. Capacity
// needs every pod, and a failed review must not turn an administrator's
// overview into a namespace-only one.
func chooseOverviewScope(namespace string, namespaced bool, all, inNamespace access) listScope {
	switch {
	case all != accessDenied:
		return scopeShared
	case !namespaced || namespace == AllNamespaces || inNamespace == accessDenied:
		return scopeDenied
	default:
		return scopeNamespace
	}
}

// overviewScopes decides the cache of every overview kind for an overview
// of namespace.
func overviewScopes(ctx context.Context, a overviewAccess, namespace string) map[schema.GroupVersionResource]listScope {
	log := ctrllog.FromContext(ctx).WithName("overview").WithValues("namespace", namespace)
	out := make(map[schema.GroupVersionResource]listScope, len(overviewKinds))
	for _, k := range overviewKinds {
		ok, err := a.CanListWatchAllNamespaces(ctx, k.gvr)
		if err != nil {
			log.Info("access review failed; treating as unknown", "gvr", k.gvr.String(), "error", err.Error())
		}
		all, inNamespace := accessOf(ok, err), accessUnknown
		if all == accessDenied && k.namespaced && namespace != AllNamespaces {
			ok, err := a.CanListWatchInNamespace(ctx, k.gvr, namespace)
			if err != nil {
				log.Info("access review failed; treating as unknown", "gvr", k.gvr.String(), "error", err.Error())
			}
			inNamespace = accessOf(ok, err)
		}
		out[k.gvr] = chooseOverviewScope(namespace, k.namespaced, all, inNamespace)
		if out[k.gvr] != scopeShared {
			log.V(1).Info("overview scope decided", "gvr", k.gvr.String(), "scope", out[k.gvr].String())
		}
	}
	return out
}

// The reasons a kind is not read, or not read everywhere. The Info preview
// of a finding matches them to suggest a permission check.
const (
	overviewDenied            = "not permitted to list and watch"
	overviewDeniedAll         = overviewDenied + " in all namespaces"
	overviewDeniedInNamespace = overviewDeniedAll + " or in namespace "
	overviewOnlyIn            = "readable only in namespace "
)

// overviewDeniedReason says why a kind is not read at all.
func overviewDeniedReason(k overviewKind, namespace string) string {
	switch {
	case !k.namespaced:
		return overviewDenied
	case namespace == AllNamespaces:
		return overviewDeniedAll
	default:
		return overviewDeniedInNamespace + namespace
	}
}

// overviewNamespaceKey is the pool key of the overview's cache limited to
// namespace: the overview's own entry, scoped as in ADR 0056.
func overviewNamespaceKey(k kccluster.Key, namespace string) kccluster.Key {
	k = overviewClusterKey(k)
	k.Namespace = namespace
	return k
}

// scopedOverviewConn reads each overview kind from the cache its scope
// decision names: the cluster-wide overview cache, a cache limited to the
// overview's namespace, or none. A cache no kind needs is not acquired, so
// a namespace-only user starts no cluster-wide informer.
type scopedOverviewConn struct {
	namespace string
	scopes    map[schema.GroupVersionResource]listScope
	parts     []*clusterOverviewConn
	byGVR     map[schema.GroupVersionResource]*clusterOverviewConn
	denied    map[schema.GroupVersionResource]string
}

// newScopedOverviewConn acquires the caches that scopes needs. acquire
// leases one pool entry. On an error every lease taken so far is given up.
func newScopedOverviewConn(namespace string, scopes map[schema.GroupVersionResource]listScope, acquire func(listScope) (*kccluster.Cluster, func(), error)) (*scopedOverviewConn, error) {
	c := &scopedOverviewConn{
		namespace: namespace,
		scopes:    scopes,
		byGVR:     make(map[schema.GroupVersionResource]*clusterOverviewConn),
		denied:    make(map[schema.GroupVersionResource]string),
	}
	groups := make(map[listScope][]overviewKind)
	for _, k := range overviewKinds {
		s := scopes[k.gvr]
		if s == scopeDenied {
			c.denied[k.gvr] = overviewDeniedReason(k, namespace)
			continue
		}
		groups[s] = append(groups[s], k)
	}
	for _, s := range []listScope{scopeShared, scopeNamespace} {
		kinds := groups[s]
		if len(kinds) == 0 {
			continue
		}
		cl, release, err := acquire(s)
		if err != nil {
			c.releaseAll()
			return nil, err
		}
		part := newClusterOverviewConn(cl, release, kinds)
		c.parts = append(c.parts, part)
		for _, k := range kinds {
			c.byGVR[k.gvr] = part
		}
	}
	return c, nil
}

func (c *scopedOverviewConn) releaseAll() {
	for _, p := range c.parts {
		p.giveUp()
	}
}

func (c *scopedOverviewConn) Reader(gvr schema.GroupVersionResource) crclient.Reader {
	if p := c.byGVR[gvr]; p != nil {
		return p.Reader(gvr)
	}
	return nil
}

func (c *scopedOverviewConn) Watch(ctx context.Context, onEvent func()) func() {
	stops := make([]func(), 0, len(c.parts))
	for _, p := range c.parts {
		stops = append(stops, p.Watch(ctx, onEvent))
	}
	return func() {
		for _, stop := range stops {
			stop()
		}
	}
}

func (c *scopedOverviewConn) Unavailable(gvr schema.GroupVersionResource) (string, bool) {
	if reason, ok := c.denied[gvr]; ok {
		return reason, false
	}
	if p := c.byGVR[gvr]; p != nil {
		return p.Unavailable(gvr)
	}
	return "not watched", false
}

func (c *scopedOverviewConn) OnlyNamespace(gvr schema.GroupVersionResource) string {
	if c.scopes[gvr] == scopeNamespace {
		return c.namespace
	}
	return ""
}

// KeepAlive does nothing: the leases keep the pool entries alive until
// Watch's stop gives them up.
func (c *scopedOverviewConn) KeepAlive(context.Context) {}

// Rediscover refreshes the discovery of every cache the overview reads.
// The scope decisions stay: a kind denied when the overview started stays
// denied (ADR 0182).
func (c *scopedOverviewConn) Rediscover() {
	for _, p := range c.parts {
		p.Rediscover()
	}
}

// errNoCluster is returned when the overview has no cluster to read.
var errNoCluster = errors.New("no cluster")

// acquireScopedOverviewConn decides the scope of every kind with access
// reviews and leases the caches the decision needs.
func acquireScopedOverviewConn(ctx context.Context, deps Deps, namespace string) (overviewConn, error) {
	scopes := make(map[schema.GroupVersionResource]listScope, len(overviewKinds))
	if deps.Cl != nil {
		rctx, cancel := context.WithTimeout(ctx, listScopeTimeout)
		scopes = overviewScopes(rctx, deps.Cl, namespace)
		cancel()
	}
	// The pool ties a cache's life to this context: use the app's.
	conn, err := newScopedOverviewConn(namespace, scopes, func(s listScope) (*kccluster.Cluster, func(), error) {
		key := overviewClusterKey(deps.ClusterKey)
		if s == scopeNamespace {
			key = overviewNamespaceKey(deps.ClusterKey, namespace)
		}
		cl, release, err := deps.NamespaceFactory(deps.Ctx, key, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("overview cache (%s scope): %w", s, err)
		}
		return cl, release, nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}
