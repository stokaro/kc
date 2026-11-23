package logresume

import (
	"context"
	"io"
	"sync/atomic"
)

// NoticePrefix starts the lines Reader adds about the connection, like the
// other messages kc writes into a log.
const NoticePrefix = "[kc] "

// Reader is a resumed log stream read like a single container log: the
// log lines, one per line, and kc's notices about the connection as
// "[kc] ..." lines. The error of a stream that failed for good is returned
// by Read.
type Reader struct {
	pr           *io.PipeReader
	cancel       context.CancelFunc
	reconnecting atomic.Bool
	done         chan struct{}
}

// Open opens the stream with cfg and returns a Reader that follows it
// across failures. The first open happens before Open returns, and its
// error is returned as it is.
func Open(ctx context.Context, cfg Config) (*Reader, error) {
	ctx, cancel := context.WithCancel(ctx)
	first, err := cfg.Open(ctx, newResumer(cfg, Sink{}).options())
	if err != nil {
		cancel()
		return nil, err
	}
	pr, pw := io.Pipe()
	r := &Reader{pr: pr, cancel: cancel, done: make(chan struct{})}
	write := func(s string) bool {
		_, err := io.WriteString(pw, s+"\n")
		return err == nil
	}
	sink := Sink{
		Line:   write,
		Notice: func(text string) { write(NoticePrefix + text) },
		State:  func(s State) { r.reconnecting.Store(s == StateReconnecting) },
	}
	go func() {
		defer close(r.done)
		err := run(ctx, cfg, sink, first)
		// Run can end while it waits to reconnect: a refusal, the last
		// failed attempt, a stopped instance or a cancel. Nothing is
		// pending then, so the state is cleared before the end shows.
		r.reconnecting.Store(false)
		pw.CloseWithError(endOrErr(err))
	}()
	return r, nil
}

// endOrErr turns a nil result into io.EOF for the pipe.
func endOrErr(err error) error {
	if err == nil {
		return io.EOF
	}
	return err
}

// Read reads the next lines.
func (r *Reader) Read(p []byte) (int, error) { return r.pr.Read(p) }

// Close stops the stream and any retry. It does not wait for them.
func (r *Reader) Close() error {
	r.cancel()
	return r.pr.Close()
}

// Reconnecting reports whether the stream is waiting to reconnect. It is
// false once the stream has stopped for good.
func (r *Reader) Reconnecting() bool { return r.reconnecting.Load() }

// Done is closed when the stream and its retries have stopped.
func (r *Reader) Done() <-chan struct{} { return r.done }
