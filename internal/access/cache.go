package access

import (
	"context"
	"sync"
	"time"
)

// DefaultTTL is how long a review result is used before it is asked again.
const DefaultTTL = 30 * time.Second

// maxEntries bounds the cache; when it is exceeded, expired entries go first.
const maxEntries = 4096

// maxParallel bounds the reviews one Run sends at the same time.
const maxParallel = 8

type cacheKey struct {
	identity string
	check    Check
}

// Cache keeps review results per identity and check for a TTL and tracks
// reviews in flight so that the same question is not asked twice at once.
// It is safe for concurrent use.
type Cache struct {
	ttl time.Duration
	now func() time.Time

	mu       sync.Mutex
	entries  map[cacheKey]Result
	inflight map[cacheKey]struct{}
}

// NewCache returns a cache with the given TTL (DefaultTTL when zero) and
// clock (time.Now when nil).
func NewCache(ttl time.Duration, now func() time.Time) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Cache{
		ttl:      ttl,
		now:      now,
		entries:  make(map[cacheKey]Result),
		inflight: make(map[cacheKey]struct{}),
	}
}

// Lookup returns the cached result for c. ok is false when nothing is
// cached. An expired result is returned with Stale set.
func (k *Cache) Lookup(id Identity, c Check) (Result, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.lookupLocked(cacheKey{id.key(), c})
}

func (k *Cache) lookupLocked(key cacheKey) (Result, bool) {
	r, ok := k.entries[key]
	if !ok {
		return Result{}, false
	}
	r.Stale = k.now().Sub(r.CheckedAt) >= k.ttl
	return r, true
}

// Claim returns the checks that have no fresh result and no review in
// flight, and marks them in flight. The caller must Run (or Store) every
// returned check, or later Claims never ask them again.
func (k *Cache) Claim(id Identity, checks ...Check) []Check {
	k.mu.Lock()
	defer k.mu.Unlock()
	idKey := id.key()
	var out []Check
	for _, c := range checks {
		key := cacheKey{idKey, c}
		if _, busy := k.inflight[key]; busy {
			continue
		}
		if r, ok := k.lookupLocked(key); ok && !r.Stale {
			continue
		}
		k.inflight[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

// Store records a result and clears the in-flight mark.
func (k *Cache) Store(id Identity, c Check, r Result) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := cacheKey{id.key(), c}
	delete(k.inflight, key)
	r.Stale = false
	k.entries[key] = r
	if len(k.entries) > maxEntries {
		k.pruneLocked()
	}
}

func (k *Cache) pruneLocked() {
	now := k.now()
	for key, r := range k.entries {
		if now.Sub(r.CheckedAt) >= k.ttl {
			delete(k.entries, key)
		}
	}
	if len(k.entries) > maxEntries {
		// Everything is fresh; start over rather than grow without bound.
		k.entries = make(map[cacheKey]Result)
	}
}

// Forget drops cached answers so the next Claim asks again. Reviews in
// flight are not affected.
func (k *Cache) Forget(id Identity, checks ...Check) {
	k.mu.Lock()
	defer k.mu.Unlock()
	idKey := id.key()
	for _, c := range checks {
		delete(k.entries, cacheKey{idKey, c})
	}
}

// Run reviews the checks with up to maxParallel requests at a time and
// stores every result, including failures. It returns when all are stored.
// Call it off the UI goroutine.
func (k *Cache) Run(ctx context.Context, r Reviewer, id Identity, checks []Check) {
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for _, c := range checks {
		wg.Add(1)
		sem <- struct{}{}
		go func(c Check) {
			defer wg.Done()
			defer func() { <-sem }()
			var res Result
			if err := ctx.Err(); err != nil {
				res = Result{Decision: Unknown, Reason: "access review canceled: " + err.Error(), CheckedAt: k.now()}
			} else {
				res = r.Review(ctx, c)
			}
			if res.CheckedAt.IsZero() {
				res.CheckedAt = k.now()
			}
			k.Store(id, c, res)
		}(c)
	}
	wg.Wait()
}

// Decide answers a check for gating UI actions from the cache only. A fresh
// or stale type-level Allowed answers a named check too: a rule that allows
// every object of a type allows each one. Otherwise the named result is
// used; with nothing cached the answer is Unknown.
func (k *Cache) Decide(id Identity, c Check) Decision {
	k.mu.Lock()
	defer k.mu.Unlock()
	idKey := id.key()
	if c.Name != "" {
		if r, ok := k.lookupLocked(cacheKey{idKey, c.TypeLevel()}); ok && r.Decision == Allowed {
			return Allowed
		}
	}
	if r, ok := k.lookupLocked(cacheKey{idKey, c}); ok {
		return r.Decision
	}
	return Unknown
}

// GateChecks returns the reviews that gating c still needs: first the
// type-level check, and the named check only when the type-level answer is
// not Allowed. Most identities that may delete one pod may delete all pods
// in the namespace, so one review per type and verb usually settles every
// row; resourceNames rules still get a named review.
func (k *Cache) GateChecks(id Identity, c Check) []Check {
	if c.Name == "" {
		return []Check{c}
	}
	k.mu.Lock()
	r, ok := k.lookupLocked(cacheKey{id.key(), c.TypeLevel()})
	k.mu.Unlock()
	if !ok || r.Stale {
		return []Check{c.TypeLevel()}
	}
	if r.Decision == Allowed {
		return nil
	}
	return []Check{c}
}
