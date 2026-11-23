package logmux_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logresume"
)

// flakyOpener answers the opens of one target in order: each body returns
// its text and then fails or blocks until canceled.
type flakyOpener struct {
	mu     sync.Mutex
	bodies []string
	fails  []bool
	opts   []corev1.PodLogOptions
}

type flakyBody struct {
	r    io.Reader
	fail bool
	ctx  context.Context
}

func (b *flakyBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 || !errors.Is(err, io.EOF) {
		return n, err
	}
	if b.fail {
		return 0, io.ErrUnexpectedEOF
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *flakyBody) Close() error { return nil }

func (o *flakyOpener) open(ctx context.Context, _ logmux.Target, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opts = append(o.opts, *opts)
	i := len(o.opts) - 1
	if i >= len(o.bodies) {
		return nil, errors.New("unexpected open")
	}
	return &flakyBody{r: strings.NewReader(o.bodies[i]), fail: o.fails[i], ctx: ctx}, nil
}

func readMerged(t *testing.T, r *bufio.Reader, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for range n {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read after %q: %v", out, err)
		}
		out = append(out, strings.TrimRight(line, "\n"))
	}
	return out
}

// TestMuxResumesAFailedStream checks that a target's stream that fails
// while its container runs reconnects from the last line, without
// repeating it, and with the reconnect shown as notices.
func TestMuxResumesAFailedStream(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := &flakyOpener{
			bodies: []string{
				"2026-11-22T10:00:01.5Z one\n",
				"2026-11-22T10:00:01.5Z one\n2026-11-22T10:00:02Z two\n",
			},
			fails: []bool{true, false},
		}
		tgt := target("api-1", "app")
		m := logmux.New(t.Context(), op.open, logmux.Options{
			LogOptions: corev1.PodLogOptions{Follow: true},
			Check: func(logmux.Target) (logresume.Instance, bool) {
				return logresume.Instance{PodUID: tgt.PodUID, Restarts: tgt.Restarts, Running: true}, true
			},
		})
		defer m.Close()
		r := bufio.NewReader(logmux.NewReader(m, func(l logmux.Line) string { return logmux.Format(l, false) }))
		m.SetTargets([]logmux.Target{tgt})

		got := readMerged(t, r, 5)
		want := []string{
			"[kc] + api-1/app",
			"[api-1/app] one",
			"[kc] ~ api-1/app: reconnecting in 1s: unexpected EOF",
			"[kc] ~ api-1/app: reconnected",
			"[api-1/app] two",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("merged =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		op.mu.Lock()
		reopen := op.opts[1]
		op.mu.Unlock()
		if reopen.SinceTime == nil || reopen.SinceTime.UTC().Format("15:04:05") != "10:00:01" || reopen.Container != "app" {
			t.Fatalf("reopen options = %+v", reopen)
		}
	})
}

// TestMuxEndsTheStreamOfAStoppedContainer checks that a failure after the
// container stopped ends the target as before.
func TestMuxEndsTheStreamOfAStoppedContainer(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := &flakyOpener{bodies: []string{"2026-11-22T10:00:01Z one\n"}, fails: []bool{true}}
		tgt := target("api-1", "app")
		m := logmux.New(t.Context(), op.open, logmux.Options{
			LogOptions: corev1.PodLogOptions{Follow: true},
			Check: func(logmux.Target) (logresume.Instance, bool) {
				return logresume.Instance{PodUID: tgt.PodUID, Restarts: tgt.Restarts}, true
			},
		})
		defer m.Close()
		r := bufio.NewReader(logmux.NewReader(m, func(l logmux.Line) string { return logmux.Format(l, false) }))
		m.SetTargets([]logmux.Target{tgt})
		got := readMerged(t, r, 3)
		if got[2] != "[kc] - api-1/app" {
			t.Fatalf("merged = %q, want the end notice", got)
		}
		synctest.Wait()
		if s := m.Stats(); s.Ended != 1 || s.Running != 0 {
			t.Fatalf("Stats() = %+v, want one ended stream", s)
		}
	})
}

func TestFollowerInstance(t *testing.T) {
	t.Parallel()
	set := &recordingSetter{}
	f := logmux.NewFollower(set, logmux.NameMatcher("web", "api-1"))
	tgt := target("api-1", "app")
	if _, ok := f.Instance(tgt); ok {
		t.Fatal("a pod the follower never saw must be unknown")
	}
	pod := &corev1.Pod{}
	pod.Namespace, pod.Name, pod.UID = "web", "api-1", tgt.PodUID
	pod.Spec.Containers = []corev1.Container{{Name: "app"}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", RestartCount: 4, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	f.OnAdd(pod, false)
	if inst, ok := f.Instance(tgt); !ok || !inst.Running || inst.Restarts != 4 || inst.PodUID != tgt.PodUID {
		t.Fatalf("Instance() = %+v, %v", inst, ok)
	}
	f.OnDelete(pod)
	if inst, ok := f.Instance(tgt); !ok || inst.Running {
		t.Fatalf("Instance() after delete = %+v, %v; want known gone", inst, ok)
	}
}
