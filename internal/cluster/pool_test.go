package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

const testTTL = 2 * time.Minute

// testPool is a Pool whose clusters are never started and whose clock only
// moves when the test advances it.
type testPool struct {
	*Pool
	clock  time.Time
	starts map[Key]int
	stops  map[Key]int
}

func newTestPool(t *testing.T) *testPool {
	t.Helper()
	tp := &testPool{
		Pool:   NewPool(testTTL, time.Minute),
		clock:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		starts: make(map[Key]int),
		stops:  make(map[Key]int),
	}
	tp.now = func() time.Time { return tp.clock }
	tp.start = func(_ context.Context, k Key, _ map[schema.GroupVersionResource]cache.ByObject) (*Cluster, context.CancelFunc, error) {
		tp.starts[k]++
		return nil, func() { tp.stops[k]++ }, nil
	}
	return tp
}

func (tp *testPool) advance(d time.Duration) { tp.clock = tp.clock.Add(d) }

func (tp *testPool) has(k Key) bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	_, ok := tp.items[k]
	return ok
}

func (tp *testPool) acquire(t *testing.T, k Key) *Lease {
	t.Helper()
	l, err := tp.Acquire(t.Context(), k, nil)
	if err != nil {
		t.Fatalf("acquire %v: %v", k, err)
	}
	return l
}

var (
	mainKey = Key{KubeconfigPath: "/tmp/kubeconfig", ContextName: "ctx"}
	nodeKey = Key{KubeconfigPath: "/tmp/kubeconfig", ContextName: "ctx", SelectorKey: "pods=node-a"}
)

func TestPoolKeepsHeldEntryPastTTL(t *testing.T) {
	t.Parallel()
	tp := newTestPool(t)
	l := tp.acquire(t, nodeKey)

	tp.advance(10 * testTTL)
	tp.evictIdle()

	if !tp.has(nodeKey) || tp.stops[nodeKey] != 0 {
		t.Fatalf("a held entry was evicted after 10 TTLs: present=%v stops=%d", tp.has(nodeKey), tp.stops[nodeKey])
	}
	l.Release()
}

func TestPoolEvictsAfterLastRelease(t *testing.T) {
	t.Parallel()
	tp := newTestPool(t)
	l := tp.acquire(t, nodeKey)
	tp.advance(10 * testTTL)

	l.Release()
	// The TTL counts from the release, not from the acquire.
	tp.advance(testTTL - time.Second)
	tp.evictIdle()
	if !tp.has(nodeKey) {
		t.Fatalf("entry evicted %v after its release, before the TTL of %v", testTTL-time.Second, testTTL)
	}

	tp.advance(2 * time.Second)
	tp.evictIdle()
	if tp.has(nodeKey) || tp.stops[nodeKey] != 1 {
		t.Fatalf("unheld entry past the TTL not evicted: present=%v stops=%d", tp.has(nodeKey), tp.stops[nodeKey])
	}
}

func TestPoolSharedEntryNeedsEveryRelease(t *testing.T) {
	t.Parallel()
	tp := newTestPool(t)
	left := tp.acquire(t, nodeKey)
	right := tp.acquire(t, nodeKey)
	if tp.starts[nodeKey] != 1 {
		t.Fatalf("two acquires of one key started %d clusters, want 1", tp.starts[nodeKey])
	}
	if left.Cluster() != right.Cluster() {
		t.Fatalf("two leases on one key returned different clusters")
	}
	if n := tp.Leases(nodeKey); n != 2 {
		t.Fatalf("Leases = %d, want 2", n)
	}

	left.Release()
	left.Release() // a second release must not drop the right panel's hold
	if n := tp.Leases(nodeKey); n != 1 {
		t.Fatalf("Leases after a double release = %d, want 1", n)
	}
	tp.advance(10 * testTTL)
	tp.evictIdle()
	if !tp.has(nodeKey) {
		t.Fatalf("leaving one panel evicted the cache the other panel holds")
	}

	right.Release()
	tp.advance(testTTL + time.Second)
	tp.evictIdle()
	if tp.has(nodeKey) {
		t.Fatalf("entry still present after both leases were released and the TTL passed")
	}
}

func TestPoolReacquireAfterEvictionStartsNewCluster(t *testing.T) {
	t.Parallel()
	tp := newTestPool(t)
	stale := tp.acquire(t, nodeKey)
	stale.Release()
	tp.advance(testTTL + time.Second)
	tp.evictIdle()

	fresh := tp.acquire(t, nodeKey)
	if tp.starts[nodeKey] != 2 {
		t.Fatalf("reacquire after eviction started %d clusters in total, want 2", tp.starts[nodeKey])
	}
	// Releasing the old lease again must not touch the new entry.
	stale.Release()
	tp.advance(10 * testTTL)
	tp.evictIdle()
	if !tp.has(nodeKey) {
		t.Fatalf("a stale lease released the entry that replaced it")
	}
	fresh.Release()
}

func TestPoolNeverAcquiredEntryIsEvicted(t *testing.T) {
	t.Parallel()
	tp := newTestPool(t)
	tp.acquire(t, mainKey).Release()
	tp.advance(testTTL + time.Second)
	tp.evictIdle()
	if tp.has(mainKey) {
		t.Fatalf("an entry with no lease survived the TTL")
	}
}

func TestPoolPruneNamespacesKeepsHeldEntries(t *testing.T) {
	t.Parallel()
	tp := newTestPool(t)
	heldKey := mainKey
	heldKey.Namespace = "held"
	freeKey := mainKey
	freeKey.Namespace = "free"
	held := tp.acquire(t, heldKey)
	tp.acquire(t, freeKey).Release()
	tp.acquire(t, mainKey).Release()

	tp.PruneNamespaces("other")

	if !tp.has(heldKey) {
		t.Fatalf("prune stopped a namespace cache a lease holds")
	}
	if tp.has(freeKey) {
		t.Fatalf("prune kept an unheld cache of another namespace")
	}
	if !tp.has(mainKey) {
		t.Fatalf("prune removed the cluster-scoped entry")
	}
	held.Release()
	tp.advance(testTTL + time.Second)
	tp.evictIdle()
	if tp.has(heldKey) {
		t.Fatalf("a pruning-exempt entry was not evicted after its release")
	}
}

func TestPoolAcquireDerivesSelectorKey(t *testing.T) {
	t.Parallel()
	tp := newTestPool(t)
	selectors := map[schema.GroupVersionResource]cache.ByObject{
		{Version: "v1", Resource: "pods"}: {Field: fields.OneTermEqualSelector("spec.nodeName", "node-a")},
	}
	l, err := tp.Acquire(t.Context(), mainKey, selectors)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer l.Release()
	if got, want := l.Key().SelectorKey, SelectorKeyForScopes(selectors); got != want || got == "" {
		t.Fatalf("lease key SelectorKey = %q, want %q", got, want)
	}
	if !tp.has(l.Key()) || tp.has(mainKey) {
		t.Fatalf("entry stored under the wrong key")
	}
}

func TestPoolAcquireFailureStoresNothing(t *testing.T) {
	t.Parallel()
	tp := newTestPool(t)
	tp.start = func(context.Context, Key, map[schema.GroupVersionResource]cache.ByObject) (*Cluster, context.CancelFunc, error) {
		return nil, nil, errors.New("boom")
	}
	l, err := tp.Acquire(t.Context(), mainKey, nil)
	if err == nil || l != nil {
		t.Fatalf("Acquire = %v, %v; want nil lease and an error", l, err)
	}
	if tp.has(mainKey) {
		t.Fatalf("a failed acquire left an entry")
	}
	l.Release() // nil-safe
}
