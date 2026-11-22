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
	{Version: "v1", Resource: "pods"},
}

// infoNotPermitted is the Info text for a user who may not list nodes or
// pods in all namespaces. The widget prefixes it with "Overview
// unavailable: ".
const infoNotPermitted = "not permitted to list and watch nodes and pods in all namespaces"

// accessCheck answers whether the current user may list and watch gvr in
// all namespaces. A Cluster's CanListWatchAllNamespaces fits.
type accessCheck func(ctx context.Context, gvr schema.GroupVersionResource) (bool, error)

// gatedInfo starts the Info engine only after SelfSubjectAccessReviews say
// that the user may list and watch nodes and pods in all namespaces. The
// engine's informers are cluster-wide; for a user with namespace-scoped
// RBAC they never sync and their reflectors retry forbidden lists for as
// long as the panel shows Info, so the engine is not started at all and
// the panel says why. An access review that fails counts as allowed, as in
// ADR 0056: only a clear denial stops the engine.
type gatedInfo struct {
	open   func() infowidget.Summarizer
	cancel context.CancelFunc

	mu      sync.Mutex
	decided bool
	allowed bool
	inner   infowidget.Summarizer
	closed  bool
}

// newGatedInfo starts the access reviews in the background and returns at
// once; Summary reports Loading until they are answered.
func newGatedInfo(parent context.Context, check accessCheck, open func() infowidget.Summarizer) *gatedInfo {
	ctx, cancel := context.WithTimeout(parent, infoAccessTimeout)
	g := &gatedInfo{open: open, cancel: cancel}
	go func() {
		defer cancel()
		allowed := infoPermitted(ctx, check)
		g.mu.Lock()
		g.decided, g.allowed = true, allowed
		g.mu.Unlock()
	}()
	return g
}

func infoPermitted(ctx context.Context, check accessCheck) bool {
	log := ctrllog.FromContext(ctx).WithName("info")
	for _, gvr := range infoNeedsResources {
		ok, err := check(ctx, gvr)
		if err != nil {
			log.Info("access review failed; starting Info anyway", "gvr", gvr.String(), "error", err.Error())
			continue
		}
		if !ok {
			log.Info("Info not started: list and watch in all namespaces is forbidden", "gvr", gvr.String())
			return false
		}
	}
	return true
}

// Summary serves the engine's summary once access is known to be allowed.
func (g *gatedInfo) Summary(focus models.InfoFocus) models.InfoSummary {
	g.mu.Lock()
	switch {
	case !g.decided:
		g.mu.Unlock()
		return models.InfoSummary{Loading: true}
	case !g.allowed:
		g.mu.Unlock()
		return models.InfoSummary{Err: infoNotPermitted}
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
