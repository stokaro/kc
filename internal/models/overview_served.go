package models

import (
	"context"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// overviewRediscoverTimeout bounds one round of followDiscovery: the
// discovery read and the informers it starts.
const overviewRediscoverTimeout = 10 * time.Second

// startKind gets the informer of k from the cache, without waiting for its
// sync, and adds the overview's handler. It records the informer, or why
// there is none, and returns that reason ("" when k is watched).
func (c *clusterOverviewConn) startKind(ctx context.Context, k overviewKind) string {
	getCtx, cancel := context.WithTimeout(ctx, informerStartTimeout)
	defer cancel()
	// Do not wait for the sync: rows of a kind that cannot be listed say
	// so instead of blocking the overview.
	inf, err := c.cl.GetCache().GetInformer(getCtx, k.newObject(), crcache.BlockUntilSynced(false))
	if err != nil {
		reason := err.Error()
		if meta.IsNoMatchError(err) {
			reason = notServedReason
		}
		c.setFailed(k.gvr, reason)
		return reason
	}
	c.mu.Lock()
	onEvent := c.onEvent
	c.mu.Unlock()
	if onEvent == nil {
		onEvent = func() {}
	}
	reg, err := inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { onEvent() },
		UpdateFunc: func(any, any) { onEvent() },
		DeleteFunc: func(any) { onEvent() },
	})
	if err != nil {
		c.setFailed(k.gvr, err.Error())
		return err.Error()
	}
	c.mu.Lock()
	delete(c.failed, k.gvr)
	c.informers[k.gvr] = inf
	c.since[k.gvr] = time.Now()
	c.regs[k.gvr] = func() { _ = inf.RemoveEventHandler(reg) }
	c.mu.Unlock()
	return ""
}

func (c *clusterOverviewConn) setFailed(gvr schema.GroupVersionResource, reason string) {
	c.mu.Lock()
	c.failed[gvr] = reason
	c.mu.Unlock()
}

// stopKinds removes the overview's handlers; Watch's stop calls it.
func (c *clusterOverviewConn) stopKinds() {
	c.mu.Lock()
	c.onEvent = nil
	regs := c.regs
	c.regs = make(map[schema.GroupVersionResource]func())
	c.mu.Unlock()
	for _, r := range regs {
		r()
	}
}

// dropKind stops reading a kind the cluster no longer serves: the rows that
// need it say so instead of showing what its informer held. The informer
// is removed from the cache as well. Its list and watch can only fail now,
// and once the kind is served again a new informer starts from a fresh
// list. The cache is the overview's own (ADR 0182), and every overview
// engine on it reads the same discovery, so they drop the kind in the same
// refresh.
func (c *clusterOverviewConn) dropKind(ctx context.Context, k overviewKind) {
	c.mu.Lock()
	unregister := c.regs[k.gvr]
	delete(c.regs, k.gvr)
	delete(c.informers, k.gvr)
	delete(c.since, k.gvr)
	c.failed[k.gvr] = notServedReason
	c.mu.Unlock()
	if unregister != nil {
		unregister()
	}
	if err := c.cl.GetCache().RemoveInformer(ctx, k.newObject()); err != nil {
		ctrllog.FromContext(ctx).WithName("overview").V(1).Info("removing the informer of a kind that is no longer served failed", "gvr", k.gvr.String(), "error", err.Error())
	}
}

// followDiscovery runs rediscover after every discovery refresh of the
// cluster: the periodic one (kubernetes.discovery.refresh) and the one
// Rediscover asks for. The returned stop waits for a running round.
func (c *clusterOverviewConn) followDiscovery(ctx context.Context) (stop func()) {
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	finished := make(chan struct{})
	removeListener := c.cl.AddDiscoveryListener(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	go func() {
		defer close(finished)
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-wake:
				c.rediscover(ctx)
			}
		}
	}()
	return func() {
		removeListener()
		close(done)
		<-finished
	}
}

// rediscover compares the kinds with the cluster's discovery, which the
// refresh that woke it has just invalidated. A kind recorded as not served
// that discovery lists is retried; a watched kind that discovery no longer
// lists is dropped. A kind that failed for another reason (forbidden) is
// left as it is, and so is every kind when discovery cannot tell. Each
// change is logged once and leads to one recompute.
func (c *clusterOverviewConn) rediscover(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, overviewRediscoverTimeout)
	defer cancel()
	log := ctrllog.FromContext(ctx).WithName("overview")
	c.mu.Lock()
	kinds := slices.Clone(c.kinds)
	c.mu.Unlock()
	log.V(1).Info("reading API discovery for the overview kinds", "kinds", len(kinds), "namespace", c.cl.Namespace())
	changed := false
	for _, k := range kinds {
		served, err := c.cl.Serves(ctx, k.gvr)
		if err != nil {
			log.V(1).Info("discovery cannot tell whether a kind is served", "gvr", k.gvr.String(), "error", err.Error())
			continue
		}
		c.mu.Lock()
		_, watched := c.informers[k.gvr]
		notServed := c.failed[k.gvr] == notServedReason
		c.mu.Unlock()
		switch {
		case served && notServed:
			log.Info("overview kind is served now; starting its informer", "gvr", k.gvr.String())
			reason := c.startKind(ctx, k)
			if reason == notServedReason {
				continue
			}
			changed = true
			if reason != "" {
				log.Info("overview kind unavailable", "gvr", k.gvr.String(), "reason", reason)
			}
		case !served && watched:
			log.Info("overview kind is no longer served; dropping its informer", "gvr", k.gvr.String())
			c.dropKind(ctx, k)
			changed = true
		}
	}
	if !changed {
		return
	}
	c.mu.Lock()
	fn := c.onEvent
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}
