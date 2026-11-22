package models

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stokaro/kc/internal/podfs"
)

// idleShell is a fake filesystem session. Like the real exec stream it
// runs a goroutine until Close, so a session that is never closed shows up
// as a leaked goroutine in a synctest bubble.
type idleShell struct {
	closed    atomic.Bool
	usedAfter atomic.Int32 // operations started on a closed shell
	done      chan struct{}
	closeOnce sync.Once
}

func newIdleShell() *idleShell {
	s := &idleShell{done: make(chan struct{})}
	go func() { <-s.done }()
	return s
}

func (s *idleShell) List(context.Context, string) ([]podfs.FileEntry, error) {
	if s.closed.Load() {
		s.usedAfter.Add(1)
		return nil, io.ErrClosedPipe
	}
	return []podfs.FileEntry{{Name: "etc", Type: podfs.EntryTypeDir}}, nil
}

func (s *idleShell) ReadFile(context.Context, string, int64) (io.ReadCloser, error) {
	if s.closed.Load() {
		s.usedAfter.Add(1)
		return nil, io.ErrClosedPipe
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (s *idleShell) Close() error {
	s.closed.Store(true)
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

// idleShellFactory opens a new idleShell for every session.
type idleShellFactory struct {
	mu     sync.Mutex
	shells []*idleShell
}

func (f *idleShellFactory) NewSession(context.Context, podfs.SessionSpec) (podfs.ExecSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := newIdleShell()
	f.shells = append(f.shells, s)
	return s, nil
}

func (f *idleShellFactory) opened() []*idleShell {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*idleShell(nil), f.shells...)
}

func useShell(t *testing.T, h *containerSessionHandle) *idleShell {
	t.Helper()
	sess, release, err := h.acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	if _, err := sess.List(t.Context(), "/"); err != nil {
		t.Fatalf("List: %v", err)
	}
	return sess.(*idleShell)
}

func TestContainerSessionClosesWhenIdleAndReopens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		factory := &idleShellFactory{}
		h := newContainerSessionHandle(t.Context(), factory, "ns", "pod", "app")

		first := useShell(t, h)
		time.Sleep(podfs.SessionIdleTimeout - time.Second)
		synctest.Wait()
		if first.closed.Load() {
			t.Fatal("shell closed before the idle timeout")
		}
		// A use inside the window restarts the idle period.
		if again := useShell(t, h); again != first {
			t.Fatal("a use inside the idle window opened a new shell")
		}
		time.Sleep(podfs.SessionIdleTimeout - time.Second)
		synctest.Wait()
		if first.closed.Load() {
			t.Fatal("shell closed before the idle timeout after the second use")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if !first.closed.Load() {
			t.Fatalf("shell still open %v after its last use", podfs.SessionIdleTimeout+time.Second)
		}

		second := useShell(t, h)
		if second == first {
			t.Fatal("the next operation reused the closed shell")
		}
		if n := len(factory.opened()); n != 2 {
			t.Fatalf("opened %d shells, want 2", n)
		}
		_ = h.Close()
	})
}

func TestContainerSessionHeldIsNeverIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		factory := &idleShellFactory{}
		h := newContainerSessionHandle(t.Context(), factory, "ns", "pod", "app")

		// A copy holds the session far longer than the idle timeout.
		sess, release, err := h.acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		shell := sess.(*idleShell)
		for range 10 {
			time.Sleep(podfs.SessionIdleTimeout)
			synctest.Wait()
			if _, err := sess.List(t.Context(), "/"); err != nil {
				t.Fatalf("held shell failed mid-copy: %v", err)
			}
		}
		// A listing that starts and ends during the copy must not arm
		// the idle timer under it.
		useShell(t, h)
		time.Sleep(2 * podfs.SessionIdleTimeout)
		synctest.Wait()
		if shell.closed.Load() {
			t.Fatal("a held shell closed because another user released it")
		}
		release()
		release() // a second release must not count twice
		time.Sleep(podfs.SessionIdleTimeout + time.Second)
		synctest.Wait()
		if !shell.closed.Load() {
			t.Fatal("shell still open after the copy released it and went idle")
		}
	})
}

// An acquire that takes the lock while the idle timer fires must get an
// open shell: either the old one, which then stays open, or a new one.
func TestContainerSessionAcquireAtIdleExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		factory := &idleShellFactory{}
		h := newContainerSessionHandle(t.Context(), factory, "ns", "pod", "app")
		useShell(t, h)
		// Wake up at the very instant the timer fires.
		time.Sleep(podfs.SessionIdleTimeout)
		shell := useShell(t, h)
		synctest.Wait()
		if shell.closed.Load() {
			t.Fatal("the timer closed the shell an operation had just taken")
		}
		for _, s := range factory.opened() {
			if n := s.usedAfter.Load(); n != 0 {
				t.Fatalf("an operation used a closed shell %d times", n)
			}
		}
		_ = h.Close()
	})
}

