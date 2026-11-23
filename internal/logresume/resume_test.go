package logresume_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/logresume"
)

// attempt is one scripted answer to an open: an error, or a body that
// returns its lines and then end (io.EOF for a clean end). A nil end blocks
// after the lines until the context ends, like a live follow.
type attempt struct {
	err   error
	lines []string
	end   error
}

// body returns its text, then end.
type body struct {
	r   io.Reader
	end error
	ctx context.Context
}

func (b *body) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 || !errors.Is(err, io.EOF) {
		return n, err
	}
	if b.end == nil {
		<-b.ctx.Done()
		return 0, b.ctx.Err()
	}
	return 0, b.end
}

func (b *body) Close() error { return nil }

// script answers opens in order and records their options.
type script struct {
	mu       sync.Mutex
	attempts []attempt
	opts     []corev1.PodLogOptions
}

func (s *script) open(ctx context.Context, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts = append(s.opts, *opts)
	if len(s.opts) > len(s.attempts) {
		return nil, errors.New("unexpected open")
	}
	a := s.attempts[len(s.opts)-1]
	if a.err != nil {
		return nil, a.err
	}
	text := ""
	if len(a.lines) > 0 {
		text = strings.Join(a.lines, "\n") + "\n"
	}
	return &body{r: strings.NewReader(text), end: a.end, ctx: ctx}, nil
}

func (s *script) opens() []corev1.PodLogOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]corev1.PodLogOptions(nil), s.opts...)
}

// recorder collects what Run delivers.
type recorder struct {
	mu     sync.Mutex
	out    []string
	states []logresume.State
}

func (r *recorder) sink() logresume.Sink {
	return logresume.Sink{
		Line: func(s string) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.out = append(r.out, s)
			return true
		},
		Notice: func(s string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.out = append(r.out, "notice: "+s)
		},
		State: func(s logresume.State) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.states = append(r.states, s)
		},
	}
}

func (r *recorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.out...)
}

// running reports the same instance as running.
func running(context.Context) (logresume.Instance, bool) {
	return logresume.Instance{PodUID: "uid-1", Restarts: 2, Running: true}, true
}

// stopsAfter reports the instance as running for n checks, then as
// terminated.
func stopsAfter(n int) logresume.Checker {
	var mu sync.Mutex
	calls := 0
	return func(ctx context.Context) (logresume.Instance, bool) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls > n {
			return logresume.Instance{PodUID: "uid-1", Restarts: 2}, true
		}
		return running(ctx)
	}
}

var follow = corev1.PodLogOptions{Follow: true, Container: "app"}

const (
	t1 = "2026-11-22T10:00:01.100000000Z"
	t2 = "2026-11-22T10:00:01.200000000Z"
	t3 = "2026-11-22T10:00:02.000000000Z"
)

func equal(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestResumeAfterAFailureSkipsWhatWasDelivered(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := &script{attempts: []attempt{
			// Two identical lines at t2: both are real records.
			{lines: []string{t1 + " a", t2 + " same", t2 + " same"}, end: io.ErrUnexpectedEOF},
			// The new stream starts at the second of t2 and repeats
			// what was delivered; the third "same" at t2 is new.
			{lines: []string{t1 + " a", t2 + " same", t2 + " same", t2 + " same", t3 + " b"}, end: io.EOF},
		}}
		rec := &recorder{}
		err := logresume.Run(t.Context(), logresume.Config{Options: follow, Open: s.open, Check: stopsAfter(3)}, rec.sink())
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
		equal(t, rec.lines(), []string{
			"a", "same", "same",
			"notice: reconnecting in 1s: unexpected EOF",
			"notice: reconnected",
			"same", "b",
		})
		opens := s.opens()
		if !opens[0].Timestamps || opens[0].SinceTime != nil {
			t.Fatalf("first open = %+v, want timestamps and no sinceTime", opens[0])
		}
		if got := opens[1].SinceTime; got == nil || !got.Time.Equal(time.Date(2026, 11, 22, 10, 0, 1, 0, time.UTC)) || opens[1].TailLines != nil {
			t.Fatalf("reopen = %+v, want sinceTime 10:00:01 and no tail", opens[1])
		}
	})
}

