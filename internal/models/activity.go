package models

import "sync"

// Activity gates background refreshes of resource counts. While paused,
// recounts are deferred and run once on Resume; the UI pauses it while a
// dialog or menu is open so the cluster is not polled behind it.
type Activity struct {
	mu       sync.Mutex
	paused   bool
	deferred map[*ResourceGroupItem]struct{}
}

// Pause defers background recounts until Resume.
func (a *Activity) Pause() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
}

// Resume runs the recounts deferred while paused.
func (a *Activity) Resume() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.paused = false
	items := a.deferred
	a.deferred = nil
	a.mu.Unlock()
	for r := range items {
		r.scheduleRecount()
	}
}

// Paused reports whether background refreshes are paused.
func (a *Activity) Paused() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// deferIfPaused queues r for Resume and reports true when paused.
func (a *Activity) deferIfPaused(r *ResourceGroupItem) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.paused {
		return false
	}
	if a.deferred == nil {
		a.deferred = make(map[*ResourceGroupItem]struct{})
	}
	a.deferred[r] = struct{}{}
	return true
}

// forget drops r from the deferred set, e.g. when it is closed.
func (a *Activity) forget(r *ResourceGroupItem) {
	if a == nil {
		return
	}
	a.mu.Lock()
	delete(a.deferred, r)
	a.mu.Unlock()
}
