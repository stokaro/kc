package pagedfile

import (
	"context"
	"errors"
	"io"
	"math"
	"sync/atomic"
)

// MatchFunc returns the matches in the text of one line as [start, end)
// pairs, in the order they appear. The positions are the caller's: the
// viewer matches the text without color codes.
type MatchFunc func(text []byte) [][2]int

// Match is a match found by Search: the line that holds it (the offset of
// its start) and its position as MatchFunc returned it.
type Match struct {
	Line       int64
	Start, End int
}

// SearchRequest says where and how Search looks.
type SearchRequest struct {
	Match MatchFunc
	// Line and Col are where the search starts: forward, the first match
	// at or after Col in the line that starts at Line, else in a later
	// line; backward, the last match before Col in that line, else in an
	// earlier line. Line must be a line start.
	Line int64
	Col  int
	// Backwards searches toward the top of the file.
	Backwards bool
	// Wrap goes on from the other end of the file up to the start.
	Wrap bool
	// Options are the sizes of the document the positions refer to.
	Options Options
	// Progress, when set, counts the bytes looked at so far.
	Progress *atomic.Int64
}

// ErrNoMatch is returned when the file holds no match.
var ErrNoMatch = errors.New("no match")

// linesPerCheck is how many lines pass between checks of the context.
const linesPerCheck = 1024

// Search streams over src from req.Line and returns the first match in the
// search direction. It reads one block at a time and keeps at most a few
// blocks in memory, never the whole file. A canceled ctx stops it with the
// context's error.
func Search(ctx context.Context, src Source, req SearchRequest) (Match, error) {
	opts := req.Options.withDefaults()
	size, _ := src.Size()
	r := &streamReader{ctx: ctx, src: src, size: size, chunk: int64(opts.BlockSize), split: int64(opts.SplitBytes)}
	s := &searcher{r: r, match: req.Match, progress: req.Progress}
	start := min(max(req.Line, 0), size)
	var m Match
	var ok bool
	var err error
	if req.Backwards {
		m, ok, err = s.searchBackward(start, req.Col, req.Wrap)
	} else {
		m, ok, err = s.searchForward(start, req.Col, req.Wrap)
	}
	switch {
	case err != nil:
		return Match{}, err
	case !ok:
		return Match{}, ErrNoMatch
	}
	return m, nil
}

func (s *searcher) searchForward(start int64, col int, wrap bool) (Match, bool, error) {
	if m, ok, err := s.forward(start, col, math.MaxInt64, 0); ok || err != nil {
		return m, ok, err
	}
	if !wrap || (start == 0 && col == 0) {
		return Match{}, false, nil
	}
	return s.forward(0, 0, start, col)
}

func (s *searcher) searchBackward(start int64, col int, wrap bool) (Match, bool, error) {
	if m, ok, err := s.backward(start, col, 0, 0); ok || err != nil {
		return m, ok, err
	}
	if !wrap || start >= s.r.size {
		return Match{}, false, nil
	}
	last, err := s.lastLine()
	if err != nil {
		return Match{}, false, err
	}
	return s.backward(last, math.MaxInt, start, col)
}

type searcher struct {
	r        *streamReader
	match    MatchFunc
	progress *atomic.Int64
	lines    int
}

func (s *searcher) tick(bytes int64) error {
	if s.progress != nil {
		s.progress.Add(bytes)
	}
	s.lines++
	if s.lines%linesPerCheck == 0 {
		return s.r.ctx.Err()
	}
	return nil
}

// forward looks at the lines from the one at from (matches at or after
// fromCol) to the one at to (matches before toCol).
func (s *searcher) forward(from int64, fromCol int, to int64, toCol int) (Match, bool, error) {
	r := s.r
	for line := from; line < r.size && line <= to; {
		w, err := r.ensure(line-r.split-1, line+2*r.split+1)
		if err != nil {
			return Match{}, false, err
		}
		if line >= w.size {
			break
		}
		next := w.nextStart(line)
		for _, m := range s.match(w.lineText(line, next)) {
			if line == from && m[0] < fromCol {
				continue
			}
			if line == to && m[0] >= toCol {
				break
			}
			return Match{Line: line, Start: m[0], End: m[1]}, true, nil
		}
		if err := s.tick(next - line); err != nil {
			return Match{}, false, err
		}
		line = next
	}
	return Match{}, false, nil
}

