package pagedfile_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stokaro/kc/internal/pagedfile"
)

// memSource is a file in memory that can change while it is open. It
// records the offset of every read.
type memSource struct {
	mu    sync.Mutex
	data  []byte
	reads []int64
}

func (m *memSource) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads = append(m.reads, off)
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *memSource) Size() (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.data)), true
}

func (m *memSource) Close() error { return nil }

func (m *memSource) set(data []byte) {
	m.mu.Lock()
	m.data = data
	m.mu.Unlock()
}

// loader reads the blocks a document names as missing, as the viewer does.
type loader struct {
	t    *testing.T
	doc  *pagedfile.Document
	src  *memSource
	read int
}

func (l *loader) fill(missing []int64) {
	l.t.Helper()
	bs := l.doc.BlockSize()
	for _, b := range missing {
		buf := make([]byte, bs)
		n, err := l.src.ReadAt(l.t.Context(), buf, b*bs)
		if err != nil && !errors.Is(err, io.EOF) {
			l.t.Fatal(err)
		}
		size, _ := l.src.Size()
		l.doc.Put(b, buf[:n], size)
		l.read++
	}
}

func (l *loader) line(s int64) (string, int64) {
	l.t.Helper()
	for range 10 {
		text, next, missing := l.doc.Line(s)
		if missing == nil {
			return string(text), next
		}
		l.fill(missing)
	}
	l.t.Fatalf("line at %d stays missing", s)
	return "", 0
}

func (l *loader) prev(s int64) int64 {
	l.t.Helper()
	for range 10 {
		p, missing := l.doc.Prev(s)
		if missing == nil {
			return p
		}
		l.fill(missing)
	}
	l.t.Fatalf("line before %d stays missing", s)
	return 0
}

// refStarts finds the line starts byte by byte, by the rule in lines.go.
func refStarts(data []byte, split int) []int64 {
	var starts []int64
	trueStart := 0
	for q := range data {
		if q == 0 || data[q-1] == '\n' {
			starts = append(starts, int64(q))
			trueStart = q
			continue
		}
		if q%split == 0 && data[q] != '\n' && q-trueStart >= split {
			starts = append(starts, int64(q))
		}
	}
	return starts
}

func refLines(data []byte, split int) []string {
	starts := refStarts(data, split)
	out := make([]string, 0, len(starts))
	for i, s := range starts {
		end := int64(len(data))
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		line := strings.TrimSuffix(string(data[s:end]), "\n")
		out = append(out, strings.TrimSuffix(line, "\r"))
	}
	return out
}

var smallOpts = pagedfile.Options{BlockSize: 64, SplitBytes: 8, MaxBlocks: 4}

func randomText(r *rand.Rand, n int) []byte {
	alphabet := []byte("abcdefgh \t\n\n\r")
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[r.IntN(len(alphabet))]
	}
	// Some long lines, to cross split points and block boundaries.
	for range 3 {
		at := r.IntN(n)
		for i := at; i < min(n, at+r.IntN(90)); i++ {
			out[i] = 'x'
		}
	}
	return out
}

func walk(t *testing.T, data []byte, opts pagedfile.Options) (forward, backward []int64, texts []string, l *loader) {
	t.Helper()
	src := &memSource{data: data}
	doc := pagedfile.NewDocument(int64(len(data)), opts)
	l = &loader{t: t, doc: doc, src: src}
	for s := int64(0); s < doc.Size(); {
		text, next := l.line(s)
		forward = append(forward, s)
		texts = append(texts, text)
		if next <= s {
			t.Fatalf("next %d after %d", next, s)
		}
		s = next
	}
	for s := doc.Size(); s > 0; {
		s = l.prev(s)
		backward = append(backward, s)
	}
	slices.Reverse(backward)
	return forward, backward, texts, l
}

