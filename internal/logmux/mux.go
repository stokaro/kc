package logmux

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/internal/logresume"
)

// DefaultMaxStreams is the default number of streams that run at once. It
// matches stern's --max-log-requests default for following logs; kubectl's
// default of 5 is too low for a workload with several pods and sidecars.
const DefaultMaxStreams = 50

// DefaultRemoveGrace is how long the stream of a target that left the set
// keeps running. A pod leaves the cache only after its containers stopped,
// so the stream has usually ended by then; the grace lets the last lines
// arrive and still stops a stream that hangs (a pod on a lost node that was
// force-deleted).
const DefaultRemoveGrace = 5 * time.Second

// outBuffer is the number of lines the mux holds for a slow reader before
// the streams wait.
const outBuffer = 256

// Opener opens the log stream of one target. opts already names the
// container. The stream must end when ctx is canceled.
type Opener func(ctx context.Context, t Target, opts *corev1.PodLogOptions) (io.ReadCloser, error)

// Line is one line of the merged stream.
type Line struct {
	// Target is the container the line comes from. It is the zero value
	// for a notice.
	Target Target
	// Notice marks a message from kc itself (a stream started, ended or
	// failed) rather than a container log line.
	Notice bool
	Text   string
}

// Options configure a Mux.
type Options struct {
	// LogOptions are the options of every stream. Container is set per
	// target.
	LogOptions corev1.PodLogOptions
	// MaxStreams limits the streams that run at once; zero or less means
	// DefaultMaxStreams. Further targets wait until a stream ends.
	MaxStreams int
	// RemoveGrace is how long the stream of a removed target keeps running;
	// zero or less means DefaultRemoveGrace.
	RemoveGrace time.Duration
	// Check, when set, returns the current state of a target's container
	// (Follower.Instance). With it a stream that ends while its container
	// still runs reconnects; without it only failed streams reconnect.
	Check func(Target) (logresume.Instance, bool)
}

func (o Options) maxStreams() int {
	if o.MaxStreams <= 0 {
		return DefaultMaxStreams
	}
	return o.MaxStreams
}

func (o Options) removeGrace() time.Duration {
	if o.RemoveGrace <= 0 {
		return DefaultRemoveGrace
	}
	return o.RemoveGrace
}

type streamState int

const (
	stateQueued streamState = iota
	stateRunning
	stateEnded
)

type stream struct {
	target  Target
	state   streamState
	cancel  context.CancelFunc
	removed bool
	timer   *time.Timer
}

// Stats is a snapshot of the mux streams.
type Stats struct {
	Running int
	Waiting int
	Ended   int
}

// Mux merges the log streams of a set of targets. SetTargets changes the
// set; Lines delivers the merged lines until Close.
//
// A target keeps one stream for its lifetime. A followed stream that fails,
// or ends while Options.Check says the container still runs, reconnects
// where it stopped, without repeating the tail (see logresume). Once the
// stream has ended it is not reopened. A restarted container or a
// recreated pod is a new target (see Target) and gets a new stream.
type Mux struct {
	open   Opener
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	out    chan Line

	mu       sync.Mutex
	closed   bool
	streams  map[Target]*stream
	queue    []Target
	running  int
	reported int // waiting count last reported in a notice
	wg       sync.WaitGroup
}

// New returns a mux that opens streams with open. The streams live until
// Close or until ctx ends.
func New(ctx context.Context, open Opener, opts Options) *Mux {
	ctx, cancel := context.WithCancel(ctx)
	return &Mux{
		open:    open,
		opts:    opts,
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan Line, outBuffer),
		streams: make(map[Target]*stream),
	}
}

// Lines returns the merged lines. The channel is closed after Close, once
// every stream goroutine has returned.
func (m *Mux) Lines() <-chan Line { return m.out }

// Done is closed when the mux is closed.
func (m *Mux) Done() <-chan struct{} { return m.ctx.Done() }

// Stats returns how many streams run, wait for a slot, and have ended.
func (m *Mux) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s Stats
	for _, st := range m.streams {
		switch st.state {
		case stateQueued:
			s.Waiting++
		case stateRunning:
			s.Running++
		case stateEnded:
			s.Ended++
		}
	}
	return s
}

// SetTargets replaces the target set. New targets are queued and start as
// slots free up; targets that left the set are dropped from the queue, and
// their running streams stop after the remove grace unless they end first.
// It is safe to call from any goroutine.
func (m *Mux) SetTargets(targets []Target) {
	want := make(map[Target]bool, len(targets))
	sorted := make([]Target, 0, len(targets))
	for _, t := range targets {
		if want[t] {
			continue
		}
		want[t] = true
		sorted = append(sorted, t)
	}
	sortTargets(sorted)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	for t, s := range m.streams {
		if want[t] {
			if s.removed {
				s.removed = false
				stopTimer(s)
			}
			continue
		}
		switch s.state {
		case stateQueued:
			delete(m.streams, t)
			m.dequeueLocked(t)
		case stateEnded:
			delete(m.streams, t)
		case stateRunning:
			if !s.removed {
				s.removed = true
				s.timer = time.AfterFunc(m.opts.removeGrace(), s.cancel)
			}
		}
	}
	for _, t := range sorted {
		if _, ok := m.streams[t]; ok {
			continue
		}
		m.streams[t] = &stream{target: t, state: stateQueued}
		m.queue = append(m.queue, t)
	}
	notices := m.fillLocked()
	m.emitUnlock(notices)
}

