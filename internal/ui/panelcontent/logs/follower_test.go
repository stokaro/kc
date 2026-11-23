package logs_test

import (
	"bufio"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// readFollower reads f line by line on its own goroutine, as the
// full-screen viewer does.
type readFollower struct {
	lines chan string
	errs  chan error
}

func startReading(f *logswidget.Follower) *readFollower {
	r := &readFollower{lines: make(chan string, 64), errs: make(chan error, 1)}
	go func() {
		br := bufio.NewReader(f)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				r.lines <- strings.TrimSuffix(line, "\n")
			}
			if err != nil {
				r.errs <- err
				return
			}
		}
	}()
	return r
}

// next returns the next line, failing when none comes in time.
func (r *readFollower) next(t *testing.T) string {
	t.Helper()
	select {
	case line := <-r.lines:
		return line
	case err := <-r.errs:
		t.Fatalf("the follower ended with %v, want a line", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no line from the follower within 5s")
	}
	return ""
}

// end returns the error the follower ended with, after its last lines.
func (r *readFollower) end(t *testing.T) (rest []string, err error) {
	t.Helper()
	for {
		select {
		case line := <-r.lines:
			rest = append(rest, line)
		case err := <-r.errs:
			for {
				select {
				case line := <-r.lines:
					rest = append(rest, line)
				default:
					return rest, err
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the follower did not end within 5s")
		}
	}
}

// TestFollowAdoptsThePanelSession checks that a follower starts with the
// lines the panel keeps and gets the lines the panel reads later, from the
// same stream: nothing is opened again, and closing the follower leaves
// the panel streaming.
func TestFollowAdoptsThePanelSession(t *testing.T) {
	t.Parallel()
	a := newChanBody("a1", "a2")
	src := newFakeSource(map[string]*chanBody{"pod-a": a})
	w := newWidget(t, src)
	read := open(t, w, podItem("ns", "pod-a"))
	read = update(t, w, run(t, read)) // a1
	read = update(t, w, run(t, read)) // a2

	sess, err := w.Follow()
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if !slices.Equal(sess.Lines, []string{"a1", "a2"}) {
		t.Fatalf("Lines = %q, want the kept a1 a2", sess.Lines)
	}
	if sess.Spec.Pod != "pod-a" || sess.Spec.Container != "app" || sess.AllContainers {
		t.Fatalf("session = %+v, want pod-a/app", sess)
	}
	r := startReading(sess.Follower)

	a.lines <- "a3\n"
	read = update(t, w, run(t, read))
	if got := r.next(t); got != "a3" {
		t.Fatalf("follower read %q, want a3", got)
	}
	if pods := src.openedPods(); len(pods) != 1 {
		t.Fatalf("opened %v, want the one panel stream", pods)
	}

	if err := sess.Follower.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.end(t); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed follower ended with %v", err)
	}
	a.lines <- "a4\n"
	update(t, w, run(t, read))
	if src.wasCanceled("pod-a") || !w.Streaming() {
		t.Fatal("closing the follower stopped the panel stream")
	}
	if got := screen(t, w); !strings.Contains(got, "a4") {
		t.Fatalf("the panel lost its later lines:\n%s", got)
	}
}

// TestFollowEndsWithThePanelStream checks the ends of a follower: the
// panel's stream ends, or the panel moves to another target, which the
// follower says before it ends.
func TestFollowEndsWithThePanelStream(t *testing.T) {
	t.Parallel()

	t.Run("stream ends", func(t *testing.T) {
		t.Parallel()
		a := newChanBody("a1")
		src := newFakeSource(map[string]*chanBody{"pod-a": a})
		w := newWidget(t, src)
		read := open(t, w, podItem("ns", "pod-a"))
		read = update(t, w, run(t, read))
		sess, err := w.Follow()
		if err != nil {
			t.Fatal(err)
		}
		r := startReading(sess.Follower)
		a.lines <- "a2\n"
		close(a.lines)
		read = update(t, w, run(t, read)) // a2
		if cmd := update(t, w, run(t, read)); cmd != nil {
			cmd()
		}
		rest, err := r.end(t)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("follower ended with %v, want EOF", err)
		}
		if !slices.Equal(rest, []string{"a2", "[the log stream ended]"}) {
			t.Fatalf("last lines %q, want a2 and the end marker", rest)
		}
		// A follower of an ended session gets its lines and ends at once.
		late, err := w.Follow()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := startReading(late.Follower).end(t); !errors.Is(err, io.EOF) {
			t.Fatalf("late follower ended with %v, want EOF", err)
		}
	})

	t.Run("another target", func(t *testing.T) {
		t.Parallel()
		src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody("a1"), "pod-b": newChanBody("b1")})
		w := newWidget(t, src)
		update(t, w, run(t, open(t, w, podItem("ns", "pod-a"))))
		sess, err := w.Follow()
		if err != nil {
			t.Fatal(err)
		}
		r := startReading(sess.Follower)
		open(t, w, podItem("ns", "pod-b"))
		rest, err := r.end(t)
		if !errors.Is(err, io.EOF) || len(rest) != 1 || !strings.Contains(rest[0], "stopped this stream") {
			t.Fatalf("follower ended with %q, %v; want the stop note and EOF", rest, err)
		}
	})

	t.Run("no session", func(t *testing.T) {
		t.Parallel()
		w := newWidget(t, newFakeSource(nil))
		if w.HasSession() {
			t.Fatal("a panel showing the hint has no session")
		}
		if _, err := w.Follow(); !errors.Is(err, logswidget.ErrNoSession) {
			t.Fatalf("Follow = %v, want ErrNoSession", err)
		}
	})
}

// TestFollowerSkipsWhatItCannotHold checks the bound: a reader that falls
// behind loses the oldest unread lines and reads how many, and the panel
// never waits for it.
func TestFollowerSkipsWhatItCannotHold(t *testing.T) {
	t.Parallel()
	const extra = 5
	lines := make([]string, logswidget.FollowerQueue+extra)
	for i := range lines {
		lines[i] = "line"
	}
	lines[extra] = "first kept"
	a := newChanBody()
	src := newFakeSource(map[string]*chanBody{"pod-a": a})
	w := newWidget(t, src)
	read := open(t, w, podItem("ns", "pod-a"))
	sess, err := w.Follow()
	if err != nil {
		t.Fatal(err)
	}
	// Nothing reads the follower while the panel reads every line; the
	// fake body yields one line per read.
	for _, l := range lines {
		a.lines <- l + "\n"
		read = update(t, w, run(t, read))
	}
	r := startReading(sess.Follower)
	if got := r.next(t); !strings.Contains(got, "5 lines skipped") {
		t.Fatalf("first line %q, want the skip note", got)
	}
	if got := r.next(t); got != "first kept" {
		t.Fatalf("second line %q, want the oldest line kept", got)
	}
}