func TestDocumentLinesMatchTheRule(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2)) // #nosec G404 -- test data
	cases := map[string][]byte{
		"crlf":                []byte("one\r\ntwo\r\n\r\nthree"),
		"no trailing newline": []byte("alpha\nbeta"),
		"trailing newline":    []byte("alpha\nbeta\n"),
		"only newlines":       []byte("\n\n\n"),
		"one long line":       bytes.Repeat([]byte("y"), 300),
		"long line then text": append(bytes.Repeat([]byte("z"), 77), []byte("\nend\n")...),
		"line across a block": append(bytes.Repeat([]byte("a\n"), 30), []byte("spanning-the-block-boundary\nok")...),
	}
	for i := range 20 {
		cases["random "+string(rune('a'+i))] = randomText(r, 50+r.IntN(700))
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			split := smallOpts.SplitBytes
			want := refStarts(data, split)
			forward, backward, texts, l := walk(t, data, smallOpts)
			if !slices.Equal(forward, want) {
				t.Fatalf("forward starts\n got %v\nwant %v", forward, want)
			}
			if !slices.Equal(backward, want) {
				t.Fatalf("backward starts\n got %v\nwant %v", backward, want)
			}
			if wantText := refLines(data, split); !slices.Equal(texts, wantText) {
				t.Fatalf("texts\n got %q\nwant %q", texts, wantText)
			}
			for _, line := range texts {
				if len(line) >= 2*split {
					t.Fatalf("line %q is longer than two split sizes", line)
				}
			}
			if total, ok := l.doc.Lines(); !ok || total != int64(len(want)) {
				t.Fatalf("Lines() = %d, %v; want %d", total, ok, len(want))
			}
			for i, s := range want {
				// LineNumber needs the block cached; read it again.
				l.line(s)
				if n, ok := l.doc.LineNumber(s); !ok || n != int64(i) {
					t.Fatalf("LineNumber(%d) = %d, %v; want %d", s, n, ok, i)
				}
			}
		})
	}
}

func TestDocumentEmptyFile(t *testing.T) {
	t.Parallel()
	doc := pagedfile.NewDocument(0, smallOpts)
	text, next, missing := doc.Line(0)
	if text != nil || next != 0 || missing != nil {
		t.Fatalf("Line(0) = %q, %d, %v", text, next, missing)
	}
	if p, missing := doc.Prev(0); p != 0 || missing != nil {
		t.Fatalf("Prev(0) = %d, %v", p, missing)
	}
}

func TestCounterMatchesTheRule(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(3, 4)) // #nosec G404 -- test data
	for range 50 {
		data := randomText(r, 1+r.IntN(500))
		want := int64(len(refStarts(data, 8)))
		c := pagedfile.NewCounter()
		// Feed in uneven pieces: the count must not depend on them.
		for rest := data; len(rest) > 0; {
			n := min(len(rest), 1+r.IntN(20))
			c.Feed(rest[:n], 8)
			rest = rest[n:]
		}
		if c.Lines() != want {
			t.Fatalf("Counter counted %d lines in %q, want %d", c.Lines(), data, want)
		}
	}
}

func TestDocumentCacheStaysBounded(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range 200000 {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%70))
		b.WriteByte('\n')
	}
	data := []byte(b.String()) // about 7.6 MiB
	opts := pagedfile.Options{}
	src := &memSource{data: data}
	doc := pagedfile.NewDocument(int64(len(data)), opts)
	l := &loader{t: t, doc: doc, src: src}
	limit := int64(pagedfile.DefaultMaxBlocks * pagedfile.DefaultBlockSize)
	lines := 0
	for s := int64(0); s < doc.Size(); lines++ {
		_, s = l.line(s)
		if idx, size := doc.CachedBlocks(); len(idx) > pagedfile.DefaultMaxBlocks || size > limit {
			t.Fatalf("cache holds %d blocks, %d bytes; limit %d blocks", len(idx), size, pagedfile.DefaultMaxBlocks)
		}
	}
	if lines != 200000 {
		t.Fatalf("walked %d lines, want 200000", lines)
	}
	blocks := (len(data) + pagedfile.DefaultBlockSize - 1) / pagedfile.DefaultBlockSize
	if l.read != blocks {
		t.Fatalf("read %d blocks for a walk from the top, want each of the %d once", l.read, blocks)
	}
	if total, ok := doc.Lines(); !ok || total != 200000 {
		t.Fatalf("Lines() = %d, %v after the walk", total, ok)
	}
	if n, ok := doc.LineNumber(doc.Size() - 1 - int64(len("line "+strings.Repeat("x", 199999%70)))); !ok || n != 199999 {
		t.Fatalf("LineNumber of the last line = %d, %v", n, ok)
	}
}

