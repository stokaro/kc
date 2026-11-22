package podfs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
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
// Only a session in the target container offers it: the debug helper
// exits after a minute without a command, which a long tar does not send.
func (s *nativeSession) TarStream(ctx context.Context, dir, name string) (io.ReadCloser, error) {
	if s.cfg == nil || s.client == nil {
		return nil, fmt.Errorf("podfs: tar stream unavailable")
	}
	command := []string{"tar", "cf", "-", "-C", dir, "./" + name}
	req := s.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(s.spec.Pod).
		Namespace(s.spec.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: s.spec.Container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(s.cfg, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("podfs: executor: %w", err)
	}
	ctrllog.FromContext(ctx).WithName("podfs").Info("exec tar",
		"namespace", s.spec.Namespace, "pod", s.spec.Pod, "container", s.spec.Container, "dir", dir, "name", name)
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	stderr := &tailBuffer{limit: tarStderrLimit}
	go func() {
		err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: pw, Stderr: stderr})
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
