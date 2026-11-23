package models

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

const (
	// overviewInterval is the shortest time between two recomputes.
	overviewInterval = time.Second
	// overviewRefreshEvery recomputes without events, so windows (events in
	// the last hour) and ages move on.
	overviewRefreshEvery = 30 * time.Second
	// overviewSyncGrace is how long a kind may stay unsynced before its rows
	// say so for good and stop forcing recomputes.
	overviewSyncGrace = 30 * time.Second
	// overviewRetryAfter spaces attempts to acquire the cluster after a failure.
	overviewRetryAfter = 5 * time.Second
	// overviewComputeTimeout bounds one recompute.
	overviewComputeTimeout = 10 * time.Second
	// overviewSelectorKey names the overview's own pool entry: a cluster-wide
	// cache that the pool's TTL stops once the overview is left.
	overviewSelectorKey = "kc-overview"
)

// overviewConn is the overview's handle on a running cache.
type overviewConn interface {
	// Reader lists objects from the cache.
	Reader() crclient.Reader
	// Watch calls onEvent on every change of an overview kind and returns a
	// function that removes the handlers.
	Watch(ctx context.Context, onEvent func()) (stop func())
	// Unavailable returns why gvr cannot be read now, or "". pending is true
	// while the reason may still go away by itself (the cache is syncing).
	Unavailable(gvr schema.GroupVersionResource) (reason string, pending bool)
	// KeepAlive renews the cache's pool entry.
	KeepAlive(ctx context.Context)
}

// overviewEngine recomputes the overview in the background, at most once per
// interval and only after informer events, and serves the latest report.
// It runs while some folder read it within idleAfter, then stops and drops
// its informer handlers; the pool stops the cache one TTL later.
type overviewEngine struct {
	deps      Deps
	namespace string
	cfg       appconfig.OverviewConfig
	connect   func(ctx context.Context) (overviewConn, error)
	// measure reads the measured usage; it runs on every tick, which also
	// keeps the metrics pollers alive while the engine runs.
	measure   func() *overviewMeasured
	interval  time.Duration
	refresh   time.Duration
	idleAfter time.Duration
	keepEvery time.Duration
	now       func() time.Time

	dirty atomic.Bool

	mu          sync.Mutex
	running     bool
	closed      bool
	stop        chan struct{}
	lastRead    time.Time
	lastFailure time.Time
	report      *overviewReport
	err         error
	computes    int // recomputes so far; tests read it
	listeners   map[int]func()
	nextID      int

	// Kubelet stats: read every statsEvery for the nodes of the last
	// recompute, in the background.
	statsFetch func() (statsFetcher, error)
	statsEvery time.Duration
	statsBusy  bool
	// statsDenied stops the stats rounds after one where every node said
	// forbidden: the identity may not use nodes/proxy, and asking each
	// node again every minute only adds 403s.
	statsDenied bool
	statsLast   time.Time
	statsRes    *statsResult
	lastNodes   []string
	lastMeasure *overviewMeasured
}

// newOverviewEngine returns an engine for deps' cluster in namespace.
func newOverviewEngine(deps Deps, namespace string) *overviewEngine {
	cfg := appconfig.DefaultOverview()
	if deps.AppConfig != nil {
		cfg = deps.AppConfig.Overview.Normalized()
	}
	ttl := watchDuration(deps)
	e := &overviewEngine{
		deps:      deps,
		namespace: namespace,
		cfg:       cfg,
		interval:  overviewInterval,
		refresh:   overviewRefreshEvery,
		idleAfter: ttl,
		keepEvery: ttl / 3,
		now:       time.Now,
		listeners: make(map[int]func()),
	}
	e.connect = func(ctx context.Context) (overviewConn, error) { return acquireOverviewConn(ctx, deps) }
	e.measure = func() *overviewMeasured {
		return measuredUsage(deps, namespace, e, func() { e.dirty.Store(true) })
	}
	e.statsEvery = overviewStatsEvery
	var once sync.Once
	var fetch statsFetcher
	var fetchErr error
	e.statsFetch = func() (statsFetcher, error) {
		once.Do(func() {
			if deps.Cl == nil {
				return
			}
			fetch, fetchErr = newKubeletStatsFetcher(deps.Cl.GetConfig())
		})
		return fetch, fetchErr
	}
	return e
}

// snapshot returns the latest report (nil before the first recompute) and
// the error that keeps the engine from running. It counts as a read: the
// engine starts when it is not running.
func (e *overviewEngine) snapshot() (*overviewReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	e.lastRead = now
	if !e.running && !e.closed && (e.lastFailure.IsZero() || now.Sub(e.lastFailure) >= overviewRetryAfter) {
		e.running = true
		e.stop = make(chan struct{})
		e.dirty.Store(true)
		go e.run(e.stop)
	}
	return e.report, e.err
}