func TestDocumentFollowsAFileThatChanges(t *testing.T) {
	t.Parallel()
	src := &memSource{data: []byte("a\nb\n")}
	doc := pagedfile.NewDocument(4, smallOpts)
	l := &loader{t: t, doc: doc, src: src}
	if text, _ := l.line(2); text != "b" {
		t.Fatalf("line 2 = %q", text)
	}

	t.Run("grows", func(t *testing.T) {
		src.set([]byte("a\nb\nc\n" + strings.Repeat("d\n", 40)))
		// A read of another block reports the new size; the short block
		// cached before is then stale and read again.
		size, _ := src.Size()
		doc.SetSize(size)
		text, next := l.line(2)
		if text != "b" || next != 4 {
			t.Fatalf("line 2 = %q, next %d", text, next)
		}
		if text, _ := l.line(4); text != "c" {
			t.Fatalf("line 4 = %q after the file grew", text)
		}
		if p := l.prev(doc.Size()); p != size-2 {
			t.Fatalf("last line starts at %d, want %d", p, size-2)
		}
	})

	t.Run("shrinks", func(t *testing.T) {
		src.set([]byte("a\n"))
		buf := make([]byte, doc.BlockSize())
		n, _ := src.ReadAt(t.Context(), buf, 0)
		doc.Put(0, buf[:n], 2)
		if doc.Size() != 2 {
			t.Fatalf("size = %d after a short block, want 2", doc.Size())
		}
		if _, next := l.line(0); next != 2 {
			t.Fatalf("next after line 0 = %d, want the end", next)
		}
		if text, next, missing := doc.Line(40); text != nil || next != 2 || missing != nil {
			t.Fatalf("a line past the end = %q, %d, %v", text, next, missing)
		}
		if idx, _ := doc.CachedBlocks(); slices.ContainsFunc(idx, func(b int64) bool { return b > 0 }) {
			t.Fatalf("blocks past the end stay cached: %v", idx)
		}
	})
}

func substring(needle string) pagedfile.MatchFunc {
	re := regexp.MustCompile(regexp.QuoteMeta(needle))
	return func(text []byte) [][2]int {
		var out [][2]int
		for _, m := range re.FindAllIndex(text, -1) {
			out = append(out, [2]int{m[0], m[1]})
		}
		return out
	}
}

