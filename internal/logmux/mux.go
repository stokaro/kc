package logmux

import (
	"context"
	"fmt"
	"io"
	"strings"
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
	// excluded is set when the filter dropped the running stream.
	excluded bool
	// reconnecting is set while a resumed stream waits to reconnect.
	reconnecting bool
	// err is why the stream failed; nil for a stream that ended cleanly.
	err error
	// fresh opens the stream with no earlier lines: the target streamed
	// before, was excluded and is selected again.
	fresh bool
	// pos is where the stream continues; a retry resumes from it.
	pos *logresume.Position
}

// Stats is a snapshot of the mux streams. Running streams include the
// reconnecting ones and the streams of removed targets that still finish;
// Ended does not count the failed streams.
type Stats struct {
	Running      int
	Reconnecting int
	Waiting      int
	Ended        int
	Failed       int
	// Excluded counts the targets the filter leaves out.
	Excluded int
	// Limit is the number of streams that run at once.
	Limit int
}

// Incomplete reports a selection whose sources are not all followed: some
// wait for a slot, wait to reconnect, or failed.
func (s Stats) Incomplete() bool { return s.Waiting > 0 || s.Reconnecting > 0 || s.Failed > 0 }

// String describes the coverage for a status line, for example
// "incomplete: 50 streaming, 20 waiting (limit 50), 2 failed".
func (s Stats) String() string {
	parts := []string{fmt.Sprintf("%d streaming", s.Running-s.Reconnecting)}
	if s.Reconnecting > 0 {
		parts = append(parts, fmt.Sprintf("%d reconnecting", s.Reconnecting))
	}
	if s.Waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting (limit %d)", s.Waiting, s.Limit))
	}
	if s.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", s.Failed))
	}
	if s.Ended > 0 {
		parts = append(parts, fmt.Sprintf("%d ended", s.Ended))
	}
	if s.Excluded > 0 {
		parts = append(parts, fmt.Sprintf("%d excluded", s.Excluded))
	}
	text := strings.Join(parts, ", ")
	if s.Incomplete() {
		text = "incomplete: " + text
	}
	return text
}

// SourceState is what a source of a merged log does now.
type SourceState int

const (
	SourceStreaming SourceState = iota
	SourceReconnecting
	SourceWaiting
	SourceFailed
	SourceEnded
	SourceExcluded
)

func (s SourceState) String() string {
	switch s {
	case SourceStreaming:
		return "streaming"
	case SourceReconnecting:
		return "reconnecting"
	case SourceWaiting:
		return "waiting"
	case SourceFailed:
		return "failed"
	case SourceEnded:
		return "ended"
	case SourceExcluded:
		return "excluded"
	}
	return "unknown"
}

// Source is one target of the set and what its stream does.
type Source struct {
	Target Target
	State  SourceState
	// Err is the last failure of a failed source.
	Err error
}

// Mux merges the log streams of a set of targets. SetTargets changes the
// set; Lines delivers the merged lines until Close.
//
// A target keeps one stream for its lifetime. A followed stream that fails,
// or ends while Options.Check says the container still runs, reconnects
// where it stopped, without repeating the tail (see logresume). Once the
// stream has ended it is not reopened, unless Retry asks for a failed
// one. A restarted container or a recreated pod is a new target (see
// Target) and gets a new stream.
//
// SetFilter narrows the set by pod and container name. An excluded target
// stops at once; selected again, it streams from that moment on, so the
// lines already shown are not repeated. The other streams are untouched.
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
	limit    int
	filter   Filter
	// candidates is the last target set, sorted, before the filter.
	candidates []Target
	// positions keeps where each candidate's stream got to, so that a
	// target streamed before is not read from its start again.
	positions map[Target]*logresume.Position
	wg        sync.WaitGroup
}

// New returns a mux that opens streams with open. The streams live until
// Close or until ctx ends.
func New(ctx context.Context, open Opener, opts Options) *Mux {
	ctx, cancel := context.WithCancel(ctx)
	return &Mux{
		open:      open,
		opts:      opts,
		ctx:       ctx,
		cancel:    cancel,
		out:       make(chan Line, outBuffer),
		streams:   make(map[Target]*stream),
		limit:     opts.maxStreams(),
		positions: make(map[Target]*logresume.Position),
	}
}

// Lines returns the merged lines. The channel is closed after Close, once
// every stream goroutine has returned.
func (m *Mux) Lines() <-chan Line { return m.out }

// Done is closed when the mux is closed.
func (m *Mux) Done() <-chan struct{} { return m.ctx.Done() }

// Stats returns how many streams run, wait for a slot, have ended or
// failed, and how many targets the filter leaves out.
func (m *Mux) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Stats{Limit: m.limit}
	for _, st := range m.streams {
		switch st.state {
		case stateQueued:
			s.Waiting++
		case stateRunning:
			s.Running++
			if st.reconnecting {
				s.Reconnecting++
			}
		case stateEnded:
			if st.err != nil {
				s.Failed++
			} else {
				s.Ended++
			}
		}
	}
	for _, t := range m.candidates {
		if !m.filter.Match(t) {
			s.Excluded++
		}
	}
	return s
}

// Sources returns every target of the set, in order, with its state. The
// streams of removed targets that still finish are left out.
func (m *Mux) Sources() []Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Source, 0, len(m.candidates))
	for _, t := range m.candidates {
		src := Source{Target: t, State: SourceExcluded}
		if s, ok := m.streams[t]; ok && m.filter.Match(t) {
			src.State = s.sourceState()
			src.Err = s.err
		}
		out = append(out, src)
	}
	return out
}

