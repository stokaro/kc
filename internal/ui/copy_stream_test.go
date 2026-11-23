package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCopyStreamsContentOfAnySize(t *testing.T) {
	a := newTransferApp(t)
	data := bytes.Repeat([]byte("0123456789\n"), 400000) // 4.4 MB, past every viewer limit
	req := &copyRequest{
		subject: "/big.log", filename: "big.log", stream: true,
		fetch: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}
	dest := filepath.Join(t.TempDir(), "big.log")
	if err := a.copyToPath(req, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("export has %d bytes (%v), want %d", len(got), err, len(data))
	}
}

// stalledReader returns one chunk and then waits for its context.
type stalledReader struct {
	ctx  context.Context
	sent bool
}

func (r *stalledReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "first"), nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (r *stalledReader) Close() error { return nil }

func TestIdleTimeoutReaderEndsAStalledStream(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newIdleTimeoutReader(&stalledReader{ctx: ctx}, 20*time.Millisecond, cancel)
	defer r.Close()
	_, err := io.ReadAll(r)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled after the pause", err)
	}
}
