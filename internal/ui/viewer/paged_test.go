package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/pagedfile"
)

// pagedSource is a file in memory that records the blocks read. With gate
// set, every read waits for it or for its context.
type pagedSource struct {
	mu     sync.Mutex
	data   []byte
	reads  []int64
	gate   chan struct{}
	closed bool
}

func (s *pagedSource) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, off)
	if off >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (s *pagedSource) Size() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.data)), true
}

func (s *pagedSource) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *pagedSource) readOffsets() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.reads...)
}

// numberedFile is "line 0\n" ... "line n-1\n".
func numberedFile(n int) []byte {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return []byte(b.String())
}

// drive runs cmd and feeds the widget's messages back until nothing is
// left. Redraw ticks are dropped: they only repaint.
func drive(t *testing.T, w *PagedWidget, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 10000 {
			t.Fatal("the widget keeps issuing commands")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case pagedTickMsg:
		case PagedMsg:
			next, _ := w.Update(msg)
			queue = append(queue, next)
		}
	}
}

func newTestPaged(t *testing.T, src *pagedSource) *PagedWidget {
	t.Helper()
	w := NewPaged(t.Context(), src, int64(len(src.data)), Metadata{Title: "/var/log/big.log", Filename: "big.log"}, pagedfile.Options{})
	t.Cleanup(w.Close)
	w.Resize(80, 10)
	drive(t, w, w.Start())
	return w
}

func pressPaged(t *testing.T, w *PagedWidget, k string) {
	t.Helper()
	cmd, handled := w.Update(key(k))
	if !handled {
		t.Fatalf("key %q not handled", k)
	}
	drive(t, w, cmd)
}

func screen(w *PagedWidget) []string {
	view := ansi.Strip(w.View(Frame{Width: 80, Height: 10}))
	lines := strings.Split(view, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

func TestPagedShowsTopMiddleAndEnd(t *testing.T) {
	t.Parallel()
	const n = 300000 // about 3.5 MiB, 14 blocks
	src := &pagedSource{data: numberedFile(n)}
	w := newTestPaged(t, src)

	if got := screen(w); got[0] != "line 0" || got[9] != "line 9" {
		t.Fatalf("top page = %q", got)
	}
	if status := w.StatusText(); !strings.Contains(status, "paged, 3.") || !strings.Contains(status, "highlighting off") || !strings.Contains(status, "line 1") {
		t.Fatalf("status = %q", status)
	}

	pressPaged(t, w, "pgdown")
	if got := screen(w); got[0] != "line 10" {
		t.Fatalf("after PgDn the page starts at %q", got[0])
	}
	pressPaged(t, w, "down")
	pressPaged(t, w, "pgup")
	if got := screen(w); got[0] != "line 1" {
		t.Fatalf("after Down and PgUp the page starts at %q", got[0])
	}

	before := len(src.readOffsets())
	pressPaged(t, w, "end")
	got := screen(w)
	if got[9] != fmt.Sprintf("line %d", n-1) || got[0] != fmt.Sprintf("line %d", n-10) {
		t.Fatalf("end page = %q", got)
	}
	// End reads the last blocks, not the ones between.
	for _, off := range src.readOffsets()[before:] {
		if off < int64(len(src.data))-3*pagedfile.DefaultBlockSize {
			t.Fatalf("End read the block at %d", off)
		}
	}
	// The lines before are not counted: the position is approximate.
	if status := w.StatusText(); !strings.Contains(status, "~") {
		t.Fatalf("status at the end = %q, want an approximate position", status)
	}
	pressPaged(t, w, "down")
	if got := screen(w); got[9] != fmt.Sprintf("line %d", n-1) {
		t.Fatalf("Down at the end moved past it: %q", got)
	}

	pressPaged(t, w, "home")
	if got := screen(w); got[0] != "line 0" {
		t.Fatalf("Home shows %q", got[0])
	}
}

func TestPagedSearchJumpsToTheMiddle(t *testing.T) {
	t.Parallel()
	src := &pagedSource{data: numberedFile(300000)}
	w := newTestPaged(t, src)
	setPagedSearch(t, w, "line 150000\n", SearchOptions{Mode: SearchRegex})
	if w.Searching() {
		t.Fatal("the search did not finish")
	}
	got := screen(w)
	if !slices.Contains(got, "line 150000") {
		t.Fatalf("the match is not on the page: %q", got)
	}
	if summary := w.searchSummary(); !strings.Contains(summary, "n next") {
		t.Fatalf("summary = %q", summary)
	}
	// n wraps around to the same, only match.
	pressPaged(t, w, "n")
	if !w.hasActive || !slices.Contains(screen(w), "line 150000") {
		t.Fatalf("n lost the match: %q", screen(w))
	}
	setPagedSearch(t, w, "no such text", SearchOptions{})
	if summary := w.searchSummary(); !strings.Contains(summary, "no match") {
		t.Fatalf("summary = %q", summary)
	}
}

// setPagedSearch commits a search through the dialog and runs it.
func setPagedSearch(t *testing.T, w *PagedWidget, query string, opts SearchOptions) {
	t.Helper()
	pressPaged(t, w, "f7")
	// A trailing newline in query anchors it at the end of a line.
	if q, ok := strings.CutSuffix(query, "\n"); ok {
		query = q + "$"
	}
	w.searchDialog.input.SetValue(query)
	w.searchDialog.opts = opts
	cmd, _ := w.Update(key("enter"))
	drive(t, w, cmd)
}

func TestPagedSearchIsCanceledWithEsc(t *testing.T) {
	t.Parallel()
	src := &pagedSource{data: numberedFile(300000)}
	w := newTestPaged(t, src)
	src.mu.Lock()
	src.gate = make(chan struct{}) // later reads wait
	src.mu.Unlock()

	pressPaged(t, w, "f7")
	w.searchDialog.input.SetValue("not there")
	cmd, _ := w.Update(key("enter"))
	if !w.Searching() || !strings.Contains(w.searchSummary(), "searching") {
		t.Fatalf("no search runs: %q", w.searchSummary())
	}
	results := make(chan tea.Msg, 4)
	go func() {
		for _, c := range flatten(cmd) {
			if msg := c(); msg != nil {
				if _, tick := msg.(pagedTickMsg); !tick {
					results <- msg
				}
			}
		}
		close(results)
	}()
	if _, handled := w.HandleEscape(); !handled || w.Searching() {
		t.Fatal("Esc did not stop the search")
	}
	for msg := range results {
		if m, ok := msg.(pagedSearchMsg); ok && !errors.Is(m.err, context.Canceled) {
			t.Fatalf("the search ended with %v, want context.Canceled", m.err)
		}
		w.Update(msg) // a late result of a canceled search is ignored
	}
	if !strings.Contains(w.searchSummary(), "canceled") {
		t.Fatalf("summary = %q", w.searchSummary())
	}
}

func flatten(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	return []tea.Cmd{func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c == nil {
					continue
				}
				if m := c(); m != nil {
					if _, tick := m.(pagedTickMsg); !tick {
						return m
					}
				}
			}
			return nil
		}
		return msg
	}}
}