// A timer whose Stop came too late still runs expire; it must see that the
// session was taken since it was armed.
func TestContainerSessionStaleIdleTimerKeepsSession(t *testing.T) {
	factory := &idleShellFactory{}
	h := newContainerSessionHandle(t.Context(), factory, "ns", "pod", "app")
	h.idle = time.Hour
	shell := useShell(t, h)
	h.mu.Lock()
	armed := h.idleGen
	h.mu.Unlock()

	sess, release, err := h.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	h.expire(armed) // late: an operation holds the session
	release()
	h.expire(armed) // late: released, but a newer timer owns the session
	if shell.closed.Load() {
		t.Fatal("a stale idle timer closed the session")
	}
	if _, err := sess.List(t.Context(), "/"); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
}

// Many operations racing with a short idle timeout: none may run on a
// closed shell. Run with -race.
func TestContainerSessionIdleRace(t *testing.T) {
	t.Parallel()
	factory := &idleShellFactory{}
	h := newContainerSessionHandle(t.Context(), factory, "ns", "pod", "app")
	h.idle = 50 * time.Microsecond
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i := range 300 {
				sess, release, err := h.acquire(t.Context())
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := sess.List(t.Context(), "/"); err != nil {
					t.Errorf("List on an acquired shell: %v", err)
				}
				release()
				if i%10 == 0 {
					time.Sleep(100 * time.Microsecond)
				}
			}
		})
	}
	wg.Wait()
	_ = h.Close()
	shells := factory.opened()
	if len(shells) < 2 {
		t.Logf("only %d shell opened; the idle timer never won a race", len(shells))
	}
	for _, s := range shells {
		if n := s.usedAfter.Load(); n != 0 {
			t.Fatalf("an operation used a closed shell %d times", n)
		}
	}
}

// When kc exits (Deps.Ctx ends) the open shell closes and the idle timer
// stops; synctest fails the test if the shell's goroutine is left behind.
func TestContainerSessionClosesOnExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root, cancel := context.WithCancel(t.Context())
		factory := &idleShellFactory{}
		h := newContainerSessionHandle(root, factory, "ns", "pod", "app")
		shell := useShell(t, h)

		cancel()
		synctest.Wait()
		if !shell.closed.Load() {
			t.Fatal("shell still open after kc exited")
		}
		h.mu.Lock()
		timer, stop := h.idleTimer, h.stopRoot
		h.mu.Unlock()
		if timer != nil || stop != nil {
			t.Fatal("idle timer or exit hook left armed after exit")
		}
		if _, _, err := h.acquire(t.Context()); !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire after exit = %v, want context.Canceled", err)
		}
		if n := len(factory.opened()); n != 1 {
			t.Fatalf("opened %d shells after exit, want 1", n)
		}
	})
}

// hookCountingContext counts the context.AfterFunc hooks registered on it
// and not stopped yet. context.AfterFunc uses a parent's own AfterFunc
// method when it cannot find a standard cancelCtx through Value, so Value
// hides the embedded one.
type hookCountingContext struct {
	context.Context
	live atomic.Int32
}

func (*hookCountingContext) Value(any) any { return nil }

func (c *hookCountingContext) AfterFunc(f func()) func() bool {
	c.live.Add(1)
	stop := context.AfterFunc(c.Context, f)
	return func() bool {
		stopped := stop()
		if stopped {
			c.live.Add(-1)
		}
		return stopped
	}
}

// A closed session drops its exit hook, so browsing many containers does
// not pile hooks on Deps.Ctx.
func TestContainerSessionIdleCloseDropsExitHook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := &hookCountingContext{Context: t.Context()}
		factory := &idleShellFactory{}
		h := newContainerSessionHandle(root, factory, "ns", "pod", "app")
		for range 3 {
			useShell(t, h)
			if n := root.live.Load(); n != 1 {
				t.Fatalf("%d exit hooks while a shell is open, want 1", n)
			}
			time.Sleep(podfs.SessionIdleTimeout + time.Second)
			synctest.Wait()
			if n := root.live.Load(); n != 0 {
				t.Fatalf("%d exit hooks left after the idle close, want 0", n)
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.session != nil || h.idleTimer != nil {
			t.Fatalf("idle close left session=%v timer=%v", h.session != nil, h.idleTimer != nil)
		}
		if n := len(factory.opened()); n != 3 {
			t.Fatalf("opened %d shells, want 3", n)
		}
	})
}