// backward looks at the lines from the one at from (matches before
// fromCol) back to the one at to (matches at or after toCol), and returns
// the last match.
func (s *searcher) backward(from int64, fromCol int, to int64, toCol int) (Match, bool, error) {
	r := s.r
	if from < r.size {
		m, ok, err := s.lastInLine(from, func(m [2]int) bool {
			return m[0] < fromCol && (from != to || m[0] >= toCol)
		})
		if ok || err != nil || from <= to {
			return m, ok, err
		}
	}
	// cur is the start of the first line looked at; the next chunk holds
	// the lines before it.
	cur := min(from, r.size)
	for cur > to {
		lo := max(to, cur-r.chunk)
		w, err := r.ensure(lo-r.split-1, cur+2*r.split+1)
		if err != nil {
			return Match{}, false, err
		}
		first := w.firstStartFrom(lo)
		var found Match
		ok := false
		for line := first; line < cur; {
			next := w.nextStart(line)
			for _, m := range s.match(w.lineText(line, next)) {
				if line == to && m[0] < toCol {
					continue
				}
				found, ok = Match{Line: line, Start: m[0], End: m[1]}, true
			}
			if err := s.tick(next - line); err != nil {
				return Match{}, false, err
			}
			line = next
		}
		if ok {
			return found, true, nil
		}
		cur = first
	}
	return Match{}, false, nil
}

// lastInLine returns the last match in the line at line that keep accepts.
func (s *searcher) lastInLine(line int64, keep func([2]int) bool) (Match, bool, error) {
	r := s.r
	w, err := r.ensure(line-r.split-1, line+2*r.split+1)
	if err != nil {
		return Match{}, false, err
	}
	next := w.nextStart(line)
	var found Match
	ok := false
	for _, m := range s.match(w.lineText(line, next)) {
		if keep(m) {
			found, ok = Match{Line: line, Start: m[0], End: m[1]}, true
		}
	}
	return found, ok, s.tick(next - line)
}

// lastLine returns the start of the last line.
func (s *searcher) lastLine() (int64, error) {
	r := s.r
	w, err := r.ensure(r.size-3*r.split-1, r.size)
	if err != nil {
		return 0, err
	}
	return w.prevStart(r.size), nil
}

// streamReader keeps a run of file bytes read in chunks, the bytes a
// search looks at and a little around them.
type streamReader struct {
	ctx   context.Context
	src   Source
	size  int64
	chunk int64
	split int64
	base  int64
	buf   []byte
}

func (r *streamReader) end() int64 { return r.base + int64(len(r.buf)) }

// ensure returns a window that holds [lo, hi), clipped to the file.
func (r *streamReader) ensure(lo, hi int64) (window, error) {
	lo, hi = max(lo, 0), min(hi, r.size)
	from := floorMultiple(lo, r.chunk)
	if len(r.buf) == 0 || from >= r.end() || hi <= r.base {
		r.base, r.buf = from, r.buf[:0]
	}
	if from < r.base {
		head, err := r.read(from, r.base-from)
		if err != nil {
			return window{}, err
		}
		r.buf = append(head, r.buf...)
		r.base = from
	}
	for r.end() < hi {
		more, err := r.read(r.end(), r.chunk)
		if err != nil {
			return window{}, err
		}
		r.buf = append(r.buf, more...)
		if int64(len(more)) < r.chunk {
			r.size = min(r.size, r.end())
			hi = min(hi, r.size)
			break
		}
	}
	// Keep a chunk on either side at most.
	if drop := floorMultiple(lo-r.base, r.chunk); drop >= r.chunk {
		r.buf = append(r.buf[:0], r.buf[drop:]...)
		r.base += drop
	}
	if keep := ceilMultiple(hi-r.base, r.chunk) + r.chunk; keep < int64(len(r.buf)) {
		r.buf = r.buf[:keep]
	}
	return window{base: r.base, data: r.buf, size: r.size, split: r.split}, nil
}

func (r *streamReader) read(off, n int64) ([]byte, error) {
	n = min(n, r.size-off)
	if n <= 0 {
		return nil, nil
	}
	p := make([]byte, n)
	got := 0
	for got < len(p) {
		k, err := r.src.ReadAt(r.ctx, p[got:], off+int64(got))
		got += k
		if errors.Is(err, io.EOF) || (err == nil && k == 0) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return p[:got], nil
}

// Scan counts the lines of src from the top, one block at a time, and
// returns the index Document.AdoptIndex takes and the number of lines.
// Progress, when set, counts the bytes read.
func Scan(ctx context.Context, src Source, opts Options, progress *atomic.Int64) ([]Counter, int64, error) {
	opts = opts.withDefaults()
	bs, split := int64(opts.BlockSize), int64(opts.SplitBytes)
	size, _ := src.Size()
	r := &streamReader{ctx: ctx, src: src, size: size, chunk: bs, split: split}
	c := NewCounter()
	index := []Counter{c}
	for off := int64(0); off < r.size; off += bs {
		data, err := r.read(off, bs)
		if err != nil {
			return nil, 0, err
		}
		c.Feed(data, split)
		if progress != nil {
			progress.Add(int64(len(data)))
		}
		if int64(len(data)) < bs {
			break
		}
		if c.Pos() < r.size {
			index = append(index, c)
		}
	}
	return index, c.Lines(), nil
}
