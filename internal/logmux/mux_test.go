package logmux_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/logmux"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// fakeStream is a log stream the test feeds line by line.
type fakeStream struct {
	lines  chan string
	closed chan struct{}
	once   sync.Once
	buf    []byte
	onDone func()
}

func newFakeStream(onDone func()) *fakeStream {
	return &fakeStream{lines: make(chan string), closed: make(chan struct{}), onDone: onDone}
}

func (s *fakeStream) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		select {
		case l, ok := <-s.lines:
			if !ok {
				return 0, io.EOF
			}
			s.buf = []byte(l + "\n")
		case <-s.closed:
			return 0, io.ErrClosedPipe
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *fakeStream) Close() error {
	s.once.Do(func() {
		close(s.closed)
		if s.onDone != nil {
			s.onDone()
		}
	})
	return nil
}

// fakeOpener records the streams the mux opens and how many are open.
type fakeOpener struct {
	mu        sync.Mutex
	streams   map[string]*fakeStream
	opts      map[string]corev1.PodLogOptions
	ctxs      map[string]context.Context
	opens     []string
	fail      map[string]error
	active    int
	maxActive int
}

func newFakeOpener() *fakeOpener {
	return &fakeOpener{
		streams: map[string]*fakeStream{},
		opts:    map[string]corev1.PodLogOptions{},
		ctxs:    map[string]context.Context{},
		fail:    map[string]error{},
	}
}

func (f *fakeOpener) open(ctx context.Context, t logmux.Target, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := t.Name()
	f.opens = append(f.opens, name)
	f.opts[name] = *opts
	f.ctxs[name] = ctx
	if err := f.fail[name]; err != nil {
		return nil, err
	}
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	s := newFakeStream(func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	})
	f.streams[name] = s
	return s, nil
}

func (f *fakeOpener) stream(t *testing.T, name string) *fakeStream {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.streams[name]
	if !ok {
		t.Fatalf("stream %s was not opened; opened: %v", name, f.opens)
	}
	return s
}

func (f *fakeOpener) ctx(name string) context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxs[name]
}

func (f *fakeOpener) optsFor(name string) corev1.PodLogOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opts[name]
}

func (f *fakeOpener) counts() (active, maxActive int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, f.maxActive
}

func (f *fakeOpener) opened() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.opens)
}

func target(pod, container string) logmux.Target {
	return logmux.Target{Namespace: "web", Pod: pod, Container: container, PodUID: types.UID("uid-" + pod)}
}

// readLine reads the next line from the merged stream or fails after a
// timeout. The timeout is fake time inside a synctest bubble.
func readLine(t *testing.T, lines <-chan logmux.Line) logmux.Line {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("merged stream closed while waiting for a line")
		}
		return l
	case <-time.After(10 * time.Second):
		t.Fatal("no line from the merged stream within 10s")
	}
	return logmux.Line{}
}

func TestMuxMergesLinesWithPrefix(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		tail := int64(200)
		m := logmux.New(t.Context(), op.open, logmux.Options{LogOptions: corev1.PodLogOptions{Follow: true, TailLines: &tail}})
		defer m.Close()
		r := bufio.NewReader(logmux.NewReader(m, func(l logmux.Line) string { return logmux.Format(l, false) }))

		m.SetTargets([]logmux.Target{target("api-1", "app"), target("api-2", "app")})
		synctest.Wait()

		got := map[string]bool{}
		for range 2 {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			got[strings.TrimSpace(line)] = true
		}
		for _, want := range []string{"[kc] + api-1/app", "[kc] + api-2/app"} {
			if !got[want] {
				t.Fatalf("start notices %v, want %q", got, want)
			}
		}

		a, b := op.stream(t, "api-1/app"), op.stream(t, "api-2/app")
		// Lines of different streams merge in arrival order.
		a.lines <- "first from a"
		synctest.Wait()
		b.lines <- "first from b"
		synctest.Wait()
		a.lines <- "second from a"

		want := []string{
			"[api-1/app] first from a",
			"[api-2/app] first from b",
			"[api-1/app] second from a",
		}
		for _, w := range want {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got := strings.TrimRight(line, "\n"); got != w {
				t.Fatalf("line = %q, want %q", got, w)
			}
		}

		for name, opts := range map[string]corev1.PodLogOptions{"api-1/app": op.optsFor("api-1/app"), "api-2/app": op.optsFor("api-2/app")} {
			if opts.Container != "app" || !opts.Follow || opts.TailLines == nil || *opts.TailLines != 200 {
				t.Fatalf("%s opened with %+v, want container app, follow, tail 200", name, opts)
			}
		}

		close(a.lines)
		synctest.Wait()
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if got := strings.TrimRight(line, "\n"); got != "[kc] - api-1/app" {
			t.Fatalf("end notice = %q, want %q", got, "[kc] - api-1/app")
		}
	})
}

