package ui

import (
	"os"
	"sync"
	"syscall"
	"time"
)

// interruptGrace keeps SIGINT swallowed for a moment after a child that
// owned the terminal ended. The C-c that ended it reaches kc at the same
// time, and the signal goroutine may read it only after the child is gone.
const interruptGrace = time.Second

// interruptGate tells kc's signal handler that SIGINT belongs to a child
// that owns the terminal. While tea.Exec runs kubectl the terminal is in
// cooked mode, so C-c makes the tty send SIGINT to the whole foreground
// process group, kc included. The zero value is ready to use.
type interruptGate struct {
	mu         sync.Mutex
	held       int
	releasedAt time.Time
	// now is time.Now unless a test replaces it.
	now func() time.Time
}

func (g *interruptGate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// hold marks a child as owning the terminal until release is called.
func (g *interruptGate) hold() (release func()) {
	g.mu.Lock()
	g.held++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.held--
			g.releasedAt = g.clock()
		})
	}
}

// swallow reports whether a SIGINT received now is meant for a child.
func (g *interruptGate) swallow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held > 0 {
		return true
	}
	return !g.releasedAt.IsZero() && g.clock().Sub(g.releasedAt) < interruptGrace
}

// forwardQuitSignals calls quit on the first signal from sigs that is not a
// SIGINT meant for a child holding gate.
func forwardQuitSignals(sigs <-chan os.Signal, gate *interruptGate, quit func()) {
	for sig := range sigs {
		if sig == syscall.SIGINT && gate.swallow() {
			continue
		}
		quit()
		return
	}
}