func TestResumeMarksAPossibleGap(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := &script{attempts: []attempt{
			{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF},
			// The log was rotated: the stream starts after the cursor.
			{lines: []string{t3 + " b"}},
		}}
		rec := &recorder{}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() {
			done <- logresume.Run(ctx, logresume.Config{Options: follow, Open: s.open, Check: running}, rec.sink())
		}()
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
		equal(t, rec.lines(), []string{
			"a",
			"notice: reconnecting in 1s: unexpected EOF",
			"notice: reconnected",
			"notice: lines after 2026-11-22T10:00:01.1Z may be missing",
			"b",
		})
	})
}

func TestResumeEndsWhenTheContainerStopped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		check logresume.Checker
		end   error
		want  []string
	}{
		{name: "clean end of a terminated container", end: io.EOF,
			check: func(context.Context) (logresume.Instance, bool) {
				return logresume.Instance{PodUID: "uid-1", Restarts: 2}, true
			}, want: []string{"a"}},
		{name: "failure after a restart", end: io.ErrUnexpectedEOF,
			check: func(context.Context) (logresume.Instance, bool) {
				return logresume.Instance{PodUID: "uid-1", Restarts: 3, Running: true}, true
			}, want: []string{"a"}},
		{name: "failure after the pod was recreated", end: io.ErrUnexpectedEOF,
			check: func(context.Context) (logresume.Instance, bool) {
				return logresume.Instance{PodUID: "uid-2", Restarts: 2, Running: true}, true
			}, want: []string{"a"}},
		{name: "pod gone", end: io.ErrUnexpectedEOF,
			check: func(context.Context) (logresume.Instance, bool) { return logresume.Instance{}, true },
			want:  []string{"a"}},
		{name: "clean end without a check is the end", end: io.EOF, want: []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				s := &script{attempts: []attempt{{lines: []string{t1 + " a"}, end: tt.end}}}
				rec := &recorder{}
				start := logresume.Instance{PodUID: "uid-1", Restarts: 2, Running: true}
				err := logresume.Run(t.Context(), logresume.Config{Options: follow, Open: s.open, Check: tt.check, Start: start}, rec.sink())
				if err != nil {
					t.Fatalf("Run = %v", err)
				}
				equal(t, rec.lines(), tt.want)
				if n := len(s.opens()); n != 1 {
					t.Fatalf("opened %d times, want once", n)
				}
			})
		})
	}
}

func TestResumeStopsOnAPermanentError(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "p", errors.New("no"))
		s := &script{attempts: []attempt{
			{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF},
			{err: errors.New("connection refused")},
			{err: forbidden},
		}}
		rec := &recorder{}
		err := logresume.Run(t.Context(), logresume.Config{Options: follow, Open: s.open, Check: running}, rec.sink())
		if !apierrors.IsForbidden(err) {
			t.Fatalf("Run = %v, want the forbidden error", err)
		}
		equal(t, rec.lines(), []string{
			"a",
			"notice: reconnecting in 1s: unexpected EOF",
			"notice: reconnecting in 2s: connection refused",
		})
	})
}

func TestResumeGivesUp(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		attempts := []attempt{{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF}}
		for range 3 {
			attempts = append(attempts, attempt{err: errors.New("connection refused")})
		}
		s := &script{attempts: attempts}
		rec := &recorder{}
		start := time.Now()
		err := logresume.Run(t.Context(), logresume.Config{Options: follow, Open: s.open, Check: running, MaxAttempts: 3, MaxDelay: 3 * time.Second}, rec.sink())
		if !errors.Is(err, logresume.ErrGaveUp) || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("Run = %v, want ErrGaveUp with the last error", err)
		}
		// Waits of 1s, 2s and 3s (capped).
		if got := time.Since(start); got != 6*time.Second {
			t.Fatalf("gave up after %s, want 6s", got)
		}
		if n := len(s.opens()); n != 4 {
			t.Fatalf("opened %d times, want 4", n)
		}
	})
}

func TestResumeCancelStopsTheRetry(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := &script{attempts: []attempt{{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF}}}
		rec := &recorder{}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() {
			done <- logresume.Run(ctx, logresume.Config{Options: follow, Open: s.open, Check: running}, rec.sink())
		}()
		synctest.Wait() // waiting for the first retry
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
		if n := len(s.opens()); n != 1 {
			t.Fatalf("opened %d times after cancel, want 1", n)
		}
	})
}