func TestSearchAcrossBlocks(t *testing.T) {
	t.Parallel()
	// Block size 64: "needle" in line 3 straddles offset 64, and lines
	// 0 and 5 hold one each.
	data := []byte("needle zero\n" + strings.Repeat("-", 40) + "\n" + "xxxxx\n" + "abneedle\n" + "plain\n" + "last needle")
	if i := strings.Index(string(data), "abneedle"); i+2 >= 64 || i+8 <= 64 {
		t.Fatalf("the middle needle at %d does not straddle a block", i+2)
	}
	src := &memSource{data: data}
	line3 := int64(strings.Index(string(data), "abneedle"))
	line5 := int64(strings.Index(string(data), "last needle"))
	tests := []struct {
		name      string
		line      int64
		col       int
		backwards bool
		wrap      bool
		want      pagedfile.Match
		wantErr   error
	}{
		{name: "first", want: pagedfile.Match{Line: 0, Start: 0, End: 6}},
		{name: "after the first", col: 1, want: pagedfile.Match{Line: line3, Start: 2, End: 8}},
		{name: "after the middle", line: line3, col: 3, want: pagedfile.Match{Line: line5, Start: 5, End: 11}},
		{name: "past the last", line: line5, col: 6, wantErr: pagedfile.ErrNoMatch},
		{name: "past the last, wrapping", line: line5, col: 6, wrap: true, want: pagedfile.Match{Line: 0, Start: 0, End: 6}},
		{name: "backward from the end", line: line5, col: 1 << 30, backwards: true, want: pagedfile.Match{Line: line5, Start: 5, End: 11}},
		{name: "backward before the last", line: line5, col: 5, backwards: true, want: pagedfile.Match{Line: line3, Start: 2, End: 8}},
		{name: "backward before the first", backwards: true, wantErr: pagedfile.ErrNoMatch},
		{name: "backward wrapping", backwards: true, wrap: true, want: pagedfile.Match{Line: line5, Start: 5, End: 11}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var progress atomic.Int64
			got, err := pagedfile.Search(t.Context(), src, pagedfile.SearchRequest{
				Match: substring("needle"), Line: tc.line, Col: tc.col,
				Backwards: tc.backwards, Wrap: tc.wrap, Options: smallOpts, Progress: &progress,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("match = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSearchReadsInBlocks(t *testing.T) {
	t.Parallel()
	data := []byte(strings.Repeat("hay\n", 100000) + "needle\n")
	src := &memSource{data: data}
	var progress atomic.Int64
	got, err := pagedfile.Search(t.Context(), src, pagedfile.SearchRequest{Match: substring("needle"), Progress: &progress})
	if err != nil || got.Line != 400000 {
		t.Fatalf("match = %+v, %v", got, err)
	}
	for _, off := range src.reads {
		if off%pagedfile.DefaultBlockSize != 0 {
			t.Fatalf("read at %d, not at a block start", off)
		}
	}
	if len(src.reads) != 2 {
		t.Fatalf("read %d blocks for a 400 KB file, want 2", len(src.reads))
	}
	if progress.Load() < 400000 {
		t.Fatalf("progress = %d", progress.Load())
	}
}

// blockingSource answers the first read and then waits for the context.
type blockingSource struct {
	memSource
	calls atomic.Int32
}

func (b *blockingSource) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	if b.calls.Add(1) > 1 {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return b.memSource.ReadAt(ctx, p, off)
}

func TestSearchStopsWhenCanceled(t *testing.T) {
	t.Parallel()
	src := &blockingSource{}
	src.data = []byte(strings.Repeat("hay\n", 200000))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := pagedfile.Search(ctx, src, pagedfile.SearchRequest{Match: substring("needle")})
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestScanCountsLikeTheDocument(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(5, 6)) // #nosec G404 -- test data
	data := randomText(r, 5000)
	src := &memSource{data: data}
	index, total, err := pagedfile.Scan(t.Context(), src, smallOpts, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := refStarts(data, 8)
	if total != int64(len(want)) {
		t.Fatalf("Scan counted %d lines, want %d", total, len(want))
	}
	doc := pagedfile.NewDocument(int64(len(data)), smallOpts)
	doc.AdoptIndex(index, total)
	l := &loader{t: t, doc: doc, src: src}
	// Jump to lines far from the top: the adopted index numbers them.
	for _, i := range []int{len(want) - 1, len(want) / 2, 7} {
		l.line(want[i])
		if n, ok := doc.LineNumber(want[i]); !ok || n != int64(i) {
			t.Fatalf("LineNumber(%d) = %d, %v; want %d", want[i], n, ok, i)
		}
	}
}
