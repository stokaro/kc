package logmux

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	// Scope names the pods the set covers, such as "selector app=web in
	// shop", for the notice that none match.
	Scope string
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
	// stateHeld is a selected source whose container waits and has no log
	// yet; it is queued once the pod status says the container started.
	stateHeld
)

type stream struct {
	target  Target
	state   streamState
	cancel  context.CancelFunc
	removed bool
	timer   *time.Timer
	// previous is set when the stream reads the last terminated instance
	// of a waiting container; reason is why the container waited then.
	previous bool
	reason   string
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
// Ended does not count the failed streams. Waiting counts the streams that
// wait for a free slot, ContainerWaiting the sources whose container waits.
type Stats struct {
	Running      int
	Reconnecting int
	Waiting      int
	Ended        int
	Failed       int
	// ContainerWaiting counts the sources whose container waits, by the
	// reason the pod status gives (CrashLoopBackOff, ContainerCreating),
	// including those whose previous instance is being read.
	ContainerWaiting map[string]int
	// Excluded counts the targets the filter leaves out.
	Excluded int
	// Limit is the number of streams that run at once.
	Limit int
}

// Incomplete reports a selection whose sources are not all followed: some
// wait for a slot, wait to reconnect, or failed. A source whose container
// waits is followed: its next instance streams when it starts.
func (s Stats) Incomplete() bool { return s.Waiting > 0 || s.Reconnecting > 0 || s.Failed > 0 }

// String describes the coverage for a status line, for example
// "incomplete: 50 streaming, 20 waiting (limit 50), 2 failed, 1 waiting
// (CrashLoopBackOff)".
func (s Stats) String() string {
	parts := []string{fmt.Sprintf("%d streaming", s.Running-s.Reconnecting)}
	if s.Reconnecting > 0 {
		parts = append(parts, fmt.Sprintf("%d reconnecting", s.Reconnecting))
	}
	if s.Waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting (limit %d)", s.Waiting, s.Limit))
	}
	parts = append(parts, s.idleParts()...)
	if s.Excluded > 0 {
		parts = append(parts, fmt.Sprintf("%d excluded", s.Excluded))
	}
	text := strings.Join(parts, ", ")
	if s.Incomplete() {
		text = "incomplete: " + text
	}
	return text
}

// idleParts counts the sources that have nothing to stream now: failed,
// waiting containers by reason, and ended.
func (s Stats) idleParts() []string {
	var parts []string
	if s.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", s.Failed))
	}
	for _, reason := range slices.Sorted(maps.Keys(s.ContainerWaiting)) {
		parts = append(parts, fmt.Sprintf("%d waiting (%s)", s.ContainerWaiting[reason], reason))
	}
	if s.Ended > 0 {
		parts = append(parts, fmt.Sprintf("%d ended", s.Ended))
	}
	return parts
}

// SourceState is what a source of a merged log does now.
type SourceState int

const (
	SourceStreaming SourceState = iota
	SourceReconnecting
	// SourceWaiting waits for a free slot under the stream limit.
	SourceWaiting
	SourceFailed
	SourceEnded
	SourceExcluded
	// SourceContainerWaiting is a source whose container waits (to start,
	// or to restart after a crash). Source.Waiting says why.
	SourceContainerWaiting
)

