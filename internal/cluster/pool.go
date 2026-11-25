package cluster

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/pkg/kubeconfig"
)

// Key identifies a controller-runtime Cluster by kubeconfig path and context name.
type Key struct {
	// KubeconfigPath is the kubeconfig to load: one file, or a $KUBECONFIG
	// list that is merged as kubectl merges it (kubeconfig.Source).
	KubeconfigPath       string
	ContextName          string
	Namespace            string
	SelectorKey          string
	ImpersonateUser      string
	ImpersonateUID       string
	ImpersonateGroupsKey string
}

type entry struct {
	key    Key
	cl     *Cluster
	cancel context.CancelFunc
	// refs counts the leases that hold the entry. An entry with refs > 0 is
	// never evicted or pruned.
	refs int
	// lastUsed is when the entry was created or its last lease was released.
	// The TTL counts from there.
	lastUsed time.Time
}

// Pool manages controller-runtime clusters per kubeconfig+context. Callers
// hold an entry with a Lease from Acquire; an entry nobody holds is stopped
// once it has been unheld for the TTL.
type Pool struct {
	mu      sync.Mutex
	ttl     time.Duration
	refresh time.Duration
	closing chan struct{}
	started bool
	items   map[Key]*entry
	// now and start are replaced by tests.
	now   func() time.Time
	start func(ctx context.Context, k Key, selectors map[schema.GroupVersionResource]cache.ByObject) (*Cluster, context.CancelFunc, error)
}

func NewPool(ttl, refresh time.Duration) *Pool {
	p := &Pool{ttl: ttl, refresh: refresh, closing: make(chan struct{}), items: make(map[Key]*entry), now: time.Now}
	p.start = p.startCluster
	return p
}

// Lease is one owner's hold on a pool entry. The entry keeps running while
// any lease on it is unreleased. A Lease is not reusable: after Release the
// caller must acquire a new one.
type Lease struct {
	pool *Pool
	e    *entry
	once sync.Once
}

// Cluster returns the leased cluster.
func (l *Lease) Cluster() *Cluster {
	if l == nil {
		return nil
	}
	return l.e.cl
}

// Key returns the key of the leased entry.
func (l *Lease) Key() Key {
	if l == nil {
		return Key{}
	}
	return l.e.key
}

// Release gives up the hold. It is safe to call more than once and on a nil
// Lease. When the last lease is released the entry's TTL starts.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() { l.pool.release(l.e) })
}

func (p *Pool) Start() {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()
	go p.evictLoop()
}

func (p *Pool) Stop() {
	close(p.closing)
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.items {
		e.stop()
		delete(p.items, k)
	}
}

// Acquire returns a lease on a running Cluster for the key, starting the
// Cluster if the pool has none. selectors may be nil; when set they scope the
// Cluster's cache and, if k.SelectorKey is empty, derive it. The caller must
// Release the lease when it stops using the Cluster. ctx bounds the life of a
// newly started Cluster, so pass a long-lived context.
func (p *Pool) Acquire(ctx context.Context, k Key, selectors map[schema.GroupVersionResource]cache.ByObject) (*Lease, error) {
	if len(selectors) > 0 && strings.TrimSpace(k.SelectorKey) == "" {
		k.SelectorKey = SelectorKeyForScopes(selectors)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[k]
	if !ok {
		cl, cancel, err := p.start(ctx, k, selectors)
		if err != nil {
			return nil, err
		}
		e = &entry{key: k, cl: cl, cancel: cancel, lastUsed: p.now()}
		p.items[k] = e
	}
	e.refs++
	return &Lease{pool: p, e: e}, nil
}

func (p *Pool) release(e *entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.refs > 0 {
		e.refs--
	}
	if e.refs == 0 {
		e.lastUsed = p.now()
	}
}

// startCluster builds the Cluster for k and starts it in the background.
func (p *Pool) startCluster(ctx context.Context, k Key, selectors map[schema.GroupVersionResource]cache.ByObject) (*Cluster, context.CancelFunc, error) {
	ctrllog.FromContext(ctx).WithName("pool").Info("starting cluster cache", "context", k.ContextName, "namespace", k.Namespace, "selectorKey", k.SelectorKey)
	override := clientcmd.ConfigOverrides{CurrentContext: k.ContextName}
	if strings.TrimSpace(k.ImpersonateUser) != "" {
		override.AuthInfo.Impersonate = strings.TrimSpace(k.ImpersonateUser)
	}
	if strings.TrimSpace(k.ImpersonateUID) != "" {
		override.AuthInfo.ImpersonateUID = strings.TrimSpace(k.ImpersonateUID)
	}
	if g := strings.TrimSpace(k.ImpersonateGroupsKey); g != "" {
		override.AuthInfo.ImpersonateGroups = strings.Split(g, ",")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		kubeconfig.LoadingRules(k.KubeconfigPath),
		&override,
	).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("client config: %w", err)
	}
	opts := make([]Option, 0)
	if p.refresh > 0 {
		opts = append(opts, WithRefreshInterval(p.refresh))
	}
	if ns := strings.TrimSpace(k.Namespace); ns != "" {
		opts = append(opts, WithNamespaceScope(ns))
	}
	if len(selectors) > 0 {
		opts = append(opts, WithSelectorScope(selectors))
	}
	cl, err := New(cfg, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("cluster: %w", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	go func() { _ = cl.Start(cctx) }()
	return cl, cancel, nil
}

func (p *Pool) evictLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.closing:
			return
		case <-t.C:
			p.evictIdle()
		}
	}
}

// evictIdle stops entries that no lease holds and that have been unheld for
// longer than the TTL.
func (p *Pool) evictIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := p.now().Add(-p.ttl)
	for k, e := range p.items {
		if e.refs == 0 && e.lastUsed.Before(cutoff) {
			e.stop()
			delete(p.items, k)
		}
	}
}

// Leases is the number of unreleased leases on k's entry, 0 when the pool
// has no entry for k.
func (p *Pool) Leases(k Key) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.items[k]; ok {
		return e.refs
	}
	return 0
}

// PruneNamespaces keeps cluster-scoped entries and entries for the provided namespace,
// cancelling and removing all other namespace-scoped clusters. An empty namespace prunes
// all namespace-scoped entries. Entries held by a lease are kept; they are
// evicted by the TTL after their last lease is released.
func (p *Pool) PruneNamespaces(namespace string) {
	ns := strings.TrimSpace(namespace)
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.items {
		if k.Namespace == "" || e.refs > 0 {
			continue
		}
		if ns == "" || k.Namespace != ns {
			e.stop()
			delete(p.items, k)
		}
	}
}

// stop cancels the entry's informers and its discovery refresh loop.
func (e *entry) stop() {
	if e.cancel != nil {
		e.cancel()
	}
	if e.cl != nil {
		e.cl.Stop()
	}
}
