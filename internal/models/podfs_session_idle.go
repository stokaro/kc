package models

import (
	"context"
	"fmt"
	"sync"
	"time"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/podfs"
)

// acquire returns the container's filesystem session, opening it when
// needed, and holds it for the caller until release is called. A held
// session does not close for being idle, however long the operation runs
// (a large copy). After the last release the session closes once it has
// stayed unused for h.idle; the next acquire opens a new one.
//
// release is safe to call more than once, and is a no-op when err is set.
// The session must not be used after release.
func (h *containerSessionHandle) acquire(ctx context.Context) (sess podfs.ExecSession, release func(), err error) {
	if h == nil {
		return nil, func() {}, fmt.Errorf("pod filesystem unavailable")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sess, err = h.openLocked(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	h.users++
	h.stopIdleLocked()
	var once sync.Once
	return sess, func() { once.Do(h.release) }, nil
}

func (h *containerSessionHandle) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.users--
	if h.users > 0 || h.session == nil || h.idle <= 0 {
		return
	}
	h.stopIdleLocked()
	gen := h.idleGen
	h.idleTimer = time.AfterFunc(h.idle, func() { h.expire(gen) })
}

// expire closes the session the idle timer armed for gen. An acquire
// that took the lock after the timer fired but before expire ran moved
// idleGen, and the session stays open for it.
func (h *containerSessionHandle) expire(gen uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if gen != h.idleGen || h.session == nil {
		return
	}
	ctrllog.FromContext(h.root).WithName("podfs_session").V(1).Info("Closing idle filesystem session",
		"namespace", h.spec.Namespace, "pod", h.spec.Pod, "container", h.spec.Container, "idle", h.idle)
	_ = h.closeLocked()
}

// stopIdleLocked disarms the idle timer. h.mu must be held.
func (h *containerSessionHandle) stopIdleLocked() {
	if h.idleTimer != nil {
		h.idleTimer.Stop()
		h.idleTimer = nil
	}
	h.idleGen++
}