func (s SourceState) String() string {
	switch s {
	case SourceStreaming:
		return "streaming"
	case SourceReconnecting:
		return "reconnecting"
	case SourceWaiting, SourceContainerWaiting:
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
	// Waiting is why the container of a SourceContainerWaiting source
	// waits, for example CrashLoopBackOff.
	Waiting string
	// Previous is set when the source shows the last terminated instance
	// of a waiting container.
	Previous bool
	// Done says how the pod of an ended source without a log ended, such
	// as "pod Failed (OutOfcpu)" (Candidate.Done).
	Done string
}

// Status is the state as the sources dialog shows it: "waiting
// (CrashLoopBackOff)" for a container that waits, the state name
// otherwise.
func (s Source) Status() string {
	if s.State == SourceContainerWaiting {
		return "waiting (" + s.Waiting + ")"
	}
	return s.State.String()
}

// Mux merges the log streams of a set of targets. SetCandidates (or
// SetTargets) changes the set; Lines delivers the merged lines until Close.
//
// A target keeps one stream for its lifetime. A followed stream that fails,
// or ends while Options.Check says the container still runs, reconnects
// where it stopped, without repeating the tail (see logresume). Once the
// stream has ended it is not reopened, unless Retry asks for a failed
// one. A restarted container or a recreated pod is a new target (see
// Target) and gets a new stream.
//
// A target whose container waits gets no request while it has no log; it
// is opened when the pod status says the container started. A waiting
// container that ran before (a crash loop) has a log: its last terminated
// instance is read once, with Previous, and not followed. A container
// without a log in a pod that is over (Candidate.Done) is ended at once.
//
// SetFilter narrows the set by pod and container name. An excluded target
// stops at once; selected again, it streams from that moment on, so the
// lines already shown are not repeated. The other streams are untouched.
//
// Once Synced says the set is complete, and until the first stream opens,
// the mux explains in a notice why the view is empty, whenever that reason
// changes (see docs/adr/0379-waiting-and-failed-log-sources.md).
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
	// waiting holds the candidates whose container waits.
	waiting map[Target]Candidate
	// positions keeps where each candidate's stream got to, so that a
	// target streamed before is not read from its start again.
	positions map[Target]*logresume.Position
	// synced is set once the target set is complete (Synced); explained is
	// the last notice that said why the view has no log line.
	synced    bool
	explained string
	// streamed is set when the first stream opens: from then on its
	// `+` line is in the view, so the view is not empty.
	streamed atomic.Bool
	wg       sync.WaitGroup
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
		waiting:   make(map[Target]Candidate),
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
		if st.removed {
			// A removed or excluded target whose stream still stops.
			if st.state == stateRunning {
				s.Running++
				if st.reconnecting {
					s.Reconnecting++
				}
			}
			continue
		}
		s.count(m.sourceLocked(st))
	}
	for _, t := range m.candidates {
		if !m.filter.Match(t) {
			s.Excluded++
		}
	}
	return s
}

// count adds a selected source to the counts.
func (s *Stats) count(src Source) {
	switch src.State {
	case SourceStreaming:
		s.Running++
	case SourceReconnecting:
		s.Running++
		s.Reconnecting++
	case SourceWaiting:
		s.Waiting++
	case SourceFailed:
		s.Failed++
	case SourceEnded:
		s.Ended++
	case SourceContainerWaiting:
		if s.ContainerWaiting == nil {
			s.ContainerWaiting = make(map[string]int)
		}
		s.ContainerWaiting[src.Waiting]++
	case SourceExcluded:
	}
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
			src = m.sourceLocked(s)
		}
		out = append(out, src)
	}
	return out
}

// sourceLocked is what the source of s does now. A failure, a reconnect and
// a wait for a slot come first; then a container that waits, whether its
// previous instance is still being read or not.
func (m *Mux) sourceLocked(s *stream) Source {
	src := Source{Target: s.target, Err: s.err, Previous: s.previous}
	w, waits := m.waiting[s.target]
	switch {
	case s.state == stateQueued:
		src.State = SourceWaiting
	case s.state == stateEnded && s.err != nil:
		src.State = SourceFailed
	case s.reconnecting:
		src.State = SourceReconnecting
	case waits && w.Done != "":
		src.State, src.Done = SourceEnded, w.Done
	case waits || s.state == stateHeld:
		src.State = SourceContainerWaiting
		src.Waiting = cmp.Or(w.Waiting, waitingUnknown)
	case s.state == stateEnded:
		src.State = SourceEnded
	default:
		src.State = SourceStreaming
	}
	return src
}

// hasLogLocked reports whether the container of s has a log to read now:
// it runs or terminated, or it waits after an earlier instance. A source
// selected again after the filter excluded it reads from that moment on
// (fresh), and an instance that is over has nothing new.
func (m *Mux) hasLogLocked(s *stream) bool {
	w, waits := m.waiting[s.target]
	return !waits || (w.Previous && !s.fresh)
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
// line it delivered. A failed source whose container now waits without a
// log is held until it runs. It returns how many failed sources it reset.
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
		// A container that now waits without a log is held until it runs.
		s.state, s.err, s.fresh = stateHeld, nil, false
		m.holdLocked(s)
		n++
	}
	m.emitUnlock(append(m.fillLocked(), m.explainLocked()...))
	return n
}

// SetTargets replaces the target set with targets whose containers have a
// log to read; see SetCandidates.
func (m *Mux) SetTargets(targets []Target) {
	cs := make([]Candidate, len(targets))
	for i, t := range targets {
		cs[i] = Candidate{Target: t}
	}
	m.SetCandidates(cs)
}

