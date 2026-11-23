package logs

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	models "github.com/stokaro/kc/internal/models"
)

// FollowerQueue bounds the lines a Follower holds that its reader has not
// read yet. The panel never waits for a reader: past the bound the oldest
// unread lines go, and the reader gets a "[kc] N lines skipped" line in
// their place.
const FollowerQueue = 8192

// ErrNoSession is returned by Follow when the panel streams nothing.
var ErrNoSession = errors.New("the Logs panel shows no log")

// Session is a Logs panel stream handed to another view: the lines the
// panel keeps now and a Follower for the lines it reads from now on.
type Session struct {
	// Spec is what the panel streams.
	Spec models.LogsSpec
	// AllContainers is set for a merged stream of every container.
	AllContainers bool
	// Lines are the lines the panel keeps, oldest first.
	Lines []string
	// Follower delivers the lines the panel reads after Follow.
	Follower *Follower
}

// Follower is a reader of the lines a Logs panel reads, one per line. It
// shares the panel's stream: it opens nothing, and closing it does not
// stop the panel. It ends when the panel's stream ends, fails or moves to
// another target. Read and Close are safe for concurrent use; the panel
// writes on the UI goroutine and never blocks. Reconnecting asks the
// panel's stream, so call it on the UI goroutine.
type Follower struct {
	mu      sync.Mutex
	cond    *sync.Cond
	queue   []string
	skipped int
	buf     []byte
	end     error // set once no more lines come; io.EOF for a clean end
	closed  bool
	// reconnecting reports the panel stream's connection state.
	reconnecting func() bool
}

func newFollower(reconnecting func() bool) *Follower {
	f := &Follower{reconnecting: reconnecting}
	f.cond = sync.NewCond(&f.mu)
	return f
}

// Read returns the next lines. It blocks until a line arrives, the stream
// ends, or the reader is closed.
func (f *Follower) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.buf) == 0 {
		if f.closed {
			return 0, io.ErrClosedPipe
		}
		if f.skipped > 0 {
			f.buf = fmt.Appendf(f.buf, "[kc] %d lines skipped: the view fell behind the stream\n", f.skipped)
			f.skipped = 0
			continue
		}
		if len(f.queue) > 0 {
			f.buf = append(f.buf, strings.Join(f.queue, "\n")...)
			f.buf = append(f.buf, '\n')
			f.queue = f.queue[:0]
			continue
		}
		if f.end != nil {
			return 0, f.end
		}
		f.cond.Wait()
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

// Close stops the reader. The panel's stream goes on.
func (f *Follower) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.queue = nil
	f.cond.Broadcast()
	return nil
}

// Reconnecting reports whether the panel's stream waits to reconnect.
func (f *Follower) Reconnecting() bool {
	f.mu.Lock()
	ended := f.end != nil
	f.mu.Unlock()
	return !ended && f.reconnecting != nil && f.reconnecting()
}

// Closed reports whether the reader was closed.
func (f *Follower) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// add queues lines without waiting for the reader.
func (f *Follower) add(lines []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.end != nil || len(lines) == 0 {
		return
	}
	f.queue = append(f.queue, lines...)
	if over := len(f.queue) - FollowerQueue; over > 0 {
		f.skipped += over
		f.queue = append(f.queue[:0], f.queue[over:]...)
	}
	f.cond.Broadcast()
}

// finish ends the reader after the queued lines, with note as a last line
// when it is not empty, and err (io.EOF for a clean end).
func (f *Follower) finish(note string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.end != nil {
		return
	}
	if note != "" && !f.closed {
		f.queue = append(f.queue, note)
	}
	f.end = err
	f.cond.Broadcast()
}

// HasSession reports whether the panel streams a log, or did and keeps
// its lines.
func (w *Widget) HasSession() bool { return w.state != stateIdle }

// Follow hands the panel's session to another view: the kept lines, and a
// Follower that gets every line the panel reads from now on. The panel
// keeps its stream and its lines. It returns ErrNoSession while the panel
// shows the hint.
func (w *Widget) Follow() (Session, error) {
	if w.state == stateIdle {
		return Session{}, ErrNoSession
	}
	// The viewer asks on the UI goroutine, which owns w; a stream that is
	// still opening has no body yet.
	f := newFollower(func() bool { return reconnecting(w.body) })
	switch w.state {
	case stateEnded:
		f.finish("", io.EOF)
	case stateFailed:
		f.finish("", w.err)
	}
	w.followers = append(w.followers, f)
	return Session{
		Spec:          w.spec,
		AllContainers: w.req.AllContainers,
		Lines:         w.keptLines(),
		Follower:      f,
	}, nil
}

// followerStopped is the last line a follower reads when the panel stops
// its stream: another target, other options, or another panel mode.
const followerStopped = "[kc] the Logs panel stopped this stream; reopen the logs to follow it again"

// keptLines are the log lines the panel shows, as they were read rather
// than formatted, so the viewer parses them itself; none while the panel
// waits for the first one.
func (w *Widget) keptLines() []string {
	if !w.hasLines {
		return nil
	}
	return append([]string(nil), w.raw...)
}

// feedFollowers passes lines to the open followers and forgets the closed
// ones.
func (w *Widget) feedFollowers(lines []string) {
	kept := w.followers[:0]
	for _, f := range w.followers {
		if f.Closed() {
			continue
		}
		f.add(lines)
		kept = append(kept, f)
	}
	clear(w.followers[len(kept):])
	w.followers = kept
}

// endFollowers ends every follower: the panel's stream ended, failed, or
// moved to another target.
func (w *Widget) endFollowers(note string, err error) {
	for _, f := range w.followers {
		f.finish(note, err)
	}
	w.followers = nil
}
