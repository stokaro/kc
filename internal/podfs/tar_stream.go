package podfs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"k8s.io/client-go/tools/remotecommand"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// tarStderrLimit bounds the tar error output kept for the error message.
const tarStderrLimit = 2048

// TarStream runs `tar cf - -C dir ./name` in the container, in its own exec
// stream next to the session's shell, and returns the archive. A failure of
// tar (missing binary, unreadable file) surfaces as the error of the last
// Read, with the tail of tar's error output. Close stops the exec.
//
// A session with a heartbeat (the debug helper) runs tar under a script
// that touches the heartbeat file while tar runs, so the helper does not
// exit after its idle timeout during a long stream.
func (s *nativeSession) TarStream(ctx context.Context, dir, name string) (io.ReadCloser, error) {
	if !s.execAvailable() {
		return nil, fmt.Errorf("podfs: tar stream unavailable")
	}
	command := []string{"tar", "cf", "-", "-C", dir, "./" + name}
	if s.spec.Heartbeat != "" {
		command = []string{"sh", "-c", tarCreateScript, "kc-copy", s.spec.Heartbeat, dir, name}
	}
	ctrllog.FromContext(ctx).WithName("podfs").Info("exec tar",
		"namespace", s.spec.Namespace, "pod", s.spec.Pod, "container", s.spec.Container, "dir", dir, "name", name)
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	stderr := &tailBuffer{limit: tarStderrLimit}
	go func() {
		err := s.runExec(ctx, s.spec.Container, command, remotecommand.StreamOptions{Stdout: pw, Stderr: stderr})
		if err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				err = fmt.Errorf("tar: %w: %s", err, msg)
			} else {
				err = fmt.Errorf("tar: %w", err)
			}
		}
		pw.CloseWithError(err)
	}()
	return &tarReader{PipeReader: pr, cancel: cancel}, nil
}

// tarCreateScript runs tar cf - and keeps a heartbeat file fresh while it
// runs. Arguments: heartbeat file, directory, name.
const tarCreateScript = `hb=$1 dir=$2 name=$3
touch "$hb" 2>/dev/null
( while kill -0 $$ 2>/dev/null; do touch "$hb" 2>/dev/null; sleep 10; done ) </dev/null >/dev/null 2>&1 &
hbpid=$!
tar -cf - -C "$dir" "./$name"
rc=$?
kill "$hbpid" 2>/dev/null
exit $rc
`

// TarStream streams tar from the target container through the helper,
// which sees the target's files under HelperRoot.
func (h *helperAwareSession) TarStream(ctx context.Context, dir, name string) (io.ReadCloser, error) {
	inner, ok := h.ExecSession.(*nativeSession)
	if !ok {
		return nil, fmt.Errorf("podfs: tar stream unavailable")
	}
	return inner.TarStream(ctx, HelperPath(dir), name)
}

type tarReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *tarReader) Close() error {
	r.cancel()
	return r.PipeReader.Close()
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = b.buf[over:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
