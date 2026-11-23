// Package logresume follows a container log across connection failures
// (see docs/adr/0180-resume-live-logs.md).
//
// A followed log stream can end while the container still runs: the API
// server or the kubelet restarts, a proxy drops the connection, the network
// fails. Run reopens such a stream where it stopped. It asks the kubelet for
// timestamps, so every line carries the time the container wrote it, and
// reopens with sinceTime set to the second of the last line it delivered.
// Lines of that second that were already delivered are skipped by count,
// so repeated lines and lines that share a timestamp are kept.
//
// Run reconnects only while the same container instance still runs. A
// container that terminated or restarted, or a pod that was recreated, ends
// the stream as before: its next instance is a new stream. A request the
// server refuses (authorization, a missing pod or container) is not
// retried.
package logresume

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/logview"
)

// Instance is the state of the streamed container as the cache sees it.
type Instance struct {
	PodUID   types.UID
	Restarts int32
	// Running is true while the container runs.
	Running bool
}

// Opener opens the log stream with opts. The stream must end when ctx is
// canceled.
type Opener func(ctx context.Context, opts *corev1.PodLogOptions) (io.ReadCloser, error)

// Checker returns the current state of the streamed container. ok is false
// when the state is not known; a pod or container that is known to be gone
// returns ok with a zero Instance.
type Checker func(ctx context.Context) (inst Instance, ok bool)

// Default retry settings.
const (
	DefaultFirstDelay  = time.Second
	DefaultMaxDelay    = 30 * time.Second
	DefaultMaxAttempts = 8
)

// Config configures Run.
type Config struct {
	// Options are the user's stream options. Only a followed stream of the
	// current instance (Follow, not Previous) is resumed.
	Options corev1.PodLogOptions
	Open    Opener
	// Check, when set, says whether the container still runs. Without it a
	// stream that ends cleanly is the end of the log, and only failed
	// streams are reopened.
	Check Checker
	// Start is the container instance the stream belongs to. When it is
	// zero, the first Check result is used.
	Start Instance
	// Position, when not nil, is where a resumed stream continues: the
	// first open asks for the lines after it, as a reconnect does. Run
	// keeps it at the last delivered line, so a later Run with the same
	// Position continues without repeating lines.
	Position *Position
	// FirstDelay, MaxDelay and MaxAttempts bound the retries: the delay
	// doubles from FirstDelay up to MaxDelay, and after MaxAttempts failed
	// attempts in a row Run gives up. Zero means the default.
	FirstDelay  time.Duration
	MaxDelay    time.Duration
	MaxAttempts int
}

func (c Config) firstDelay() time.Duration {
	if c.FirstDelay <= 0 {
		return DefaultFirstDelay
	}
	return c.FirstDelay
}

func (c Config) maxDelay() time.Duration {
	if c.MaxDelay <= 0 {
		return DefaultMaxDelay
	}
	return c.MaxDelay
}

func (c Config) maxAttempts() int {
	if c.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return c.MaxAttempts
}

// Position is the kubelet time of the last delivered line and the number
// of delivered lines with exactly that time. The zero value is the start
// of the stream.
type Position struct {
	Time time.Time
	Seen int
}

// Resumable reports whether a stream with opts is resumed: a followed
// stream of the current container instance.
func Resumable(opts corev1.PodLogOptions) bool { return opts.Follow && !opts.Previous }

// State is the connection state of a stream.
type State int

const (
	// StateStreaming reads the stream.
	StateStreaming State = iota
	// StateReconnecting waits for the next attempt or opens the stream
	// again.
	StateReconnecting
)

// Sink receives what Run reads.
type Sink struct {
	// Line receives one log line without the line break, as the user's
	// options show it. It returns false to stop Run.
	Line func(text string) bool
	// Notice receives a message about the connection, for example
	// "reconnecting in 2s: unexpected EOF". It may be nil.
	Notice func(text string)
	// State receives every change of the connection state. It may be nil.
	State func(State)
}

// ErrGaveUp wraps the last error when MaxAttempts attempts in a row failed.
var ErrGaveUp = errors.New("gave up reconnecting")

// Run reads the log until it ends, ctx ends or Sink.Line returns false. It
// returns nil when the log ended (the container stopped, a finite read
// finished) or Run was stopped, and the error otherwise. The error of the
// first open is returned as it is, so callers report it as before.
func Run(ctx context.Context, cfg Config, sink Sink) error {
	return run(ctx, cfg, sink, nil)
}

