package promhistory

import (
	"context"
	"sync"
	"time"
)

// Defaults of a Shared.
const (
	// DefaultConcurrency is how many reads run against backends at once,
	// for all charts together.
	DefaultConcurrency = 4
	// DefaultCacheEntries bounds the results a Shared keeps.
	DefaultCacheEntries = 64
)

// Shared runs reads so that identical ones share one request: a read with
// the key of a running one waits for it, and a read with the key of a
// result younger than its TTL gets that result. A running read is
// canceled when every caller waiting for it has gone. At most Concurrency
// reads run at once; the others wait for a slot, or for their context.
//
// The key must hold everything the result depends on: the context and
// identity, the backend and its labels, the query and the range. The
// zero Shared is not usable; use NewShared.
type Shared struct {
	slots   chan struct{}
	max     int
	now     func() time.Time
	mu      sync.Mutex
	running map[string]*sharedCall
	done    map[string]sharedResult
	order   []string // keys of done, oldest first
}

type sharedCall struct {
	ready   chan struct{}
	val     any
	err     error
	waiters int
	cancel  context.CancelFunc
}

type sharedResult struct {
	val     any
	expires time.Time
}

// NewShared returns a Shared that runs at most concurrency reads at once
// and keeps at most entries results.
func NewShared(concurrency, entries int) *Shared {
	return &Shared{
		slots:   make(chan struct{}, max(concurrency, 1)),
		max:     max(entries, 1),
		now:     time.Now,
		running: make(map[string]*sharedCall),
		done:    make(map[string]sharedResult),
	}
}

// Do returns the result of read for key. ttl is how long a success is
// reused; errors are never reused. read runs with a context of its own
// that ends when its deadline (that of the first caller) passes or when
// no caller waits any more.
func (s *Shared) Do(ctx context.Context, key string, ttl time.Duration, read func(context.Context) (any, error)) (any, error) {
	s.mu.Lock()
	if r, ok := s.done[key]; ok && s.now().Before(r.expires) {
		s.mu.Unlock()
		return r.val, nil
	}
	c, ok := s.running[key]
	if !ok {
		c = &sharedCall{ready: make(chan struct{})}
		// The read outlives the caller that started it while others wait;
		// it keeps that caller's deadline and values.
		callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		if d, ok := ctx.Deadline(); ok {
			cancel()
			callCtx, cancel = context.WithDeadline(context.WithoutCancel(ctx), d)
		}
		c.cancel = cancel
		s.running[key] = c
		go s.run(callCtx, key, ttl, c, read)
	}
	c.waiters++
	s.mu.Unlock()

	select {
	case <-c.ready:
		return c.val, c.err
	case <-ctx.Done():
		s.mu.Lock()
		c.waiters--
		if c.waiters == 0 {
			c.cancel()
		}
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (s *Shared) run(ctx context.Context, key string, ttl time.Duration, c *sharedCall, read func(context.Context) (any, error)) {
	defer c.cancel()
	var val any
	var err error
	select {
	case s.slots <- struct{}{}:
		val, err = read(ctx)
		<-s.slots
	case <-ctx.Done():
		err = ctx.Err()
	}
	s.mu.Lock()
	delete(s.running, key)
	if err == nil && ttl > 0 {
		s.store(key, sharedResult{val: val, expires: s.now().Add(ttl)})
	}
	c.val, c.err = val, err
	close(c.ready)
	s.mu.Unlock()
}

// store keeps r under key and drops the oldest results beyond the bound.
// The caller holds s.mu.
func (s *Shared) store(key string, r sharedResult) {
	if _, ok := s.done[key]; !ok {
		s.order = append(s.order, key)
	}
	s.done[key] = r
	for len(s.order) > s.max {
		delete(s.done, s.order[0])
		s.order = s.order[1:]
	}
}
