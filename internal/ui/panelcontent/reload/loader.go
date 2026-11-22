// Package reload loads panel content in the background and refreshes it.
//
// A Loader belongs to one widget. It keeps at most one load in flight, drops
// results for a target the widget no longer shows, and limits refreshes of
// the same target to one per Interval.
package reload

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Interval is the shortest time between the starts of two loads of the same
// target that a refresh causes.
const Interval = time.Second

// Result is what a Fetch produced.
type Result struct {
	Value any
	// Version identifies the loaded state, for example a resourceVersion or
	// a hash of the body. Empty means unknown.
	Version string
	// Unchanged reports that Fetch found the known version and loaded
	// nothing; the shown content stays.
	Unchanged bool
	Err       error
}

// Fetch loads the target. It runs in a tea.Cmd, off the UI goroutine. known
// is the version of the content shown for this target, or "" before the
// first load.
type Fetch func(known string) Result

// Loader schedules Fetch calls for one widget. Its methods run on the UI
// goroutine.
type Loader struct {
	// Paused delays refreshes while it reports true. Loads of a new target
	// are not delayed: the user asked for them.
	Paused func() bool
	// Now and After replace time.Now and tea.Tick in tests.
	Now   func() time.Time
	After func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

	active bool
	key    string
	fetch  Fetch
	gen    uint64 // changes with key; results of an older gen are dropped

	loaded   bool   // a result for key was applied
	version  string // Result.Version of the applied result
	inflight bool   // a Fetch runs; at most one at a time
	queued   bool   // a load for key waits for the in-flight Fetch or activation
	dirty    bool   // a refresh was asked while busy or inactive
	ticking  bool   // a refresh tick is scheduled
	started  time.Time
}

type loadedMsg struct {
	l   *Loader
	gen uint64
	res Result
}

type tickMsg struct {
	l   *Loader
	gen uint64
}

func (loadedMsg) WidgetBackground() {}
func (tickMsg) WidgetBackground()   {}

// SetActive tells the loader whether its widget is the panel's active one.
// An inactive loader starts nothing; the next Show after activation catches
// up.
func (l *Loader) SetActive(active bool) { l.active = active }

// Key returns the target shown or being loaded, "" for none.
func (l *Loader) Key() string { return l.key }

// Loading reports whether the content for the current target has not
// arrived yet. A refresh of shown content does not count.
func (l *Loader) Loading() bool { return l.key != "" && !l.loaded }

// Show switches to the target identified by key. A new key starts a load at
// once; the same key asks for a refresh. An empty key clears the target and
// drops pending results.
func (l *Loader) Show(key string, fetch Fetch) tea.Cmd {
	if key == "" || fetch == nil {
		l.reset("")
		return nil
	}
	if key == l.key {
		l.fetch = fetch
		return l.Refresh()
	}
	l.reset(key)
	l.fetch = fetch
	return l.start()
}

func (l *Loader) reset(key string) {
	l.key = key
	l.fetch = nil
	l.gen++
	l.loaded = false
	l.version = ""
	l.queued = false
	l.dirty = false
	l.ticking = false // a tick of the old gen is ignored when it fires
}

// Refresh asks to reload the current target. The load starts no earlier
// than Interval after the previous one started and not while Paused.
func (l *Loader) Refresh() tea.Cmd {
	if l.key == "" {
		return nil
	}
	if l.queued {
		return l.start()
	}
	if l.inflight || l.ticking || !l.active {
		l.dirty = true
		return nil
	}
	wait := max(Interval-l.now().Sub(l.started), 0)
	return l.tick(wait)
}

func (l *Loader) tick(wait time.Duration) tea.Cmd {
	l.ticking = true
	after := l.After
	if after == nil {
		after = tea.Tick
	}
	msg := tickMsg{l: l, gen: l.gen}
	return after(wait, func(time.Time) tea.Msg { return msg })
}

func (l *Loader) start() tea.Cmd {
	if l.inflight || !l.active {
		l.queued = true
		return nil
	}
	l.queued = false
	l.dirty = false
	l.inflight = true
	l.started = l.now()
	msg := loadedMsg{l: l, gen: l.gen}
	fetch, known := l.fetch, l.version
	return func() tea.Msg {
		msg.res = fetch(known)
		return msg
	}
}

// Update consumes the loader's own messages. When a result for the current
// target arrived and changes what is shown, apply is true and res holds it.
// handled is false for messages of other loaders.
func (l *Loader) Update(msg tea.Msg) (res Result, apply bool, cmd tea.Cmd, handled bool) {
	switch m := msg.(type) {
	case loadedMsg:
		if m.l != l {
			return Result{}, false, nil, false
		}
		// Only one Fetch runs at a time, so this is the one in flight.
		l.inflight = false
		if m.gen == l.gen {
			l.loaded = true
			l.version = m.res.Version
			res, apply = m.res, !m.res.Unchanged
		}
		switch {
		case l.queued:
			cmd = l.start()
		case l.dirty:
			l.dirty = false
			cmd = l.Refresh()
		}
		return res, apply, cmd, true
	case tickMsg:
		if m.l != l {
			return Result{}, false, nil, false
		}
		if m.gen != l.gen {
			return Result{}, false, nil, true
		}
		l.ticking = false
		switch {
		case !l.active:
			l.dirty = true
		case l.Paused != nil && l.Paused():
			cmd = l.tick(Interval)
		default:
			cmd = l.start()
		}
		return Result{}, false, cmd, true
	}
	return Result{}, false, nil, false
}

func (l *Loader) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}