func TestFiniteReadsAreNotResumed(t *testing.T) {
	t.Parallel()
	for _, opts := range []corev1.PodLogOptions{{Follow: false}, {Follow: true, Previous: true}} {
		synctest.Test(t, func(t *testing.T) {
			s := &script{attempts: []attempt{{lines: []string{"x"}, end: io.ErrUnexpectedEOF}}}
			rec := &recorder{}
			err := logresume.Run(t.Context(), logresume.Config{Options: opts, Open: s.open, Check: running}, rec.sink())
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("Run(%+v) = %v, want the read error", opts, err)
			}
			equal(t, rec.lines(), []string{"x"})
			if o := s.opens()[0]; o.Timestamps {
				t.Fatalf("a finite read must keep the user's options: %+v", o)
			}
		})
	}
}

func TestUserTimestampsAreKept(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		opts := follow
		opts.Timestamps = true
		s := &script{attempts: []attempt{{lines: []string{t1 + " a"}, end: io.EOF}}}
		rec := &recorder{}
		if err := logresume.Run(t.Context(), logresume.Config{Options: opts, Open: s.open}, rec.sink()); err != nil {
			t.Fatalf("Run = %v", err)
		}
		equal(t, rec.lines(), []string{t1 + " a"})
	})
}

func TestReaderWritesNoticesAndReportsState(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := &script{attempts: []attempt{
			{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF},
			{lines: []string{t1 + " a", t3 + " b"}, end: io.EOF},
		}}
		r, err := logresume.Open(t.Context(), logresume.Config{Options: follow, Open: s.open, Check: stopsAfter(3)})
		if err != nil {
			t.Fatalf("Open = %v", err)
		}
		defer func() { _ = r.Close() }()
		br := bufio.NewReader(r)
		if line, err := br.ReadString('\n'); line != "a\n" || err != nil {
			t.Fatalf("first line = %q, %v", line, err)
		}
		synctest.Wait()
		if !r.Reconnecting() {
			t.Fatal("Reconnecting() = false while waiting to reconnect")
		}
		data, err := io.ReadAll(br)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if want := "[kc] reconnecting in 1s: unexpected EOF\n[kc] reconnected\nb\n"; string(data) != want {
			t.Fatalf("read %q, want %q", data, want)
		}
		<-r.Done()
		if r.Reconnecting() {
			t.Fatal("Reconnecting() = true after the stream resumed")
		}
	})
}

func TestOpenReturnsTheFirstError(t *testing.T) {
	t.Parallel()
	s := &script{attempts: []attempt{{err: errors.New("boom")}}}
	if _, err := logresume.Open(t.Context(), logresume.Config{Options: follow, Open: s.open}); err == nil || err.Error() != "boom" {
		t.Fatalf("Open = %v, want boom", err)
	}
}

// mutableCheck is a Checker whose answer a test changes while Run waits.
type mutableCheck struct {
	mu   sync.Mutex
	inst logresume.Instance
	ok   bool
}

func (m *mutableCheck) check(context.Context) (logresume.Instance, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inst, m.ok
}

func (m *mutableCheck) set(inst logresume.Instance, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inst, m.ok = inst, ok
}

// TestResumeRechecksTheInstanceAfterTheWait changes the container's state
// while Run waits to reconnect. A restart, a new pod or a stop known by
// then must end the stream instead of reopening it, because the reopened
// stream would be the next instance's log. An unknown state still
// reconnects.
func TestResumeRechecksTheInstanceAfterTheWait(t *testing.T) {
	t.Parallel()
	start := logresume.Instance{PodUID: "uid-A", Restarts: 2, Running: true}
	tests := []struct {
		name  string
		after logresume.Instance
		ok    bool
		want  []string
	}{
		{name: "restart count changed", after: logresume.Instance{PodUID: "uid-A", Restarts: 3, Running: true}, ok: true,
			want: []string{"old", "notice: reconnecting in 1s: unexpected EOF", "notice: not reconnecting: the container restarted"}},
		{name: "pod replaced", after: logresume.Instance{PodUID: "uid-B", Restarts: 2, Running: true}, ok: true,
			want: []string{"old", "notice: reconnecting in 1s: unexpected EOF", "notice: not reconnecting: the pod was replaced"}},
		{name: "container terminated", after: logresume.Instance{PodUID: "uid-A", Restarts: 2}, ok: true,
			want: []string{"old", "notice: reconnecting in 1s: unexpected EOF", "notice: not reconnecting: the container stopped"}},
		{name: "pod gone", ok: true,
			want: []string{"old", "notice: reconnecting in 1s: unexpected EOF", "notice: not reconnecting: the pod or container is gone"}},
		{name: "unknown state still reconnects",
			want: []string{"old", "notice: reconnecting in 1s: unexpected EOF", "notice: reconnected", "NEW"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				s := &script{attempts: []attempt{
					{lines: []string{t1 + " old"}, end: io.ErrUnexpectedEOF},
					{lines: []string{t1 + " old", t3 + " NEW"}, end: io.EOF},
				}}
				chk := &mutableCheck{inst: start, ok: true}
				rec := &recorder{}
				sink := rec.sink()
				notice := sink.Notice
				sink.Notice = func(text string) {
					notice(text)
					if strings.HasPrefix(text, "reconnecting") {
						// Known before the timer fires.
						chk.set(tt.after, tt.ok)
					}
				}
				line := sink.Line
				sink.Line = func(text string) bool {
					line(text)
					return text != "NEW"
				}
				err := logresume.Run(t.Context(), logresume.Config{Options: follow, Open: s.open, Check: chk.check, Start: start}, sink)
				if err != nil {
					t.Fatalf("Run = %v", err)
				}
				equal(t, rec.lines(), tt.want)
				wantOpens := 1
				if !tt.ok {
					wantOpens = 2
				}
				if n := len(s.opens()); n != wantOpens {
					t.Fatalf("opened %d times, want %d", n, wantOpens)
				}
			})
		})
	}
}

