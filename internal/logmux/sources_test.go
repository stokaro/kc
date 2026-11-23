package logmux_test

import (
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/logmux"
)

func TestParseFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		filter string
		match  []logmux.Target
		refuse []logmux.Target
		// text is the filter as String prints it.
		text string
	}{
		{
			name:  "empty selects everything",
			match: []logmux.Target{target("api-1", "app"), target("api-2", "istio-proxy")},
		},
		{
			name:   "exclude a sidecar in every pod",
			filter: "-/istio-proxy",
			match:  []logmux.Target{target("api-1", "app"), target("api-2", "app")},
			refuse: []logmux.Target{target("api-1", "istio-proxy"), target("api-2", "istio-proxy")},
			text:   "-/istio-proxy",
		},
		{
			name:   "a subset of pods",
			filter: "api-1 api-3",
			match:  []logmux.Target{target("api-1", "app"), target("api-3", "istio-proxy")},
			refuse: []logmux.Target{target("api-2", "app"), target("api-10", "app")},
			text:   "api-1 api-3",
		},
		{
			name:   "globs on both parts, exclude wins",
			filter: "api-*/app* -api-2",
			match:  []logmux.Target{target("api-1", "app"), target("api-3", "app-init")},
			refuse: []logmux.Target{target("api-2", "app"), target("api-1", "proxy"), target("web-1", "app")},
			text:   "api-*/app* -api-2",
		},
		{
			name:   "exclude only keeps the rest",
			filter: "-api-2/app",
			match:  []logmux.Target{target("api-2", "proxy"), target("api-1", "app")},
			refuse: []logmux.Target{target("api-2", "app")},
			text:   "-api-2/app",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := logmux.ParseFilter(tt.filter)
			if err != nil {
				t.Fatalf("ParseFilter(%q): %v", tt.filter, err)
			}
			for _, tg := range tt.match {
				if !f.Match(tg) {
					t.Errorf("%q does not match %s", tt.filter, tg.Name())
				}
			}
			for _, tg := range tt.refuse {
				if f.Match(tg) {
					t.Errorf("%q matches %s", tt.filter, tg.Name())
				}
			}
			if got := f.String(); got != tt.text {
				t.Errorf("String() = %q, want %q", got, tt.text)
			}
			if f.IsZero() != (tt.text == "") {
				t.Errorf("IsZero() = %v for %q", f.IsZero(), tt.filter)
			}
		})
	}
}

func TestParseFilterRefusesBadTerms(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"-", "/", "a/b/c", "api-[", "-/[x"} {
		if _, err := logmux.ParseFilter(s); err == nil {
			t.Errorf("ParseFilter(%q) accepted a bad term", s)
		}
	}
}

func drain(m *logmux.Mux) {
	go func() {
		for range m.Lines() {
		}
	}()
}

func mustFilter(t *testing.T, s string) logmux.Filter {
	t.Helper()
	f, err := logmux.ParseFilter(s)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func sourceStates(m *logmux.Mux) map[string]logmux.SourceState {
	out := make(map[string]logmux.SourceState)
	for _, s := range m.Sources() {
		out[s.Target.Name()] = s.State
	}
	return out
}

// TestMuxFilterExcludesASidecarAcrossPods checks that excluding a
// container stops its streams at once and leaves the other streams
// running, that a new pod gets the exclusion too, and that a source
// selected again streams only new lines.
func TestMuxFilterExcludesASidecarAcrossPods(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		tail := int64(100)
		m := logmux.New(t.Context(), op.open, logmux.Options{
			LogOptions:  corev1.PodLogOptions{Follow: true, TailLines: &tail},
			RemoveGrace: time.Minute,
		})
		defer m.Close()
		drain(m)
		m.SetTargets([]logmux.Target{target("a", "app"), target("a", "proxy"), target("b", "app"), target("b", "proxy")})
		synctest.Wait()

		m.SetFilter(mustFilter(t, "-/proxy"))
		synctest.Wait()
		for _, name := range []string{"a/proxy", "b/proxy"} {
			if op.ctx(name).Err() == nil {
				t.Errorf("%s still streams after it was excluded", name)
			}
		}
		for _, name := range []string{"a/app", "b/app"} {
			if op.ctx(name).Err() != nil {
				t.Errorf("%s was stopped although the filter keeps it", name)
			}
		}
		if s := m.Stats(); s.Running != 2 || s.Excluded != 2 || s.Incomplete() {
			t.Fatalf("Stats() = %+v, want 2 running, 2 excluded, complete", s)
		}

		// A new pod: its app streams, its proxy stays excluded.
		m.SetTargets([]logmux.Target{
			target("a", "app"), target("a", "proxy"), target("b", "app"), target("b", "proxy"),
			target("c", "app"), target("c", "proxy"),
		})
		synctest.Wait()
		if got, want := sorted(op.opened()), []string{"a/app", "a/proxy", "b/app", "b/proxy", "c/app"}; !slices.Equal(got, want) {
			t.Fatalf("opened %v, want %v", got, want)
		}
		states := sourceStates(m)
		if states["c/proxy"] != logmux.SourceExcluded || states["c/app"] != logmux.SourceStreaming {
			t.Fatalf("sources = %v", states)
		}

		// Selected again: only new lines, and the app streams stay as they are.
		m.SetFilter(logmux.Filter{})
		synctest.Wait()
		opts := op.optsFor("a/proxy")
		if opts.TailLines == nil || *opts.TailLines != 0 || opts.SinceSeconds != nil {
			t.Fatalf("a/proxy reopened with %+v, want tail 0", opts)
		}
		if opts := op.optsFor("c/proxy"); opts.TailLines == nil || *opts.TailLines != tail {
			t.Fatalf("c/proxy, never streamed, opened with %+v, want the user's tail", opts)
		}
		if n := len(op.opened()); n != 8 {
			t.Fatalf("opened %v, want the three proxies once more and nothing else", op.opened())
		}
	})
}

