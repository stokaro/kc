package models

import (
	"context"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// metrics.k8s.io resources read by the usage poller.
var (
	podMetricsGVR  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	nodeMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
)

const (
	// usagePollInterval matches metrics-server's default resolution: polling
	// faster returns the same numbers.
	usagePollInterval = 15 * time.Second
	// UsagePollInterval is usagePollInterval for the Info history, which
	// spaces its samples by it.
	UsagePollInterval = usagePollInterval
	// usagePollTimeout bounds one poll: the metrics LIST and the cache read.
	usagePollTimeout = 10 * time.Second
	// usageForbiddenInterval spaces polls that the API server answered
	// with forbidden: the grant rarely changes within minutes, and a 403
	// every 15 seconds only adds load and audit noise.
	usageForbiddenInterval = 5 * time.Minute
)

// usageSnapshot is the result of one poll.
type usageSnapshot struct {
	at     time.Time
	served bool  // metrics.k8s.io serves the resource
	err    error // the metrics could not be read; cells show n/a
	values map[string]usageValues
	// containers holds the usage of each container of a pod, by pod key.
	// The overview compares it with each container's limit.
	containers map[string][]containerUsage
}

// containerUsage is the measured usage of one container: CPU in millicores,
// memory in bytes.
type containerUsage struct {
	name     string
	cpu, mem int64
}

// usageFetcher reads one snapshot. metricsOnly skips the join with the
// objects in the shared cache. Tests replace it.
type usageFetcher func(ctx context.Context, deps Deps, kind usageKind, namespace string, metricsOnly bool) *usageSnapshot

// UsageMetrics polls metrics.k8s.io for the pod and node lists on screen.
// It keeps one poller per cluster, kind and namespace. A poller runs while
// some list read its snapshot within the last two intervals, and stops
// otherwise. A nil *UsageMetrics adds no columns.
type UsageMetrics struct {
	mu       sync.Mutex
	pollers  map[usagePollerKey]*usagePoller
	interval time.Duration
	fetch    usageFetcher
	now      func() time.Time
}

// NewUsageMetrics returns a registry that reads metrics from the cluster.
func NewUsageMetrics() *UsageMetrics {
	return &UsageMetrics{interval: usagePollInterval, fetch: fetchUsage, now: time.Now}
}

type usagePollerKey struct {
	cluster   kccluster.Key
	kind      usageKind
	namespace string
	// metricsOnly pollers read metrics.k8s.io and nothing else. The overview
	// uses them: it has the pod and node specs in its own cache, and joining
	// with the panel's shared cache would start cluster-wide informers there
	// that live for the session.
	metricsOnly bool
}

type usagePoller struct {
	m   *UsageMetrics
	key usagePollerKey

	mu       sync.Mutex
	deps     Deps
	running  bool
	snap     *usageSnapshot
	watchers map[any]usageWatcher
	lastErr  string // last logged error, to log each state once
}

type usageWatcher struct {
	seen   time.Time
	notify func()
}

// idleAfter is how long a poller runs without a reader.
func (m *UsageMetrics) idleAfter() time.Duration { return 2 * m.interval }

// snapshot returns the latest snapshot for kind in namespace and records
// watcher as a reader that notify wakes after each poll. It starts the
// poller when it is not running. The result may be nil before the first
// poll; fresh is false when the snapshot is older than the idle window.
func (m *UsageMetrics) snapshot(deps Deps, kind usageKind, namespace string, watcher any, notify func()) (snap *usageSnapshot, fresh bool) {
	return m.snapshotFor(deps, usagePollerKey{cluster: deps.ClusterKey, kind: kind, namespace: namespace}, watcher, notify)
}

// metricsSnapshot is snapshot for a poller that reads metrics.k8s.io only:
// the values carry usage, and requests, limits and allocatable stay zero.
func (m *UsageMetrics) metricsSnapshot(deps Deps, kind usageKind, namespace string, watcher any, notify func()) (snap *usageSnapshot, fresh bool) {
	return m.snapshotFor(deps, usagePollerKey{cluster: deps.ClusterKey, kind: kind, namespace: namespace, metricsOnly: true}, watcher, notify)
}

func (m *UsageMetrics) snapshotFor(deps Deps, key usagePollerKey, watcher any, notify func()) (snap *usageSnapshot, fresh bool) {
	if m == nil || key.kind == 0 {
		return nil, false
	}
	m.mu.Lock()
	if m.pollers == nil {
		m.pollers = make(map[usagePollerKey]*usagePoller)
	}
	p, ok := m.pollers[key]
	if !ok {
		p = &usagePoller{m: m, key: key, watchers: make(map[any]usageWatcher)}
		m.pollers[key] = p
	}
	m.mu.Unlock()

	now := m.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deps = deps
	p.watchers[watcher] = usageWatcher{seen: now, notify: notify}
	if !p.running {
		p.running = true
		go p.run()
	}
	if p.snap == nil {
		return nil, false
	}
	return p.snap, now.Sub(p.snap.at) <= m.idleAfter()
}

// running reports whether the poller for key polls; tests use it.
func (m *UsageMetrics) running(key usagePollerKey) bool {
	m.mu.Lock()
	p := m.pollers[key]
	m.mu.Unlock()
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// run polls until no reader is left or the app context ends. It polls at
// once, then every interval, and skips polls while a dialog is open.
func (p *usagePoller) run() {
	m := p.m
	for {
		p.mu.Lock()
		deps := p.deps
		now := m.now()
		var active, idle []func()
		for w, st := range p.watchers {
			if now.Sub(st.seen) > m.idleAfter() {
				idle = append(idle, st.notify)
				delete(p.watchers, w)
				continue
			}
			active = append(active, st.notify)
		}
		if len(active) == 0 {
			// Nobody read the snapshot for a while. Wake the readers once more
			// so a list that is still on screen (behind a dialog that just
			// closed, say) reads again and restarts the poller; lists that are
			// not on screen only get a dirty flag.
			p.running = false
			p.mu.Unlock()
			notifyAll(idle)
			return
		}
		p.mu.Unlock()
		// Readers that went away while others stay get the same last wake-up.
		notifyAll(idle)

		wait := m.interval
		if !deps.Activity.Paused() {
			ctx, cancel := context.WithTimeout(deps.Ctx, usagePollTimeout)
			snap := m.fetch(ctx, deps, p.key.kind, p.key.namespace, p.key.metricsOnly)
			cancel()
			if snap != nil && apierrors.IsForbidden(snap.err) {
				wait = max(wait, usageForbiddenInterval)
			}
			if snap != nil {
				snap.at = m.now()
				p.mu.Lock()
				p.snap = snap
				p.logStateLocked(deps.Ctx, snap)
				p.mu.Unlock()
				notifyAll(active)
			}
		}

		timer := time.NewTimer(wait)
		select {
		case <-deps.Ctx.Done():
			timer.Stop()
			p.mu.Lock()
			p.running = false
			p.mu.Unlock()
			return
		case <-timer.C:
		}
	}
}

func notifyAll(fns []func()) {
	for _, fn := range fns {
		if fn != nil {
			fn()
		}
	}
}

// logStateLocked logs when reading metrics starts or stops failing. Failures
// never reach the user as an error message: the cells say n/a.
func (p *usagePoller) logStateLocked(ctx context.Context, snap *usageSnapshot) {
	msg := ""
	if snap.err != nil {
		msg = snap.err.Error()
	}
	if msg == p.lastErr {
		return
	}
	p.lastErr = msg
	log := ctrllog.FromContext(ctx).WithName("usage").WithValues("kind", p.key.kind.String(), "namespace", p.key.namespace, "metricsOnly", p.key.metricsOnly)
	if msg == "" {
		log.Info("usage metrics available")
		return
	}
	log.Info("usage metrics unavailable", "error", msg)
}

// fetchUsage lists PodMetrics or NodeMetrics once and, unless metricsOnly,
// joins them with the pods or nodes in the shared cache.
func fetchUsage(ctx context.Context, deps Deps, kind usageKind, namespace string, metricsOnly bool) *usageSnapshot {
	if deps.Cl == nil {
		return nil
	}
	metricsGVR, objectGVR := podMetricsGVR, podGVR
	if kind == usageNodes {
		metricsGVR, objectGVR, namespace = nodeMetricsGVR, nodeGVR, ""
	}
	if !metricsServed(deps, metricsGVR) {
		return &usageSnapshot{}
	}
	log := ctrllog.FromContext(ctx).WithName("usage")
	log.Info("client list", "gvr", metricsGVR.String(), "namespace", namespace)
	res := deps.Cl.Dynamic().Resource(metricsGVR)
	var list *unstructured.UnstructuredList
	var err error
	if namespace != "" {
		list, err = res.Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = res.List(ctx, metav1.ListOptions{})
	}
	switch {
	case apierrors.IsNotFound(err):
		// Discovery still lists the API, but it is gone.
		return &usageSnapshot{}
	case err != nil:
		return &usageSnapshot{served: true, err: err}
	}
	var containers map[string][]containerUsage
	if kind == usagePods {
		containers = buildContainerUsage(list.Items)
	}
	if metricsOnly {
		return &usageSnapshot{served: true, values: buildUsageValues(kind, list.Items, nil), containers: containers}
	}
	// Requests, limits and allocatable come from the shared cache. Without
	// them usage still shows, and percentages say n/a.
	var objects []unstructured.Unstructured
	if ol, err := deps.Cl.ListByGVR(ctx, objectGVR, namespace); err == nil && ol != nil {
		objects = ol.Items
	} else if err != nil {
		log.V(1).Info("usage: cannot read objects from the cache", "gvr", objectGVR.String(), "error", err.Error())
	}
	return &usageSnapshot{served: true, values: buildUsageValues(kind, list.Items, objects), containers: containers}
}

// metricsServed reports whether discovery lists gvr. It sends no request
// when discovery is cached.
func metricsServed(deps Deps, gvr schema.GroupVersionResource) bool {
	mapper := deps.Cl.RESTMapper()
	if mapper == nil {
		return false
	}
	// Another discovery error is not a "no": the list below decides.
	_, err := mapper.KindFor(gvr)
	return !meta.IsNoMatchError(err)
}
