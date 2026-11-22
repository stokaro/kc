// Package podexec runs a command in a pod container with stdin attached,
// the way `kubectl exec -i[t]` does. It is the transport only: the caller
// owns the terminal emulator and the pod.
package podexec

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// Target names the container and the command to run in it.
type Target struct {
	Namespace string
	Pod       string
	Container string
	Command   []string
	// TTY allocates a terminal; the remote side then merges stderr into
	// stdout. Without one, stderr is not requested.
	TTY bool
}

// Stream connects stdin and stdout to Command and blocks until the command
// exits, the connection breaks or ctx ends. sizes delivers terminal size
// changes of a TTY; it may be nil.
func Stream(ctx context.Context, cfg *rest.Config, client kubernetes.Interface, t Target, stdin io.Reader, stdout io.Writer, sizes *SizeQueue) error {
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(t.Namespace).
		Name(t.Pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: t.Container,
			Command:   t.Command,
			Stdin:     true,
			Stdout:    true,
			TTY:       t.TTY,
		}, scheme.ParameterCodec)
	exec, err := newExecutor(cfg, req.URL())
	if err != nil {
		return err
	}
	ctrllog.FromContext(ctx).Info("exec in pod", "gvr", "v1/pods", "subresource", "exec",
		"namespace", t.Namespace, "pod", t.Pod, "container", t.Container, "command", t.Command)
	opts := remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Tty: t.TTY}
	if sizes != nil && t.TTY {
		opts.TerminalSizeQueue = sizes
	}
	return exec.StreamWithContext(ctx, opts)
}

// newExecutor prefers WebSockets and falls back to SPDY for API servers
// that cannot upgrade, as kubectl does.
func newExecutor(cfg *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	ws, err := remotecommand.NewWebSocketExecutor(cfg, "GET", u.String())
	if err != nil {
		return nil, fmt.Errorf("exec: websocket executor: %w", err)
	}
	spdy, err := remotecommand.NewSPDYExecutor(cfg, "POST", u)
	if err != nil {
		return nil, fmt.Errorf("exec: spdy executor: %w", err)
	}
	return remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
}

// SizeQueue hands terminal sizes to the exec stream. Only the latest size
// matters, so a size that was not read yet is replaced.
type SizeQueue struct {
	mu     sync.Mutex
	ch     chan remotecommand.TerminalSize
	done   chan struct{}
	closed bool
}

// NewSizeQueue returns a queue that starts with the given size.
func NewSizeQueue(width, height int) *SizeQueue {
	q := &SizeQueue{ch: make(chan remotecommand.TerminalSize, 1), done: make(chan struct{})}
	q.Push(width, height)
	return q
}

// Push records a new size. It never blocks.
func (q *SizeQueue) Push(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	select {
	case <-q.ch:
	default:
	}
	q.ch <- remotecommand.TerminalSize{Width: uint16(width), Height: uint16(height)}
}

// Next blocks until a size arrives; nil ends the resize loop of the stream.
func (q *SizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case s := <-q.ch:
		return &s
	case <-q.done:
		return nil
	}
}

// Close ends Next. It is safe to call more than once.
func (q *SizeQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.done)
	}
}