// subscribe calls fn after every recompute until the returned cancel.
func (e *overviewEngine) subscribe(fn func()) func() {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.nextID
	e.nextID++
	e.listeners[id] = fn
	return func() {
		e.mu.Lock()
		delete(e.listeners, id)
		e.mu.Unlock()
	}
}

// close stops the engine for good.
func (e *overviewEngine) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	if e.running {
		close(e.stop)
	}
}

func (e *overviewEngine) isRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *overviewEngine) computeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.computes
}

func (e *overviewEngine) notify() {
	e.mu.Lock()
	fns := make([]func(), 0, len(e.listeners))
	for _, fn := range e.listeners {
		fns = append(fns, fn)
	}
	e.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (e *overviewEngine) run(stop <-chan struct{}) {
	ctx := e.deps.Ctx
	log := ctrllog.FromContext(ctx).WithName("overview").WithValues("context", e.deps.CtxName, "namespace", e.namespace)
	conn, err := e.connect(ctx)
	if err != nil {
		log.Error(err, "overview cluster unavailable")
		e.mu.Lock()
		e.running = false
		e.err = err
		e.lastFailure = e.now()
		e.mu.Unlock()
		e.notify()
		return
	}
	log.Info("overview started")
	stopWatch := conn.Watch(ctx, func() { e.dirty.Store(true) })
	defer func() {
		stopWatch()
		log.Info("overview stopped")
	}()

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	lastKeep := e.now()
	var lastCompute time.Time
	for {
		e.refreshMeasured(ctx)
		if e.now().Sub(lastCompute) >= e.refresh {
			e.dirty.Store(true)
		}
		if !e.deps.Activity.Paused() && e.dirty.Swap(false) {
			pending := e.recompute(ctx, conn)
			lastCompute = e.now()
			if pending {
				e.dirty.Store(true)
			}
			e.notify()
		}
		select {
		case <-ctx.Done():
			e.markStopped()
			return
		case <-stop:
			e.markStopped()
			return
		case <-ticker.C:
		}
		e.mu.Lock()
		idle := e.now().Sub(e.lastRead) > e.idleAfter
		if idle || e.closed {
			e.running = false
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
		if e.keepEvery > 0 && e.now().Sub(lastKeep) >= e.keepEvery {
			conn.KeepAlive(ctx)
			lastKeep = e.now()
		}
	}
}

func (e *overviewEngine) markStopped() {
	e.mu.Lock()
	e.running = false
	e.mu.Unlock()
}

// recompute reads the cache and stores a new report. It reports whether a
// kind is still syncing, so the next tick computes again.
func (e *overviewEngine) recompute(ctx context.Context, conn overviewConn) bool {
	ctx, cancel := context.WithTimeout(ctx, overviewComputeTimeout)
	defer cancel()
	pending := false
	in := loadOverviewInput(ctx, conn.Reader(), e.namespace, func(gvr schema.GroupVersionResource) string {
		reason, p := conn.Unavailable(gvr)
		pending = pending || p
		return reason
	})
	e.mu.Lock()
	m := e.measuredLocked()
	e.mu.Unlock()
	report := computeOverview(in, e.cfg, e.now(), m)
	e.mu.Lock()
	e.lastNodes = nodeNames(in.nodes)
	e.report = report
	e.err = nil
	e.computes++
	e.mu.Unlock()
	return pending
}

// refreshMeasured reads the measured usage and starts a stats round when one
// is due. A new value marks the engine dirty through the pollers' notify.
func (e *overviewEngine) refreshMeasured(ctx context.Context) {
	var m *overviewMeasured
	if e.measure != nil {
		m = e.measure()
	}
	e.mu.Lock()
	e.lastMeasure = m
	due := !e.statsBusy && !e.statsDenied && e.statsFetch != nil && len(e.lastNodes) > 0 &&
		(e.statsLast.IsZero() || e.now().Sub(e.statsLast) >= e.statsEvery)
	if due && e.deps.Activity.Paused() {
		due = false
	}
	var nodes []string
	if due {
		e.statsBusy = true
		e.statsLast = e.now()
		nodes = append(nodes, e.lastNodes...)
	}
	e.mu.Unlock()
	if due {
		go e.pollStats(ctx, nodes)
	}
}

// pollStats reads the kubelet stats of nodes and marks the engine dirty.
func (e *overviewEngine) pollStats(ctx context.Context, nodes []string) {
	fetch, err := e.statsFetch()
	var res statsResult
	if err != nil {
		res = statsResult{reason: "kubelet stats: " + err.Error()}
	} else {
		res = pollNodeStats(ctx, fetch, nodes, e.cfg.StatsMaxNodes)
	}
	e.mu.Lock()
	e.statsRes = &res
	e.statsDenied = res.denied
	e.statsBusy = false
	e.mu.Unlock()
	e.dirty.Store(true)
}

// measuredLocked joins the latest usage and stats into what one recompute
// uses. e.mu must be held.
func (e *overviewEngine) measuredLocked() *overviewMeasured {
	m := &overviewMeasured{nodesReason: "not measured", podsReason: "not measured", clusterPodsReason: "not measured"}
	if e.lastMeasure != nil {
		cp := *e.lastMeasure
		m = &cp
	}
	switch {
	case e.statsRes != nil:
		m.stats, m.statsErrors, m.statsReason = e.statsRes.stats, e.statsRes.errors, e.statsRes.reason
	case e.statsFetch == nil:
		m.statsReason = "not measured"
	default:
		m.statsReason = "loading"
	}
	return m
}

// overviewClusterKey is the pool key of the overview's own cache.
func overviewClusterKey(k kccluster.Key) kccluster.Key {
	k.Namespace = ""
	k.SelectorKey = overviewSelectorKey
	return k
}

// acquireOverviewConn leases the overview's cluster from the pool. Without
// a factory (tests, examples) it uses deps.Cl. The lease is given up when
// the engine stops watching (the stop function Watch returns); the pool then
// stops the cache one TTL later.
func acquireOverviewConn(_ context.Context, deps Deps) (overviewConn, error) {
	if deps.NamespaceFactory == nil {
		if deps.Cl == nil {
			return nil, fmt.Errorf("no cluster")
		}
		return newClusterOverviewConn(deps.Cl, noRelease), nil
	}
	// The pool ties the cache's life to this context: use the app's.
	cl, release, err := deps.NamespaceFactory(deps.Ctx, overviewClusterKey(deps.ClusterKey), nil)
	if err != nil {
		return nil, err
	}
	return newClusterOverviewConn(cl, release), nil
}

func newClusterOverviewConn(cl *kccluster.Cluster, release func()) *clusterOverviewConn {
	return &clusterOverviewConn{cl: cl, release: release, informers: make(map[schema.GroupVersionResource]crcache.Informer), failed: make(map[schema.GroupVersionResource]string)}
}

// clusterOverviewConn reads the overview kinds from a kc cluster's cache.
type clusterOverviewConn struct {
	cl      *kccluster.Cluster
	release func() // gives up the pool lease; called once by Watch's stop

	mu        sync.Mutex
	started   time.Time
	informers map[schema.GroupVersionResource]crcache.Informer
	failed    map[schema.GroupVersionResource]string
}

func (c *clusterOverviewConn) Reader() crclient.Reader { return c.cl.GetCache() }

func (c *clusterOverviewConn) Watch(ctx context.Context, onEvent func()) func() {
	log := ctrllog.FromContext(ctx).WithName("overview")
	handler := toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { onEvent() },
		UpdateFunc: func(any, any) { onEvent() },
		DeleteFunc: func(any) { onEvent() },
	}
	var regs []func()
	c.mu.Lock()
	c.started = time.Now()
	c.mu.Unlock()
	for _, k := range overviewKinds {
		getCtx, cancel := context.WithTimeout(ctx, informerStartTimeout)
		// Do not wait for the sync: rows of a kind that cannot be listed
		// say so instead of blocking the overview.
		inf, err := c.cl.GetCache().GetInformer(getCtx, k.newObject(), crcache.BlockUntilSynced(false))
		cancel()
		if err != nil {
			reason := err.Error()
			if meta.IsNoMatchError(err) {
				reason = notServedReason
			}
			log.Info("overview kind unavailable", "gvr", k.gvr.String(), "reason", reason)
			c.mu.Lock()
			c.failed[k.gvr] = reason
			c.mu.Unlock()
			continue
		}
		reg, err := inf.AddEventHandler(handler)
		if err != nil {
			c.mu.Lock()
			c.failed[k.gvr] = err.Error()
			c.mu.Unlock()
			continue
		}
		c.mu.Lock()
		c.informers[k.gvr] = inf
		c.mu.Unlock()
		regs = append(regs, func() { _ = inf.RemoveEventHandler(reg) })
	}
	return func() {
		for _, r := range regs {
			r()
		}
		c.mu.Lock()
		release := c.release
		c.release = nil
		c.mu.Unlock()
		if release != nil {
			release()
		}
	}
}

func (c *clusterOverviewConn) Unavailable(gvr schema.GroupVersionResource) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason, ok := c.failed[gvr]; ok {
		return reason, false
	}
	inf, ok := c.informers[gvr]
	if !ok {
		return "not watched", false
	}
	if inf.HasSynced() {
		return "", false
	}
	if time.Since(c.started) < overviewSyncGrace {
		return "loading", true
	}
	return "the cache did not sync (no list or watch permission?)", false
}

// KeepAlive does nothing: the lease taken in acquireOverviewConn keeps the
// pool entry alive until Watch's stop gives it up.
func (c *clusterOverviewConn) KeepAlive(context.Context) {}