// TestResumeSameInstanceAfterTheWaitResumes keeps the instance unchanged
// over the wait: the stream reopens and skips what was delivered.
func TestResumeSameInstanceAfterTheWaitResumes(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		start := logresume.Instance{PodUID: "uid-A", Restarts: 2, Running: true}
		s := &script{attempts: []attempt{
			{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF},
			{lines: []string{t1 + " a", t3 + " b"}, end: io.EOF},
		}}
		chk := &mutableCheck{inst: start, ok: true}
		rec := &recorder{}
		sink := rec.sink()
		line := sink.Line
		sink.Line = func(text string) bool {
			line(text)
			return text != "b"
		}
		if err := logresume.Run(t.Context(), logresume.Config{Options: follow, Open: s.open, Check: chk.check, Start: start}, sink); err != nil {
			t.Fatalf("Run = %v", err)
		}
		equal(t, rec.lines(), []string{"a", "notice: reconnecting in 1s: unexpected EOF", "notice: reconnected", "b"})
	})
}

// TestReaderClearsReconnectingWhenItStops ends a Reader while it waits to
// reconnect: a refusal, the last failed attempt, or a close. Nothing is
// pending after that, so Reconnecting must be false.
func TestReaderClearsReconnectingWhenItStops(t *testing.T) {
	t.Parallel()
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "p", errors.New("no"))
	tests := []struct {
		name     string
		attempts []attempt
		max      int
		close    bool
		wantErr  func(error) bool
	}{
		{name: "permanent refusal", attempts: []attempt{{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF}, {err: forbidden}},
			wantErr: apierrors.IsForbidden},
		{name: "attempts run out", attempts: []attempt{{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF}, {err: errors.New("connection refused")}},
			max: 1, wantErr: func(err error) bool { return errors.Is(err, logresume.ErrGaveUp) }},
		{name: "closed while waiting", attempts: []attempt{{lines: []string{t1 + " a"}, end: io.ErrUnexpectedEOF}},
			close: true, wantErr: func(err error) bool { return err != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				s := &script{attempts: tt.attempts}
				r, err := logresume.Open(t.Context(), logresume.Config{Options: follow, Open: s.open, Check: running, MaxAttempts: tt.max})
				if err != nil {
					t.Fatalf("Open = %v", err)
				}
				defer func() { _ = r.Close() }()
				br := bufio.NewReader(r)
				if line, err := br.ReadString('\n'); line != "a\n" || err != nil {
					t.Fatalf("first line = %q, %v", line, err)
				}
				synctest.Wait()
				if !r.Reconnecting() {
					t.Fatal("Reconnecting() = false while a retry is pending")
				}
				if tt.close {
					_ = r.Close()
				}
				_, err = io.ReadAll(br)
				if !tt.wantErr(err) {
					t.Fatalf("read error = %v", err)
				}
				<-r.Done()
				if r.Reconnecting() {
					t.Fatal("Reconnecting() = true after the reader stopped")
				}
			})
		})
	}
}