func (s *stream) sourceState() SourceState {
	switch {
	case s.state == stateQueued:
		return SourceWaiting
	case s.state == stateEnded && s.err != nil:
		return SourceFailed
	case s.state == stateEnded:
		return SourceEnded
	case s.reconnecting:
		return SourceReconnecting
	}
	return SourceStreaming
}

// Filter returns the current filter.
func (m *Mux) Filter() Filter {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.filter
}

// SetFilter selects the targets f matches. Targets it excludes stop at
// once; targets it selects again stream from now on. Streams of targets
// that stay selected are untouched. The same filter again changes nothing.
func (m *Mux) SetFilter(f Filter) {
	m.mu.Lock()
	if m.closed || f.String() == m.filter.String() {
		m.mu.Unlock()
		return
	}
	m.filter = f
	text := "source filter cleared"
	if !f.IsZero() {
		text = "source filter: " + f.String()
	}
	notices := append([]Line{{Notice: true, Text: text}}, m.applyLocked()...)
	m.emitUnlock(notices)
}

// SetMaxStreams changes how many streams run at once; zero or less means
// DefaultMaxStreams. A lower limit lets running streams go on and starts
// no new ones until the count is below it.
func (m *Mux) SetMaxStreams(n int) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.limit = Options{MaxStreams: n}.maxStreams()
	m.reported = -1 // report the waiting streams with the new limit
	m.emitUnlock(m.fillLocked())
}

// Retry queues the failed streams again; each continues after the last
// line it delivered. It returns how many it queued.
func (m *Mux) Retry() int {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0
	}
	n := 0
	for _, t := range m.candidates {
		s, ok := m.streams[t]
		if !ok || s.state != stateEnded || s.err == nil {
			continue
		}
		s.state, s.err, s.fresh = stateQueued, nil, false
		m.queue = append(m.queue, t)
		n++
	}
	m.emitUnlock(m.fillLocked())
	return n
}

// SetTargets replaces the target set. New targets are queued and start as
// slots free up; targets that left the set are dropped from the queue, and
// their running streams stop after the remove grace unless they end first.
// The filter applies to the new set as well. It is safe to call from any
// goroutine.
func (m *Mux) SetTargets(targets []Target) {
	seen := make(map[Target]bool, len(targets))
	sorted := make([]Target, 0, len(targets))
	for _, t := range targets {
		if seen[t] {
			continue
		}
		seen[t] = true
		sorted = append(sorted, t)
	}
	sortTargets(sorted)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.candidates = sorted
	for t := range m.positions {
		if !seen[t] {
			delete(m.positions, t)
		}
	}
	m.emitUnlock(m.applyLocked())
}

// applyLocked brings the streams in step with the candidates and the
// filter, and returns the notices to emit.
func (m *Mux) applyLocked() []Line {
	candidate := make(map[Target]bool, len(m.candidates))
	for _, t := range m.candidates {
		candidate[t] = true
	}
	for t, s := range m.streams {
		if candidate[t] && m.filter.Match(t) {
			if s.removed && !s.excluded {
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
			switch {
			case candidate[t]:
				// The user excluded it: stop now, not after a grace.
				stopTimer(s)
				s.removed, s.excluded = true, true
				s.cancel()
			case !s.removed:
				s.removed = true
				s.timer = time.AfterFunc(m.opts.removeGrace(), s.cancel)
			}
		}
	}
	for _, t := range m.candidates {
		if _, ok := m.streams[t]; ok || !m.filter.Match(t) {
			continue
		}
		_, before := m.positions[t]
		m.streams[t] = &stream{target: t, state: stateQueued, fresh: before}
		m.queue = append(m.queue, t)
	}
	return m.fillLocked()
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
	limit := m.limit
	for m.running < limit && len(m.queue) > 0 {
		t := m.queue[0]
		m.queue = m.queue[1:]
		s := m.streams[t]
		ctx, cancel := context.WithCancel(m.ctx)
		s.state = stateRunning
		s.cancel = cancel
		if s.fresh || m.positions[t] == nil {
			m.positions[t] = &logresume.Position{}
		}
		s.pos = m.positions[t]
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
	if s.fresh {
		// Selected again: only the lines from now on, as `--tail=0 -f`.
		zero := int64(0)
		opts.TailLines, opts.SinceSeconds, opts.SinceTime = &zero, nil, nil
	}
	err := m.stream(ctx, s, &opts)
	m.mu.Lock()
	excluded := s.excluded
	if err != nil && ctx.Err() == nil {
		s.err = err
	}
	s.reconnecting = false
	m.mu.Unlock()
	ended := Line{Notice: true, Text: "- " + t.Name()}
	switch {
	case excluded:
		ended.Text += " (excluded)"
	case err != nil && ctx.Err() == nil:
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
func (m *Mux) stream(ctx context.Context, s *stream, opts *corev1.PodLogOptions) error {
	t := s.target
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
		Start:    logresume.Instance{PodUID: t.PodUID, Restarts: t.Restarts, Running: true},
		Position: s.pos,
	}
	if m.opts.Check != nil {
		cfg.Check = func(context.Context) (logresume.Instance, bool) { return m.opts.Check(t) }
	}
	return logresume.Run(ctx, cfg, logresume.Sink{
		Line:   func(text string) bool { return m.send(Line{Target: t, Text: text}) },
		Notice: func(text string) { m.emit([]Line{{Notice: true, Text: "~ " + t.Name() + ": " + text}}) },
		State: func(st logresume.State) {
			m.mu.Lock()
			s.reconnecting = st == logresume.StateReconnecting
			m.mu.Unlock()
		},
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
	var notices []Line
	if !m.closed {
		// applyLocked also restarts a target that was selected again
		// while its excluded stream was still stopping.
		notices = m.applyLocked()
	}
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
