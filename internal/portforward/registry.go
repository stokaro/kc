package portforward

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultAddress is the local address forwards bind to unless configured.
const DefaultAddress = "127.0.0.1"

// Spec describes one forward: where it listens and which pod port it
// reaches. For a service the pod is the one chosen when the forward
// started; the forward does not follow the service to another pod.
type Spec struct {
	Context   string // kubeconfig context
	Namespace string
	// Kind is "pod" or "service", as in "pod/web" or "service/web".
	Kind string
	Name string
	// Pod is the pod that carries the traffic; equal to Name for pods.
	Pod string
	// PodUID is the uid of Pod when the forward started. A pod created
	// again under the name is another pod, and the forward ends. Empty
	// takes the uid of the pod the forward first sees.
	PodUID    types.UID
	Container string // container the user started from, informational
	Address   string // local bind address
	// LocalPort 0 lets the system choose.
	LocalPort int32
	// RemotePort is the pod port.
	RemotePort int32
	// ServicePort is the service port for a service forward, else 0.
	ServicePort int32
	// Command is the equivalent kubectl command.
	Command string
}

// Target is the "kind/name:port" the user asked for.
func (s Spec) Target() string {
	port := s.RemotePort
	if s.ServicePort != 0 {
		port = s.ServicePort
	}
	return fmt.Sprintf("%s/%s:%d", s.Kind, s.Name, port)
}

// Forward is an active forward.
type Forward struct {
	ID        string
	Spec      Spec
	LocalPort int32 // the bound port, also when Spec.LocalPort was 0
	Started   time.Time
}

// Local is the address users connect to, e.g. "127.0.0.1:8080".
func (f Forward) Local() string {
	return net.JoinHostPort(f.Spec.Address, strconv.Itoa(int(f.LocalPort)))
}

// Runner carries the traffic of one forward. Run blocks until ctx ends or
// the forward fails. It calls ready once with the bound local port when it
// accepts connections. A nil return after ctx ended is a normal stop.
type Runner interface {
	Run(ctx context.Context, spec Spec, ready func(localPort int32)) error
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, spec Spec, ready func(localPort int32)) error

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, spec Spec, ready func(localPort int32)) error {
	return f(ctx, spec, ready)
}

// ErrStopped is the error Start reports when the registry was stopped
// before the forward became ready.
var ErrStopped = errors.New("port forwarding stopped")

type entry struct {
	fwd    Forward
	ready  bool
	cancel context.CancelFunc
	done   chan struct{}
}

// Registry owns the forwards of one kc process. Forwards outlive the
// folder and the context that started them; StopAll ends them on exit.
// All methods are safe for concurrent use.
type Registry struct {
	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time

	mu        sync.Mutex
	seq       int
	stopped   bool
	entries   map[string]*entry
	listeners map[int]func()
	nextL     int
	onEnded   func(Forward, error)
	wg        sync.WaitGroup
}

// NewRegistry creates a registry whose forwards end when ctx ends.
func NewRegistry(ctx context.Context) *Registry {
	ctx, cancel := context.WithCancel(ctx)
	return &Registry{
		ctx:       ctx,
		cancel:    cancel,
		now:       time.Now,
		entries:   make(map[string]*entry),
		listeners: make(map[int]func()),
	}
}

// SetClock replaces the clock used for Forward.Started; for tests.
func (r *Registry) SetClock(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

// OnEnded sets the callback for forwards that end without Stop: the pod
// went away, the connection broke. It runs on the forward's goroutine.
func (r *Registry) OnEnded(fn func(Forward, error)) {
	r.mu.Lock()
	r.onEnded = fn
	r.mu.Unlock()
}

// Subscribe calls fn after every change of the forward list. It returns a
// function that ends the subscription. fn runs on the goroutine that made
// the change and must not block.
func (r *Registry) Subscribe(fn func()) func() {
	r.mu.Lock()
	id := r.nextL
	r.nextL++
	r.listeners[id] = fn
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.listeners, id)
		r.mu.Unlock()
	}
}

