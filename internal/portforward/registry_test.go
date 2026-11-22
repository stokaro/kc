package portforward_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/portforward"
)

// fakeRunner stands in for the API server stream. It reports ready with
// its port unless failBeforeReady is set, then blocks until the context
// ends or fail is closed.
type fakeRunner struct {
	port            int32
	failBeforeReady error
	fail            chan error
	running         atomic.Int32
	stopped         chan struct{}
}

func newFakeRunner(port int32) *fakeRunner {
	return &fakeRunner{port: port, fail: make(chan error, 1), stopped: make(chan struct{})}
}

func (f *fakeRunner) Run(ctx context.Context, _ portforward.Spec, ready func(int32)) error {
	f.running.Add(1)
	defer f.running.Add(-1)
	defer close(f.stopped)
	if f.failBeforeReady != nil {
		return f.failBeforeReady
	}
	ready(f.port)
	select {
	case <-ctx.Done():
		return nil
	case err := <-f.fail:
		return err
	}
}

func spec(name string) portforward.Spec {
	return portforward.Spec{Context: "dev", Namespace: "ns", Kind: "pod", Name: name, Pod: name, Address: "127.0.0.1", LocalPort: 0, RemotePort: 80}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRegistryStartListStop(t *testing.T) {
	t.Parallel()
	reg := portforward.NewRegistry(t.Context())
	t0 := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	reg.SetClock(func() time.Time { return t0 })
	var changes atomic.Int32
	unsubscribe := reg.Subscribe(func() { changes.Add(1) })
	defer unsubscribe()

	a := newFakeRunner(40001)
	b := newFakeRunner(40002)
	fa, err := reg.Start(t.Context(), spec("a"), a)
	if err != nil {
		t.Fatalf("Start a: %v", err)
	}
	fb, err := reg.Start(t.Context(), spec("b"), b)
	if err != nil {
		t.Fatalf("Start b: %v", err)
	}
	if fa.LocalPort != 40001 || fa.Local() != "127.0.0.1:40001" || !fa.Started.Equal(t0) {
		t.Fatalf("forward a = %+v", fa)
	}
	list := reg.List()
	if len(list) != 2 || list[0].ID != fa.ID || list[1].ID != fb.ID {
		t.Fatalf("List = %+v, want a then b", list)
	}
	if got := changes.Load(); got != 2 {
		t.Fatalf("change notifications after two starts = %d, want 2", got)
	}

	if !reg.Stop(fa.ID) {
		t.Fatal("Stop(a) = false, want true")
	}
	<-a.stopped
	if reg.Stop(fa.ID) {
		t.Fatal("second Stop(a) = true, want false")
	}
	if _, ok := reg.Get(fa.ID); ok {
		t.Fatal("Get(a) after Stop found it")
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1", reg.Len())
	}
	if got := changes.Load(); got != 3 {
		t.Fatalf("change notifications after stop = %d, want 3", got)
	}
	if b.running.Load() != 1 {
		t.Fatal("stopping a stopped b too")
	}
}

func TestRegistryStartFailure(t *testing.T) {
	t.Parallel()
	reg := portforward.NewRegistry(t.Context())
	r := newFakeRunner(1)
	r.failBeforeReady = errors.New("pods \"a\" is forbidden")
	var ended atomic.Int32
	reg.OnEnded(func(portforward.Forward, error) { ended.Add(1) })
	if _, err := reg.Start(t.Context(), spec("a"), r); err == nil || err.Error() != r.failBeforeReady.Error() {
		t.Fatalf("Start error = %v, want %v", err, r.failBeforeReady)
	}
	if reg.Len() != 0 {
		t.Fatalf("Len after failed start = %d, want 0", reg.Len())
	}
	if ended.Load() != 0 {
		t.Fatal("OnEnded fired for a forward that never started")
	}
}

func TestRegistryStartCanceled(t *testing.T) {
	t.Parallel()
	reg := portforward.NewRegistry(t.Context())
	block := portforward.RunnerFunc(func(ctx context.Context, _ portforward.Spec, _ func(int32)) error {
		<-ctx.Done() // never ready
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := reg.Start(ctx, spec("a"), block); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start error = %v, want deadline exceeded", err)
	}
	if reg.Len() != 0 {
		t.Fatalf("Len = %d, want 0", reg.Len())
	}
}

func TestRegistryForwardEndsOnItsOwn(t *testing.T) {
	t.Parallel()
	reg := portforward.NewRegistry(t.Context())
	type end struct {
		fwd portforward.Forward
		err error
	}
	ended := make(chan end, 1)
	reg.OnEnded(func(f portforward.Forward, err error) { ended <- end{f, err} })
	r := newFakeRunner(40003)
	fwd, err := reg.Start(t.Context(), spec("a"), r)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	lost := errors.New("lost connection to pod")
	r.fail <- lost
	select {
	case got := <-ended:
		if got.fwd.ID != fwd.ID || !errors.Is(got.err, lost) {
			t.Fatalf("OnEnded(%+v, %v), want forward %s and %v", got.fwd, got.err, fwd.ID, lost)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnEnded not called after the stream failed")
	}
	if reg.Len() != 0 {
		t.Fatalf("Len = %d, want 0", reg.Len())
	}
}

func TestRegistryStopDoesNotReportEnded(t *testing.T) {
	t.Parallel()
	reg := portforward.NewRegistry(t.Context())
	var ended atomic.Int32
	reg.OnEnded(func(portforward.Forward, error) { ended.Add(1) })
	r := newFakeRunner(40004)
	fwd, err := reg.Start(t.Context(), spec("a"), r)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	reg.Stop(fwd.ID)
	<-r.stopped
	// finish runs on the runner goroutine after Run returned.
	time.Sleep(20 * time.Millisecond)
	if ended.Load() != 0 {
		t.Fatal("OnEnded fired for a forward stopped on purpose")
	}
}

func TestRegistryStopAll(t *testing.T) {
	t.Parallel()
	reg := portforward.NewRegistry(t.Context())
	runners := []*fakeRunner{newFakeRunner(1), newFakeRunner(2), newFakeRunner(3)}
	for i, r := range runners {
		if _, err := reg.Start(t.Context(), spec(string(rune('a'+i))), r); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
	}
	if reg.Len() != 3 {
		t.Fatalf("Len = %d, want 3", reg.Len())
	}
	reg.StopAll(5 * time.Second)
	for i, r := range runners {
		if r.running.Load() != 0 {
			t.Fatalf("runner %d still running after StopAll", i)
		}
	}
	if reg.Len() != 0 {
		t.Fatalf("Len after StopAll = %d, want 0", reg.Len())
	}
	if _, err := reg.Start(t.Context(), spec("late"), newFakeRunner(4)); !errors.Is(err, portforward.ErrStopped) {
		t.Fatalf("Start after StopAll error = %v, want ErrStopped", err)
	}
}

func TestRegistryParentContextEndsForwards(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	reg := portforward.NewRegistry(ctx)
	r := newFakeRunner(5)
	if _, err := reg.Start(t.Context(), spec("a"), r); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()
	select {
	case <-r.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop when the registry context ended")
	}
	waitFor(t, "forward removed", func() bool { return reg.Len() == 0 })
}

func TestRegistryConcurrentStarts(t *testing.T) {
	t.Parallel()
	reg := portforward.NewRegistry(t.Context())
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if _, err := reg.Start(t.Context(), spec("p"), newFakeRunner(int32(i+1))); err != nil {
				t.Errorf("Start: %v", err)
			}
		})
	}
	wg.Wait()
	list := reg.List()
	if len(list) != n {
		t.Fatalf("List has %d forwards, want %d", len(list), n)
	}
	seen := make(map[string]bool)
	for _, f := range list {
		if seen[f.ID] {
			t.Fatalf("duplicate id %s", f.ID)
		}
		seen[f.ID] = true
	}
	reg.StopAll(5 * time.Second)
}