func TestFormatColorIsStablePerTarget(t *testing.T) {
	t.Parallel()
	a := logmux.Line{Target: target("api-1", "app"), Text: "hello"}
	colored := logmux.Format(a, true)
	if colored == logmux.Format(a, false) {
		t.Fatalf("colored line %q has no styling", colored)
	}
	if got := ansi.Strip(colored); got != "[api-1/app] hello" {
		t.Fatalf("stripped colored line = %q, want the plain line", got)
	}
	again := a
	again.Target.Restarts = 3
	again.Target.PodUID = "other"
	if logmux.Color(again.Target) != logmux.Color(a.Target) {
		t.Fatal("a restarted container changed its prefix color")
	}

	// The color depends on the name: over a few names, more than one color
	// shows up, so the hash is not constant.
	colors := map[string]bool{}
	for i := range 20 {
		colors[logmux.Color(target(fmt.Sprintf("api-%d", i), "app"))] = true
	}
	if len(colors) < 2 {
		t.Fatalf("20 targets share %d color(s)", len(colors))
	}

	notice := logmux.Format(logmux.Line{Notice: true, Text: "+ api-1/app"}, true)
	if notice != "[kc] + api-1/app" {
		t.Fatalf("notice = %q", notice)
	}
}

func TestMuxLimitsConcurrentStreams(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m := logmux.New(t.Context(), op.open, logmux.Options{MaxStreams: 2})
		defer m.Close()
		var notices []string
		done := make(chan struct{})
		go func() {
			defer close(done)
			for l := range m.Lines() {
				if l.Notice {
					notices = append(notices, l.Text)
				}
			}
		}()

		m.SetTargets([]logmux.Target{
			target("p-5", "app"), target("p-1", "app"), target("p-3", "app"), target("p-2", "app"), target("p-4", "app"),
		})
		synctest.Wait()

		if got, want := op.opened(), []string{"p-1/app", "p-2/app"}; !slices.Equal(sorted(got), want) {
			t.Fatalf("opened %v, want the first two by name %v", got, want)
		}
		if s := m.Stats(); s.Running != 2 || s.Waiting != 3 {
			t.Fatalf("stats = %+v, want 2 running and 3 waiting", s)
		}

		// A stream that ends frees its slot for the next target in order.
		close(op.stream(t, "p-1/app").lines)
		synctest.Wait()
		if got := op.opened(); len(got) != 3 || got[2] != "p-3/app" {
			t.Fatalf("opened %v, want p-3/app third", got)
		}
		// The ended target stays in the set and is not reopened.
		m.SetTargets([]logmux.Target{
			target("p-1", "app"), target("p-2", "app"), target("p-3", "app"), target("p-4", "app"), target("p-5", "app"),
		})
		synctest.Wait()
		if got := op.opened(); len(got) != 3 {
			t.Fatalf("opened %v after an unchanged set, want no new stream", got)
		}
		if s := m.Stats(); s.Running != 2 || s.Waiting != 2 || s.Ended != 1 {
			t.Fatalf("stats = %+v, want 2 running, 2 waiting, 1 ended", s)
		}

		m.Close()
		<-done
		if _, maxActive := op.counts(); maxActive != 2 {
			t.Fatalf("at most %d streams were open at once, want 2", maxActive)
		}
		if !slices.Contains(notices, "3 streams wait for a free slot: at most 2 run at once") {
			t.Fatalf("notices %q lack the waiting notice", notices)
		}
	})
}

func TestMuxDefaultLimit(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m := logmux.New(t.Context(), op.open, logmux.Options{})
		defer m.Close()
		go func() {
			for range m.Lines() {
			}
		}()
		var ts []logmux.Target
		for i := range logmux.DefaultMaxStreams + 3 {
			ts = append(ts, target(fmt.Sprintf("p-%03d", i), "app"))
		}
		m.SetTargets(ts)
		synctest.Wait()
		if s := m.Stats(); s.Running != logmux.DefaultMaxStreams || s.Waiting != 3 {
			t.Fatalf("stats = %+v, want %d running and 3 waiting", s, logmux.DefaultMaxStreams)
		}
	})
}