func (r *Registry) notify() {
	r.mu.Lock()
	fns := make([]func(), 0, len(r.listeners))
	for _, fn := range r.listeners {
		fns = append(fns, fn)
	}
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Start runs spec through runner and waits until it listens, fails, or ctx
// ends. Call it off the UI goroutine: dialing the API server takes a round
// trip. Once Start returns a Forward, the forward runs until Stop, StopAll
// or a failure, independent of ctx.
func (r *Registry) Start(ctx context.Context, spec Spec, runner Runner) (Forward, error) {
	log := ctrllog.FromContext(ctx).WithName("portforward")
	runCtx, cancel := context.WithCancel(ctrllog.IntoContext(r.ctx, log))
	e := &entry{cancel: cancel, done: make(chan struct{})}

	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		cancel()
		return Forward{}, ErrStopped
	}
	r.seq++
	id := strconv.Itoa(r.seq)
	e.fwd = Forward{ID: id, Spec: spec}
	// The entry is in the map from the start, so finish always finds it;
	// List shows it only once it is ready.
	r.entries[id] = e
	r.mu.Unlock()

	readyCh := make(chan int32, 1)
	errCh := make(chan error, 1)
	r.wg.Go(func() {
		defer close(e.done)
		var once sync.Once
		err := runner.Run(runCtx, spec, func(port int32) {
			once.Do(func() { readyCh <- port })
		})
		r.finish(id, e, runCtx, err)
		errCh <- err
	})

	abort := func(err error) (Forward, error) {
		r.mu.Lock()
		if r.entries[id] == e {
			delete(r.entries, id)
		}
		r.mu.Unlock()
		cancel()
		<-e.done
		return Forward{}, err
	}

	select {
	case port := <-readyCh:
		r.mu.Lock()
		if r.entries[id] != e {
			// The runner failed or was stopped right after it was ready.
			r.mu.Unlock()
			<-e.done
			if err := <-errCh; err != nil {
				return Forward{}, err
			}
			return Forward{}, ErrStopped
		}
		e.ready = true
		e.fwd.LocalPort = port
		e.fwd.Started = r.now()
		fwd := e.fwd
		r.mu.Unlock()
		log.Info("port forward ready", "id", id, "local", fwd.Local(), "target", spec.Target(), "pod", spec.Pod, "namespace", spec.Namespace, "context", spec.Context)
		r.notify()
		return fwd, nil
	case err := <-errCh:
		cancel()
		if err == nil {
			err = errors.New("the forward ended before it was ready")
		}
		return Forward{}, err
	case <-ctx.Done():
		return abort(ctx.Err())
	case <-r.ctx.Done():
		return abort(ErrStopped)
	}
}

// finish removes a forward whose runner returned and reports a failure
// of a ready forward that Stop did not cause.
func (r *Registry) finish(id string, e *entry, runCtx context.Context, err error) {
	r.mu.Lock()
	owned := r.entries[id] == e
	if owned {
		delete(r.entries, id)
	}
	ready := e.ready
	fwd := e.fwd
	onEnded := r.onEnded
	r.mu.Unlock()
	if !owned || !ready {
		return
	}
	r.notify()
	if runCtx.Err() != nil {
		return // stopped on purpose
	}
	if err == nil {
		err = errors.New("the connection closed")
	}
	ctrllog.FromContext(runCtx).Info("port forward ended", "id", id, "local", fwd.Local(), "target", fwd.Spec.Target(), "error", err.Error())
	if onEnded != nil {
		onEnded(fwd, err)
	}
}

// Stop ends the forward with id and reports whether it existed. It does
// not wait for the runner, so the UI goroutine may call it; the listener
// closes right after.
func (r *Registry) Stop(id string) bool {
	r.mu.Lock()
	e, ok := r.entries[id]
	if ok && e.ready {
		delete(r.entries, id)
	}
	r.mu.Unlock()
	if !ok || !e.ready {
		return false
	}
	e.cancel()
	r.notify()
	return true
}

// StopAll ends every forward and refuses new ones. It waits at most
// timeout for the runners to return.
func (r *Registry) StopAll(timeout time.Duration) {
	r.mu.Lock()
	r.stopped = true
	had := r.readyCountLocked() > 0
	r.entries = make(map[string]*entry)
	r.mu.Unlock()
	r.cancel()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
	if had {
		r.notify()
	}
}

// List returns the active forwards in start order.
func (r *Registry) List() []Forward {
	r.mu.Lock()
	out := make([]Forward, 0, len(r.entries))
	for _, e := range r.entries {
		if e.ready {
			out = append(out, e.fwd)
		}
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].ID)
		b, _ := strconv.Atoi(out[j].ID)
		return a < b
	})
	return out
}

// Get returns the forward with id.
func (r *Registry) Get(id string) (Forward, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok || !e.ready {
		return Forward{}, false
	}
	return e.fwd, true
}

// Len reports the number of active forwards.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readyCountLocked()
}

func (r *Registry) readyCountLocked() int {
	n := 0
	for _, e := range r.entries {
		if e.ready {
			n++
		}
	}
	return n
}