// TestMuxFilterSelectedAgainWhileStopping checks that a source selected
// again before its excluded stream has stopped gets a new stream.
func TestMuxFilterSelectedAgainWhileStopping(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m := logmux.New(t.Context(), op.open, logmux.Options{LogOptions: corev1.PodLogOptions{Follow: true}})
		defer m.Close()
		drain(m)
		m.SetTargets([]logmux.Target{target("a", "app")})
		synctest.Wait()
		m.SetFilter(mustFilter(t, "-a"))
		m.SetFilter(logmux.Filter{})
		synctest.Wait()
		if got := op.opened(); len(got) != 2 {
			t.Fatalf("opened %v, want a second a/app stream", got)
		}
		if s := m.Stats(); s.Running != 1 || s.Excluded != 0 {
			t.Fatalf("Stats() = %+v, want the source streaming", s)
		}
	})
}

// TestMuxRetryResumesAFailedSource checks that a failed source keeps its
// error until a retry, and that the retry continues after its last line.
func TestMuxRetryResumesAFailedSource(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m := logmux.New(t.Context(), op.open, logmux.Options{LogOptions: corev1.PodLogOptions{Follow: true}})
		defer m.Close()
		drain(m)
		tgt := target("a", "app")
		m.SetTargets([]logmux.Target{tgt, target("b", "app")})
		synctest.Wait()
		op.stream(t, "a/app").lines <- "2026-11-22T10:00:01.5Z one"
		synctest.Wait()

		forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "a", errors.New("denied"))
		op.mu.Lock()
		op.fail["a/app"] = forbidden
		op.mu.Unlock()
		_ = op.stream(t, "a/app").Close() // the connection drops; the reconnect is refused
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()

		var failed logmux.Source
		for _, s := range m.Sources() {
			if s.Target == tgt {
				failed = s
			}
		}
		if failed.State != logmux.SourceFailed || !apierrors.IsForbidden(failed.Err) {
			t.Fatalf("source = %+v, want failed with the refusal", failed)
		}
		if s := m.Stats(); s.Failed != 1 || s.Running != 1 || !s.Incomplete() {
			t.Fatalf("Stats() = %+v, want 1 failed, 1 running, incomplete", s)
		}

		op.mu.Lock()
		delete(op.fail, "a/app")
		op.mu.Unlock()
		if n := m.Retry(); n != 1 {
			t.Fatalf("Retry() = %d, want 1", n)
		}
		synctest.Wait()
		opts := op.optsFor("a/app")
		if opts.SinceTime == nil || opts.SinceTime.UTC().Format("15:04:05") != "10:00:01" {
			t.Fatalf("retry opened with %+v, want sinceTime at the last line", opts)
		}
		if s := m.Stats(); s.Failed != 0 || s.Running != 2 {
			t.Fatalf("Stats() after retry = %+v", s)
		}
		if n := m.Retry(); n != 0 {
			t.Fatalf("Retry() with nothing failed = %d", n)
		}
	})
}

// TestMuxSetMaxStreams checks the coverage text of a view over the limit,
// and that raising the limit starts the waiting streams.
func TestMuxSetMaxStreams(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m := logmux.New(t.Context(), op.open, logmux.Options{MaxStreams: 1})
		defer m.Close()
		drain(m)
		m.SetTargets([]logmux.Target{target("a", "app"), target("b", "app"), target("c", "app")})
		synctest.Wait()
		s := m.Stats()
		if got, want := s.String(), "incomplete: 1 streaming, 2 waiting (limit 1)"; got != want {
			t.Fatalf("Stats().String() = %q, want %q", got, want)
		}
		if states := sourceStates(m); states["b/app"] != logmux.SourceWaiting {
			t.Fatalf("sources = %v", states)
		}

		m.SetMaxStreams(3)
		synctest.Wait()
		if got, want := m.Stats().String(), "3 streaming"; got != want {
			t.Fatalf("after raising the limit: %q, want %q", got, want)
		}

		// A lower limit stops nothing.
		m.SetMaxStreams(1)
		synctest.Wait()
		if s := m.Stats(); s.Running != 3 || s.Limit != 1 {
			t.Fatalf("after lowering the limit: %+v", s)
		}
	})
}
