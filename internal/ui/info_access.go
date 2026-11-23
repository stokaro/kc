package ui

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	models "github.com/stokaro/kc/internal/models"
	infowidget "github.com/stokaro/kc/internal/ui/panelcontent/info"
)

// infoAccessTimeout bounds the access reviews before the Info engine starts.
const infoAccessTimeout = 10 * time.Second

// infoNeedsResources are what the Info engine cannot work without: it sums
// the pods on every node, so it lists and watches nodes and pods across the
// cluster.
var infoNeedsResources = []schema.GroupVersionResource{
	{Version: "v1", Resource: "nodes"},
	infoPodsGVR,
}

var infoPodsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// infoNotPermitted is the Info text for a user who may not list nodes or
// pods in all namespaces, in an all-namespaces scope. The widget prefixes
// it with "Overview unavailable: ".
const infoNotPermitted = "not permitted to list and watch nodes and pods in all namespaces"

// infoNotPermittedIn is the Info text for a user who may not list pods in
// the namespace of a namespace scope either.
func infoNotPermittedIn(namespace string) string {
	return "not permitted to list and watch pods in all namespaces or in namespace " + namespace
}

// accessCheck answers whether the current user may list and watch gvr in
// all namespaces. A Cluster's CanListWatchAllNamespaces fits.
type accessCheck func(ctx context.Context, gvr schema.GroupVersionResource) (bool, error)

// namespaceCheck answers whether the current user may list and watch gvr
// in namespace. A Cluster's CanListWatchInNamespace fits.
type namespaceCheck func(ctx context.Context, gvr schema.GroupVersionResource, namespace string) (bool, error)

// gatedInfo starts the Info engine only after SelfSubjectAccessReviews say
// that the engine can read what it needs. In an all-namespaces scope that
// is nodes and pods in all namespaces: the engine's informers are then
// cluster-wide, and for a user with namespace-scoped RBAC they would never
// sync and their reflectors would retry forbidden lists for as long as the
// panel shows Info. In a namespace scope pods readable in the namespace
// are enough (docs/adr/0257-metric-sources.md): the engine then uses
// namespace caches only (ADR 0182), its workload and pod usage work, and
// its capacity bars say that they need every pod. An access review that
// fails counts as allowed, as in ADR 0056: only a clear denial stops the
// engine.
type gatedInfo struct {
	open   func() infowidget.Summarizer
	cancel context.CancelFunc

	mu      sync.Mutex
	decided bool
	denied  string // why the engine is not started, once decided
	inner   infowidget.Summarizer
	closed  bool
}

// newGatedInfo starts the access reviews in the background and returns at
// once; Summary reports Loading until they are answered. namespace is the
// scope (models.AllNamespaces for all); inNamespace may be nil for an
// all-namespaces scope.
func newGatedInfo(parent context.Context, check accessCheck, inNamespace namespaceCheck, namespace string, open func() infowidget.Summarizer) *gatedInfo {
	ctx, cancel := context.WithTimeout(parent, infoAccessTimeout)
	g := &gatedInfo{open: open, cancel: cancel}
	go func() {
		defer cancel()
		denied := infoDenied(ctx, check, inNamespace, namespace)
		g.mu.Lock()
		g.decided, g.denied = true, denied
		g.mu.Unlock()
	}()
	return g
}

// infoDenied returns why the engine may not start, or "" when it may.
func infoDenied(ctx context.Context, check accessCheck, inNamespace namespaceCheck, namespace string) string {
	log := ctrllog.FromContext(ctx).WithName("info")
	allowed := true
	for _, gvr := range infoNeedsResources {
		ok, err := check(ctx, gvr)
		if err != nil {
			log.Info("access review failed; starting Info anyway", "gvr", gvr.String(), "error", err.Error())
			continue
		}
		if !ok {
			allowed = false
			break
		}
	}
	if allowed {
		return ""
	}
	if namespace == models.AllNamespaces || inNamespace == nil {
		log.Info("Info not started: list and watch in all namespaces is forbidden")
		return infoNotPermitted
	}
	ok, err := inNamespace(ctx, infoPodsGVR, namespace)
	switch {
	case err != nil:
		log.Info("access review failed; starting Info in the namespace anyway", "namespace", namespace, "error", err.Error())
	case !ok:
		log.Info("Info not started: list and watch of pods is forbidden in the namespace", "namespace", namespace)
		return infoNotPermittedIn(namespace)
	default:
		log.Info("Info started with namespace access only", "namespace", namespace)
	}
	return ""
}

// Summary serves the engine's summary once access is known to be allowed.
func (g *gatedInfo) Summary(focus models.InfoFocus) models.InfoSummary {
	g.mu.Lock()
	switch {
	case !g.decided:
		g.mu.Unlock()
		return models.InfoSummary{Loading: true}
	case g.denied != "":
		denied := g.denied
		g.mu.Unlock()
		return models.InfoSummary{Err: denied}
	}
	if g.inner == nil && !g.closed {
		g.inner = g.open()
	}
	inner := g.inner
	g.mu.Unlock()
	if inner == nil {
		return models.InfoSummary{}
	}
	return inner.Summary(focus)
}

// Close stops the reviews and the engine.
func (g *gatedInfo) Close() {
	g.cancel()
	g.mu.Lock()
	g.closed = true
	inner := g.inner
	g.inner = nil
	g.mu.Unlock()
	if inner != nil {
		inner.Close()
	}
}