func TestPagedCacheStaysBoundedWhileScrolling(t *testing.T) {
	t.Parallel()
	src := &pagedSource{data: numberedFile(600000)} // about 7.5 MiB
	w := newTestPaged(t, src)
	limit := int64(pagedfile.DefaultMaxBlocks * pagedfile.DefaultBlockSize)
	for range 600000 / 10 {
		pressPaged(t, w, "pgdown")
		if idx, size := w.Document().CachedBlocks(); len(idx) > pagedfile.DefaultMaxBlocks || size > limit {
			t.Fatalf("the cache holds %d blocks, %d bytes", len(idx), size)
		}
	}
	if got := screen(w); got[9] != "line 599999" {
		t.Fatalf("after scrolling through the file the page ends with %q", got[9])
	}
	// Read from the top in order, the index numbers every line.
	if status := w.StatusText(); !strings.Contains(status, "line 599991/600000") {
		t.Fatalf("status = %q", status)
	}
}

func TestPagedShowsAPlaceholderWhileABlockIsRead(t *testing.T) {
	t.Parallel()
	src := &pagedSource{data: numberedFile(1000), gate: make(chan struct{})}
	w := NewPaged(t.Context(), src, int64(len(src.data)), Metadata{}, pagedfile.Options{})
	w.Resize(80, 10)
	cmd := w.Start()
	if got := screen(w); !strings.Contains(got[0], "reading") {
		t.Fatalf("pending page = %q", got)
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	w.Close()
	msg := <-done
	if m, ok := msg.(pagedBlockMsg); !ok || !errors.Is(m.err, context.Canceled) {
		t.Fatalf("the read after Close ended with %#v, want context.Canceled", msg)
	}
	if !src.closed {
		t.Fatal("Close left the source open")
	}
	if next, _ := w.Update(msg); next != nil {
		t.Fatal("a closed widget asked for more reads")
	}
}

func TestPagedRefusesABinaryFile(t *testing.T) {
	t.Parallel()
	data := append([]byte("\x7fELF\x00\x00"), make([]byte, 5<<20)...)
	w := newTestPaged(t, &pagedSource{data: data})
	if got := screen(w); !strings.Contains(got[0], "big.log is a binary file") {
		t.Fatalf("page = %q", got)
	}
}

func TestPagedEmptyAndShortFiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data string
		want []string
	}{
		{name: "empty", data: "", want: []string{""}},
		{name: "crlf", data: "a\r\nb\r\n", want: []string{"a", "b", ""}},
		{name: "no trailing newline", data: "a\nb", want: []string{"a", "b", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newTestPaged(t, &pagedSource{data: []byte(tc.data)})
			got := screen(w)
			for i, want := range tc.want {
				if got[i] != want {
					t.Fatalf("page = %q, want %q first", got, tc.want)
				}
			}
			pressPaged(t, w, "end")
			pressPaged(t, w, "pgdown")
		})
	}
}