// run is Run with the first stream already open when first is not nil.
func run(ctx context.Context, cfg Config, sink Sink, first io.ReadCloser) error {
	r := newResumer(cfg, sink)
	if first == nil {
		var err error
		if first, err = cfg.Open(ctx, r.options()); err != nil {
			return err
		}
	}
	if !Resumable(cfg.Options) {
		_, err := r.read(ctx, first)
		return endErr(ctx, err)
	}
	return r.run(ctx, first)
}

// endErr maps how a final read stopped to Run's result.
func endErr(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, errStopped) {
		return nil
	}
	return err
}

type resumer struct {
	cfg   Config
	sink  Sink
	start Instance

	// pos is the last delivered line.
	pos *Position
	// After a reconnect, dedupe drops the lines up to the cursor, skip of
	// them at the cursor itself, until a newer line arrives; confirm is
	// set until the first line tells whether the new stream covers the
	// interruption.
	dedupe  bool
	skip    int
	confirm bool
}

func newResumer(cfg Config, sink Sink) *resumer {
	r := &resumer{cfg: cfg, sink: sink, start: cfg.Start, pos: cfg.Position}
	if r.pos == nil {
		r.pos = &Position{}
	}
	if Resumable(cfg.Options) {
		r.resumed()
	}
	return r
}

func (r *resumer) run(ctx context.Context, body io.ReadCloser) error {
	if r.start == (Instance{}) && r.cfg.Check != nil {
		if inst, ok := r.cfg.Check(ctx); ok {
			r.start = inst
		}
	}
	failures := 0
	for {
		opened := time.Now()
		delivered, err := r.read(ctx, body)
		if ctx.Err() != nil || errors.Is(err, errStopped) {
			return nil
		}
		// A stream that brought new lines, or stayed up for the longest
		// delay, counts as recovered: the attempts start over. A stream
		// that drops at once keeps counting, so a broken connection gives
		// up instead of reconnecting forever.
		if delivered || time.Since(opened) >= r.cfg.maxDelay() {
			failures = 0
		}
		clean := errors.Is(err, io.EOF)
		if clean {
			err = errors.New("the stream ended")
		}
		if !r.running(ctx, clean) {
			return nil
		}
		body, err = r.reconnect(ctx, err, &failures)
		if body == nil {
			return err
		}
	}
}

// running decides whether to reconnect after a stream stopped. After a
// clean end it needs the cache to say the same instance still runs; after
// a failure an unknown state is enough.
func (r *resumer) running(ctx context.Context, clean bool) bool {
	if r.cfg.Check == nil {
		return !clean
	}
	inst, ok := r.cfg.Check(ctx)
	if !ok {
		return !clean
	}
	if !inst.Running {
		return false
	}
	return r.start == (Instance{}) || (inst.PodUID == r.start.PodUID && inst.Restarts == r.start.Restarts)
}

// reconnect waits and opens the stream again until it opens, the server
// refuses it, the instance stopped running or the attempts run out. It
// returns a nil body when Run ends, with the error to return.
func (r *resumer) reconnect(ctx context.Context, cause error, failures *int) (io.ReadCloser, error) {
	r.state(StateReconnecting)
	for {
		*failures++
		if *failures > r.cfg.maxAttempts() {
			return nil, fmt.Errorf("%w after %d attempts: %w", ErrGaveUp, r.cfg.maxAttempts(), cause)
		}
		delay := r.delay(*failures)
		r.notice(fmt.Sprintf("reconnecting in %s: %v", delay, cause))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil
		case <-timer.C:
		}
		body, err := r.cfg.Open(ctx, r.options())
		if ctx.Err() != nil {
			if body != nil {
				_ = body.Close()
			}
			return nil, nil
		}
		if err == nil {
			r.resumed()
			r.notice("reconnected")
			r.state(StateStreaming)
			return body, nil
		}
		if Permanent(err) {
			return nil, err
		}
		if !r.running(ctx, false) {
			return nil, nil
		}
		cause = err
	}
}

// resumed sets up a stream that continues after pos: it drops the lines
// up to pos and checks that the first new line follows on.
func (r *resumer) resumed() {
	r.dedupe, r.skip, r.confirm = !r.pos.Time.IsZero(), r.pos.Seen, !r.pos.Time.IsZero()
}

