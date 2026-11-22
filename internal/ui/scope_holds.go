package ui

import (
	"sync"

	kccluster "github.com/sttts/kc/internal/cluster"
)

// scopePinner is the part of the cluster pool that scopeHolds needs.
type scopePinner interface {
	Pin(kccluster.Key)
	Unpin(kccluster.Key)
}

// scopeHolds counts the folders that read a namespace-scoped pool entry and
// keeps the entry pinned while any of them is open. Pool.Pin is one flag per
// key, so two panels in one namespace would otherwise unpin each other.
//
// Follow-up after #85: Pool.Acquire returns a refcounted Lease and the
// NamespaceFactory returns its release; this type and Deps.HoldScope go away.
type scopeHolds struct {
	mu     sync.Mutex
	counts map[kccluster.Key]int
}

// hold pins key on pool and returns its release, which is safe to call more
// than once. The last release of a key unpins it; the pool's TTL then
// evicts it.
func (h *scopeHolds) hold(pool scopePinner, key kccluster.Key) func() {
	if pool == nil {
		return func() {}
	}
	h.mu.Lock()
	if h.counts == nil {
		h.counts = map[kccluster.Key]int{}
	}
	h.counts[key]++
	pool.Pin(key)
	h.mu.Unlock()
	return sync.OnceFunc(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.counts[key]--
		if h.counts[key] > 0 {
			return
		}
		delete(h.counts, key)
		pool.Unpin(key)
	})
}

// holdScope is the Deps.HoldScope of the app's folders.
func (a *App) holdScope(key kccluster.Key) func() {
	if a.clPool == nil {
		return func() {}
	}
	return a.namespaceScopes.holds.hold(a.clPool, key)
}