func TestMuxTargetSetChanges(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m := logmux.New(t.Context(), op.open, logmux.Options{MaxStreams: 1, RemoveGrace: 5 * time.Second})
		defer m.Close()
		go func() {
			for range m.Lines() {
			}
		}()

		m.SetTargets([]logmux.Target{target("a", "app"), target("b", "app")})
		synctest.Wait()
		if got := op.opened(); !slices.Equal(got, []string{"a/app"}) {
			t.Fatalf("opened %v, want only a/app under limit 1", got)
		}

		// b is dropped from the queue and never opened; a keeps running
		// through the grace and is re-added in time.
		m.SetTargets(nil)
		synctest.Wait()
		time.Sleep(4 * time.Second)
		m.SetTargets([]logmux.Target{target("a", "app")})
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if ctx := op.ctx("a/app"); ctx.Err() != nil {
			t.Fatal("a/app was canceled although it was re-added within the grace")
		}
		if got := op.opened(); !slices.Equal(got, []string{"a/app"}) {
			t.Fatalf("opened %v, want b/app never opened", got)
		}

		// Removed for good: the stream stops after the grace, not before.
		m.SetTargets([]logmux.Target{target("c", "app")})
		synctest.Wait()
		time.Sleep(4 * time.Second)
		synctest.Wait()
		if op.ctx("a/app").Err() != nil {
			t.Fatal("a/app was canceled before the grace ran out")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if op.ctx("a/app").Err() == nil {
			t.Fatal("a/app still runs after the grace")
		}
		// Its slot went to c.
		if got := op.opened(); !slices.Equal(got, []string{"a/app", "c/app"}) {
			t.Fatalf("opened %v, want c/app after a/app stopped", got)
		}

		// A restarted container is a new target with its own stream.
		restarted := target("c", "app")
		restarted.Restarts = 1
		m.SetTargets([]logmux.Target{restarted})
		close(op.stream(t, "c/app").lines)
		synctest.Wait()
		if got := op.opened(); !slices.Equal(got, []string{"a/app", "c/app", "c/app"}) {
			t.Fatalf("opened %v, want a second c/app stream for the restart", got)
		}
	})
}

func TestMuxOpenFailureIsANotice(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		op.fail["a/app"] = errors.New("container is waiting to start")
		m := logmux.New(t.Context(), op.open, logmux.Options{MaxStreams: 1})
		defer m.Close()
		m.SetTargets([]logmux.Target{target("a", "app"), target("b", "app")})
		synctest.Wait()

		var got []string
		for range 3 {
			got = append(got, logmux.Format(readLine(t, m.Lines()), false))
		}
		// The waiting notice comes from SetTargets and races with the
		// stream goroutines; the end of a comes before the start of b.
		want := []string{
			"[kc] - a/app: container is waiting to start",
			"[kc] + b/app",
			"[kc] 1 stream waits for a free slot: at most 1 run at once",
		}
		if !slices.Equal(sorted(got), sorted(want)) {
			t.Fatalf("lines = %q, want %q", got, want)
		}
		if slices.Index(got, want[0]) > slices.Index(got, want[1]) {
			t.Fatalf("lines = %q: b/app started before a/app ended", got)
		}
	})
}

func TestMuxCloseCancelsEverything(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m := logmux.New(t.Context(), op.open, logmux.Options{MaxStreams: 2})
		r := logmux.NewReader(m, func(l logmux.Line) string { return logmux.Format(l, false) })
		m.SetTargets([]logmux.Target{target("a", "app"), target("b", "app"), target("c", "app")})
		synctest.Wait()

		readErr := make(chan error, 1)
		go func() {
			_, err := io.ReadAll(r)
			readErr <- err
		}()
		synctest.Wait()

		if err := r.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		m.Wait()
		synctest.Wait()

		for _, name := range []string{"a/app", "b/app"} {
			if op.ctx(name).Err() == nil {
				t.Fatalf("%s context not canceled by Close", name)
			}
		}
		if active, _ := op.counts(); active != 0 {
			t.Fatalf("%d streams still open after Close", active)
		}
		if err := <-readErr; err != nil {
			t.Fatalf("reader ended with %v, want EOF", err)
		}
		// Whatever was buffered drains, and the channel closes.
		for range m.Lines() {
		}
		// The queued target never starts, and a new set is ignored.
		m.SetTargets([]logmux.Target{target("d", "app")})
		m.Notify("ignored")
		synctest.Wait()
		if got := op.opened(); !slices.Equal(sorted(got), []string{"a/app", "b/app"}) {
			t.Fatalf("opened %v after Close, want only a/app and b/app", got)
		}
	})
}

func TestMuxCloseWithParentContext(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		ctx, cancel := context.WithCancel(t.Context())
		m := logmux.New(ctx, op.open, logmux.Options{})
		m.SetTargets([]logmux.Target{target("a", "app")})
		synctest.Wait()
		cancel()
		m.Wait()
		if active, _ := op.counts(); active != 0 {
			t.Fatalf("%d streams open after the parent context ended", active)
		}
		m.Close()
	})
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