// delay is the wait before attempt n (from 1): FirstDelay doubled per
// attempt, at most MaxDelay.
func (r *resumer) delay(n int) time.Duration {
	d := r.cfg.firstDelay()
	for i := 1; i < n && d < r.cfg.maxDelay(); i++ {
		d *= 2
	}
	return min(d, r.cfg.maxDelay())
}

// Permanent reports an error that a retry cannot fix: the server refused
// the request or the pod or container does not exist.
func Permanent(err error) bool {
	return apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) || apierrors.IsNotFound(err) ||
		apierrors.IsBadRequest(err) || apierrors.IsInvalid(err) || apierrors.IsMethodNotSupported(err) ||
		apierrors.IsGone(err)
}

// options are the stream options of the next open. A resumed stream is
// always read with timestamps, and after the first line from the second of the cursor.
func (r *resumer) options() *corev1.PodLogOptions {
	opts := r.cfg.Options.DeepCopy()
	if !Resumable(r.cfg.Options) {
		return opts
	}
	opts.Timestamps = true
	if !r.pos.Time.IsZero() {
		since := metav1.NewTime(r.pos.Time.Truncate(time.Second))
		opts.SinceTime = &since
		opts.SinceSeconds = nil
		opts.TailLines = nil
		opts.LimitBytes = nil
	}
	return opts
}

var errStopped = errors.New("stopped")

// read delivers the lines of body and returns why it stopped: io.EOF for a
// clean end, errStopped when the sink stopped, the read error otherwise.
// delivered reports whether a new line arrived.
func (r *resumer) read(ctx context.Context, body io.ReadCloser) (delivered bool, err error) {
	defer func() { _ = body.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	br := bufio.NewReader(body)
	for {
		line, err := logview.ReadLine(br, logview.MaxLineBytes+len(time.RFC3339Nano)+1)
		if line != "" {
			if out, ok := r.accept(strings.TrimRight(line, "\r\n")); ok {
				delivered = true
				if !r.sink.Line(out) {
					return delivered, errStopped
				}
			}
		}
		if err != nil {
			return delivered, err
		}
	}
}

// accept decides whether a line is new and returns it as the user's
// options show it. Lines of a stream that is not resumed have no
// timestamps unless the user asked for them, and pass unchanged.
func (r *resumer) accept(line string) (string, bool) {
	if !Resumable(r.cfg.Options) {
		return line, true
	}
	ts, rest, ok := splitTimestamp(line)
	if !ok {
		return line, true // cannot be placed; show it
	}
	if r.confirm {
		r.confirm = false
		if ts.After(r.pos.Time) {
			r.notice("lines after " + r.pos.Time.UTC().Format(time.RFC3339Nano) + " may be missing")
		}
	}
	if r.dedupe {
		switch {
		case ts.Before(r.pos.Time):
			return "", false
		case ts.Equal(r.pos.Time) && r.skip > 0:
			r.skip--
			return "", false
		case ts.After(r.pos.Time):
			r.dedupe = false
		}
	}
	if ts.Equal(r.pos.Time) {
		r.pos.Seen++
	} else {
		*r.pos = Position{Time: ts, Seen: 1}
	}
	if r.cfg.Options.Timestamps {
		return line, true
	}
	return rest, true
}

// splitTimestamp splits "<RFC 3339 time> <text>" as the kubelet writes it
// with timestamps.
func splitTimestamp(line string) (time.Time, string, bool) {
	head, rest, ok := strings.Cut(line, " ")
	if !ok {
		head, rest = line, ""
	}
	ts, err := time.Parse(time.RFC3339Nano, head)
	if err != nil {
		return time.Time{}, line, false
	}
	return ts, rest, true
}

func (r *resumer) notice(text string) {
	if r.sink.Notice != nil {
		r.sink.Notice(text)
	}
}

func (r *resumer) state(s State) {
	if r.sink.State != nil {
		r.sink.State(s)
	}
}

// PodInstances returns the state of every container of pod that has a
// status: init, main and ephemeral containers.
func PodInstances(pod *corev1.Pod) map[string]Instance {
	out := make(map[string]Instance)
	for _, list := range [][]corev1.ContainerStatus{
		pod.Status.InitContainerStatuses,
		pod.Status.ContainerStatuses,
		pod.Status.EphemeralContainerStatuses,
	} {
		for _, st := range list {
			out[st.Name] = Instance{PodUID: pod.UID, Restarts: st.RestartCount, Running: st.State.Running != nil}
		}
	}
	return out
}