// SetCandidates replaces the target set. New targets are queued and start
// as slots free up, or wait without a request while their container has no
// log; targets that left the set are dropped from the queue, and their
// running streams stop after the remove grace unless they end first. The
// filter applies to the new set as well. It is safe to call from any
// goroutine.
func (m *Mux) SetCandidates(candidates []Candidate) {
	seen := make(map[Target]bool, len(candidates))
	sorted := make([]Target, 0, len(candidates))
	waiting := make(map[Target]Candidate)
	for _, c := range candidates {
		if seen[c.Target] {
			continue
		}
		seen[c.Target] = true
		sorted = append(sorted, c.Target)
		if c.Waiting != "" || c.Done != "" {
			waiting[c.Target] = c
		}
	}
	sortTargets(sorted)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.candidates = sorted
	m.waiting = waiting
	for t := range m.positions {
		if !seen[t] {
			delete(m.positions, t)
		}
	}
	m.emitUnlock(m.applyLocked())
}

// applyLocked brings the streams in step with the candidates, their
// container states and the filter, and returns the notices to emit.
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
		case stateEnded, stateHeld:
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
	// In candidate order, so that the streams that start first under the
	// limit do not depend on map order.
	for _, t := range m.candidates {
		if !m.filter.Match(t) {
			continue
		}
		s, ok := m.streams[t]
		if !ok {
			_, before := m.positions[t]
			s = &stream{target: t, state: stateHeld, fresh: before}
			m.streams[t] = s
		}
		m.holdLocked(s)
	}
	return append(m.fillLocked(), m.explainLocked()...)
}

// holdLocked queues a held stream whose container now has a log, and holds
// a queued one whose container has none.
func (m *Mux) holdLocked(s *stream) {
	switch has := m.hasLogLocked(s); {
	case s.state == stateHeld && has:
		s.state = stateQueued
		m.queue = append(m.queue, s.target)
	case s.state == stateQueued && !has:
		s.state = stateHeld
		m.dequeueLocked(s.target)
	}
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

// Synced marks the target set as complete: the informer's initial list of
// pods was delivered. From then on, until the first stream opens, a notice
// says why the view is empty whenever that reason changes.
func (m *Mux) Synced() {
	m.mu.Lock()
	if m.closed || m.synced {
		m.mu.Unlock()
		return
	}
	m.synced = true
	m.emitUnlock(m.explainLocked())
}

// explainLocked returns a notice that says why no log line has arrived,
// when the reason differs from the last one given. It says nothing before
// Synced, once a stream opened (its `[kc] +` line is in the view, and a
// crash loop that writes nothing would otherwise be explained on every
// restart), or while a source can still deliver lines: it streams,
// reconnects, waits for a slot or reads a previous instance.
func (m *Mux) explainLocked() []Line {
	if !m.synced || m.streamed.Load() {
		return nil
	}
	text := m.idleLocked()
	if text == "" || text == m.explained {
		return nil
	}
	m.explained = text
	return []Line{{Notice: true, Text: text}}
}

// idleLocked says why no selected source can deliver a line now, or ""
// while one can.
func (m *Mux) idleLocked() string {
	if len(m.candidates) == 0 {
		if m.opts.Scope == "" {
			return "no pods yet; waiting for them"
		}
		return "no pods match " + m.opts.Scope + " yet; waiting for them"
	}
	var s Stats
	for _, t := range m.candidates {
		if !m.filter.Match(t) {
			s.Excluded++
			continue
		}
		st, ok := m.streams[t]
		if !ok {
			continue
		}
		if st.state != stateHeld && st.state != stateEnded {
			return ""
		}
		s.count(m.sourceLocked(st))
	}
	if s.Excluded == len(m.candidates) {
		return fmt.Sprintf("no sources selected: the filter excludes all %d", s.Excluded)
	}
	parts := s.idleParts()
	if s.Excluded > 0 {
		parts = append(parts, fmt.Sprintf("%d excluded", s.Excluded))
	}
	return "no log lines yet: " + strings.Join(parts, ", ")
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
		// A container that waits has a log only from its last instance:
		// read it with previous. A stream that already read part of that
		// instance as the current one (Retry after a failure) resumes
		// after its last line instead, so that no line is shown twice; the
		// kubelet serves a waiting container's last instance as current.
		w, waits := m.waiting[t]
		s.previous, s.reason = waits && w.Previous && s.pos.Time.IsZero(), w.Waiting
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
	if s.previous {
		// The instance is over; its log is read once, as `kubectl logs -p`.
		opts.Previous, opts.Follow = true, false
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
	case s.previous:
		ended.Text += " (end of the previous instance)"
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
	started := "+ " + t.Name()
	if s.previous {
		started += ": previous instance (container waiting: " + s.reason + ")"
	}
	cfg := logresume.Config{
		Options: *opts,
		Open: func(ctx context.Context, o *corev1.PodLogOptions) (io.ReadCloser, error) {
			rc, err := m.open(ctx, t, o)
			if err == nil && !opened {
				opened = true
				m.streamed.Store(true)
				m.emit([]Line{{Notice: true, Text: started}})
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
