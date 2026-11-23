package pagedfile

import "bytes"

// A line of a paged file ends after a newline, or at a split point: an
// offset that is a multiple of the split size with no newline in the split
// size bytes before it. The rule needs only the bytes around an offset, so
// lines can be found from any position in either direction, and the same
// line starts come out whether the file is read from the top, from the end
// or from the middle. A line is therefore at most twice the split size.
//
// A trailing newline does not start an empty last line, and a split point
// never falls on a newline, so a line never shows as a lone "\n".

// window is a run of file bytes: data[i] is the byte at offset base+i.
// size is the size of the file the bytes were read from; split is the split
// size. Each method names the bytes it needs; the caller provides them.
type window struct {
	base  int64
	data  []byte
	size  int64
	split int64
}

func (w window) end() int64 { return w.base + int64(len(w.data)) }

func (w window) at(q int64) byte { return w.data[q-w.base] }

func (w window) slice(from, to int64) []byte { return w.data[from-w.base : to-w.base] }

// isStart reports whether a line starts at q. It needs [q-split, q].
func (w window) isStart(q int64) bool {
	switch {
	case q == 0:
		return w.size > 0
	case q < 0 || q >= w.size:
		return false
	case w.at(q-1) == '\n':
		return true
	case q%w.split != 0 || w.at(q) == '\n':
		return false
	}
	lo := max(q-w.split, w.base)
	return bytes.IndexByte(w.slice(lo, q), '\n') < 0
}

// firstStartFrom returns the first line start at or after q, or the file
// size when no line starts there. It needs [q-1-split, q-1+2*split].
func (w window) firstStartFrom(q int64) int64 {
	if q <= 0 {
		if w.size > 0 {
			return 0
		}
		return w.size
	}
	if q >= w.size {
		return w.size
	}
	next := w.size
	if hi := min(w.end(), q-1+2*w.split); hi > q-1 {
		if i := bytes.IndexByte(w.slice(q-1, hi), '\n'); i >= 0 {
			next = min(q+int64(i), w.size)
		}
	}
	for g := ceilMultiple(q, w.split); g < next && g < w.end(); g += w.split {
		if w.isStart(g) {
			return g
		}
	}
	return next
}

// nextStart returns the start of the line after the one that starts at s,
// or the file size for the last line. It needs [s-split, s+2*split].
func (w window) nextStart(s int64) int64 { return w.firstStartFrom(s + 1) }

// prevStart returns the start of the line before s, for 0 < s <= size. It
// needs [s-3*split-1, s).
func (w window) prevStart(s int64) int64 {
	if s <= 0 {
		return 0
	}
	lo := max(0, s-2*w.split)
	from := max(lo-1, w.base, 0)
	found := int64(0)
	if i := bytes.LastIndexByte(w.slice(from, s-1), '\n'); i >= 0 {
		found = from + int64(i) + 1
	}
	for g := floorMultiple(s-1, w.split); g > found && g >= lo; g -= w.split {
		if w.isStart(g) {
			return g
		}
	}
	return found
}

// lineText returns the text of the line [s, next) without its line break.
func (w window) lineText(s, next int64) []byte {
	b := w.slice(s, next)
	b = bytes.TrimSuffix(b, []byte{'\n'})
	return bytes.TrimSuffix(b, []byte{'\r'})
}

func ceilMultiple(q, m int64) int64 {
	if r := q % m; r != 0 {
		return q + m - r
	}
	return q
}

func floorMultiple(q, m int64) int64 { return q - q%m }

// Counter counts line starts in a stream of file bytes read from offset 0.
// It finds the same starts as the window rule above without looking back,
// so a file can be counted block by block. The zero value is not ready;
// use NewCounter.
type Counter struct {
	// pos is the offset of the next byte to feed.
	pos int64
	// lineStart is where the current line began after a newline (or 0).
	lineStart int64
	// pending is set after a newline: the next byte starts a line.
	pending bool
	// lines counts the line starts before pos.
	lines int64
}

// NewCounter returns a counter at offset 0.
func NewCounter() Counter { return Counter{pending: true} }

// Pos returns the offset of the next byte to feed.
func (c Counter) Pos() int64 { return c.pos }

// Lines returns the number of lines that start before Pos.
func (c Counter) Lines() int64 { return c.lines }

// Feed counts the line starts in data, the bytes at Pos.
func (c *Counter) Feed(data []byte, split int64) {
	i := 0
	for i < len(data) {
		at := c.pos + int64(i)
		if c.pending {
			c.lines++
			c.pending = false
			c.lineStart = at
		}
		j := bytes.IndexByte(data[i:], '\n')
		runEnd := c.pos + int64(len(data))
		if j >= 0 {
			runEnd = at + int64(j)
		}
		// Split points inside the run of non-newline bytes [at, runEnd).
		for g := ceilMultiple(max(at, c.lineStart+split), split); g < runEnd; g += split {
			c.lines++
		}
		if j < 0 {
			break
		}
		i += j + 1
		c.pending = true
	}
	c.pos += int64(len(data))
}
