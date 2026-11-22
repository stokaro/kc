package podfs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/remotecommand"
)

// scriptedShell is a fake exec stream that answers the bootstrap and
// __kc_ls like the session's shell. Like a SPDY stream, it ends when the
// context passed to StreamWithContext ends.
type scriptedShell struct {
	started chan struct{}
}

func (f *scriptedShell) Stream(remotecommand.StreamOptions) error {
	return fmt.Errorf("scriptedShell: use StreamWithContext")
}

func (f *scriptedShell) StreamWithContext(ctx context.Context, opts remotecommand.StreamOptions) error {
	close(f.started)
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(opts.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			switch {
			case strings.Contains(line, cmdPrefixReady):
				_, _ = io.WriteString(opts.Stdout, cmdPrefixReady+"\n")
			case strings.HasPrefix(line, "__kc_ls "):
				_, _ = io.WriteString(opts.Stdout, cmdPrefixLSDone+"\n")
			}
		}
	}
}

// TestNativeSessionOutlivesOpenContext covers #133: the folder opens a
// session under a request context, caches it, and a later copy used the
// session after that context ended and failed with "read/write on closed
// pipe".
func TestNativeSessionOutlivesOpenContext(t *testing.T) {
	t.Parallel()
	shell := &scriptedShell{started: make(chan struct{})}
	openCtx, cancelOpen := context.WithTimeout(t.Context(), 10*time.Second)
	sess, err := startNativeSession(openCtx, SessionSpec{Namespace: "ns", Pod: "p", Container: "c"}, shell, nil, nil)
	if err != nil {
		cancelOpen()
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	cancelOpen()

	// Give a stream bound to openCtx the time to end before the next use.
	native := sess.(*nativeSession)
	select {
	case <-native.doneCh:
		t.Fatalf("session stream ended with the context that opened it: %v", native.sessionError())
	case <-time.After(100 * time.Millisecond):
	}

	listCtx, cancelList := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelList()
	if _, err := sess.List(listCtx, "/data"); err != nil {
		t.Fatalf("List after the open context ended: %v", err)
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-native.doneCh:
	case <-time.After(10 * time.Second):
		t.Fatal("session stream still running after Close")
	}
}

// TestNativeSessionBootstrapBoundedByOpenContext keeps the open context as
// the bound on establishing the session.
func TestNativeSessionBootstrapBoundedByOpenContext(t *testing.T) {
	t.Parallel()
	silent := remotecommandFunc(func(ctx context.Context, opts remotecommand.StreamOptions) error {
		go func() { _, _ = io.Copy(io.Discard, opts.Stdin) }()
		<-ctx.Done()
		return ctx.Err()
	})
	openCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := startNativeSession(openCtx, SessionSpec{Namespace: "ns", Pod: "p", Container: "c"}, silent, nil, nil); err == nil {
		t.Fatal("bootstrap against a silent shell succeeded; want the open context's deadline")
	}
}

type remotecommandFunc func(ctx context.Context, opts remotecommand.StreamOptions) error

func (f remotecommandFunc) Stream(remotecommand.StreamOptions) error {
	return fmt.Errorf("remotecommandFunc: use StreamWithContext")
}

func (f remotecommandFunc) StreamWithContext(ctx context.Context, opts remotecommand.StreamOptions) error {
	return f(ctx, opts)
}
