package podfs

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/tools/remotecommand"
)

// localShell runs the session's shell on this machine instead of in a pod.
// The script needs GNU or busybox stat (stat -c), so the test skips where
// stat is BSD's.
type localShell struct{}

func (localShell) Stream(remotecommand.StreamOptions) error { return io.ErrUnexpectedEOF }

func (localShell) StreamWithContext(ctx context.Context, opts remotecommand.StreamOptions) error {
	cmd := exec.CommandContext(ctx, "sh") // #nosec G204 -- a fixed command for the test
	cmd.Stdin, cmd.Stdout, cmd.Stderr = opts.Stdin, opts.Stdout, opts.Stderr
	return cmd.Run()
}

func requireGNUStat(t *testing.T) {
	t.Helper()
	if err := exec.CommandContext(t.Context(), "stat", "-c", "%s", "/").Run(); err != nil { // #nosec G204 -- a fixed command for the test
		t.Skip("stat -c is not available here; the script targets GNU and busybox stat")
	}
}

func TestNativeSessionReadsARange(t *testing.T) {
	t.Parallel()
	requireGNUStat(t)
	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	file := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	sess, err := startNativeSession(ctx, SessionSpec{}, localShell{}, nil, nil)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	rr := sess.(RangeReader)

	tests := []struct {
		name           string
		offset, length int64
		want           []byte
	}{
		{name: "middle", offset: 4096, length: 5000, want: data[4096:9096]},
		{name: "tail", offset: 8192, length: 5000, want: data[8192:]},
		{name: "past the end", offset: 12288, length: 5000, want: nil},
		{name: "first byte", offset: 0, length: 1, want: data[:1]},
	}
	for _, tc := range tests {
		rc, total, err := rr.ReadFileRange(ctx, file, tc.offset, tc.length)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("%s: read: %v", tc.name, err)
		}
		if total != int64(len(data)) || !bytes.Equal(got, tc.want) {
			t.Fatalf("%s: got %d bytes (total %d), want %d bytes", tc.name, len(got), total, len(tc.want))
		}
	}
	// The session answers the next command after a range read.
	if _, err := sess.List(ctx, filepath.Dir(file)); err != nil {
		t.Fatalf("List after range reads: %v", err)
	}
	if _, _, err := rr.ReadFileRange(ctx, file, 100, 10); err == nil {
		t.Fatal("an offset that is not a multiple of RangeAlign was accepted")
	}
}