// Notify adds a notice line to the merged stream.
func (m *Mux) Notify(format string, args ...any) { //nolint:goprintffuncname // the rename to Notifyf also changes internal/ui, left for a follow-up in #128
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.emitUnlock([]Line{{Notice: true, Text: fmt.Sprintf(format, args...)}})
}

// emitUnlock releases m.mu and then emits lines. Callers outside a stream
// goroutine count as one in wg while they send, so Close cannot close the
// channel under them.
func (m *Mux) emitUnlock(lines []Line) {
	if len(lines) == 0 {
		m.mu.Unlock()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	m.emit(lines)
}

// Close stops every stream. It does not wait for them; Lines is closed once
// they have returned.
func (m *Mux) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, s := range m.streams {
		stopTimer(s)
	}
	m.queue = nil
	m.mu.Unlock()
	m.cancel()
	go func() {
		m.wg.Wait()
		close(m.out)
	}()
}

// Wait blocks until every stream goroutine has returned. It is meant for
// tests and for callers that must know the streams are gone.
func (m *Mux) Wait() { m.wg.Wait() }

func (m *Mux) dequeueLocked(t Target) {
	for i, q := range m.queue {
		if q == t {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return
		}
	}
}

// fillLocked starts queued streams while slots are free and returns the
// notices to emit once the lock is released.
func (m *Mux) fillLocked() []Line {
	if m.closed {
		return nil
	}
	var notices []Line
	limit := m.opts.maxStreams()
	for m.running < limit && len(m.queue) > 0 {
		t := m.queue[0]
		m.queue = m.queue[1:]
		s := m.streams[t]
		ctx, cancel := context.WithCancel(m.ctx)
		s.state = stateRunning
		s.cancel = cancel
		m.running++
		m.wg.Add(1)
		go m.run(ctx, s)
	}
	if waiting := len(m.queue); waiting != m.reported {
		m.reported = waiting
		if waiting > 0 {
			notices = append(notices, Line{Notice: true, Text: fmt.Sprintf(
				"%d %s for a free slot: at most %d run at once", waiting, plural(waiting, "stream waits", "streams wait"), limit)})
		}
	}
	return notices
}

func (m *Mux) run(ctx context.Context, s *stream) {
	defer m.wg.Done()
	t := s.target
	opts := m.opts.LogOptions
	opts.Container = t.Container
	err := m.stream(ctx, t, &opts)
	ended := Line{Notice: true, Text: "- " + t.Name()}
	if err != nil && ctx.Err() == nil {
		ended.Text = fmt.Sprintf("- %s: %v", t.Name(), err)
	}
	// Report the end before the slot goes to the next target, so that
	// "- old" comes before "+ next".
	if m.ctx.Err() == nil {
		m.emit([]Line{ended})
	}
	m.finish(s)
}

// stream reads the log of t until it ends. A followed stream that fails
// while the container still runs is reopened where it stopped (see
// logresume); the first open's error is returned.
func (m *Mux) stream(ctx context.Context, t Target, opts *corev1.PodLogOptions) error {
	opened := false
	cfg := logresume.Config{
		Options: *opts,
		Open: func(ctx context.Context, o *corev1.PodLogOptions) (io.ReadCloser, error) {
			rc, err := m.open(ctx, t, o)
			if err == nil && !opened {
				opened = true
				m.emit([]Line{{Notice: true, Text: "+ " + t.Name()}})
			}
			return rc, err
		},
		Start: logresume.Instance{PodUID: t.PodUID, Restarts: t.Restarts, Running: true},
	}
	if m.opts.Check != nil {
		cfg.Check = func(context.Context) (logresume.Instance, bool) { return m.opts.Check(t) }
	}
	return logresume.Run(ctx, cfg, logresume.Sink{
		Line:   func(text string) bool { return m.send(Line{Target: t, Text: text}) },
		Notice: func(text string) { m.emit([]Line{{Notice: true, Text: "~ " + t.Name() + ": " + text}}) },
	})
}

func (m *Mux) finish(s *stream) {
	m.mu.Lock()
	m.running--
	stopTimer(s)
	s.cancel()
	if s.removed || m.closed {
		delete(m.streams, s.target)
	} else {
		s.state = stateEnded
	}
	notices := m.fillLocked()
	m.mu.Unlock()
	m.emit(notices)
}

// send delivers one line; it reports false once the mux is closed.
func (m *Mux) send(l Line) bool {
	select {
	case m.out <- l:
		return true
	case <-m.ctx.Done():
		return false
	}
}

func (m *Mux) emit(lines []Line) {
	for _, l := range lines {
		if !m.send(l) {
			return
		}
	}
}

func stopTimer(s *stream) {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
