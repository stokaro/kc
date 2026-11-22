package logview

import (
	"bufio"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits bound what a log view keeps (see docs/adr/0179-bounded-log-retention.md).
// When a view holds more, it drops its oldest records. A zero field means
// no bound on that measure.
type Limits struct {
	// MaxRecords bounds the number of records.
	MaxRecords int
	// MaxBytes bounds the estimated memory of the records, as Cost counts
	// it: the line, its parsed JSON, its display line and their overhead.
	MaxBytes int64
}

// Default limits of one log view.
const (
	DefaultMaxRecords       = 100_000
	DefaultMaxBytes   int64 = 64 << 20
)

// DefaultLimits returns the limits a view has unless the configuration
// sets others.
func DefaultLimits() Limits {
	return Limits{MaxRecords: DefaultMaxRecords, MaxBytes: DefaultMaxBytes}
}

// MaxLineBytes is the longest line a log stream reader keeps. The rest of a
// longer line is read and dropped, and a notice is put in its place, so one
// line cannot take more memory than this.
const MaxLineBytes = MaxParseBytes

// ReadLimit is the longest line a view keeps: MaxLineBytes plus room for a
// merged log's `[pod/container]` prefix and the truncation notice, so a
// line the mux truncated is not truncated again.
const ReadLimit = MaxLineBytes + 4<<10

// recordOverhead estimates what a record costs besides its text: the Record
// value, the string headers of the display copies and the slots of the
// slices that hold them.
const recordOverhead = 256

// Cost estimates the memory a record of n bytes takes in a view: the line,
// its display line (counted as a copy even when it shares the line) and,
// for a JSON object, its parsed form, which takes about the size of the
// line again.
func Cost(n int, isJSON bool) int64 {
	c := int64(recordOverhead) + 2*int64(n)
	if isJSON {
		c += int64(n)
	}
	return c
}

// hysteresis is the part of a limit that is freed when the limit is passed,
// so a full view drops its oldest records in batches rather than one per
// new line.
const hysteresis = 10

// Window accounts for the records of a view, oldest first, and decides how
// many of the oldest to drop. The zero Window has no limits. It is not safe
// for concurrent use.
type Window struct {
	limits  Limits
	costs   []int64
	bytes   int64
	dropped int64
}

// NewWindow returns an empty window with limits.
func NewWindow(limits Limits) *Window {
	return &Window{limits: limits}
}

// Limits returns the window's limits.
func (w *Window) Limits() Limits { return w.limits }

// Add accounts for a new record of cost.
func (w *Window) Add(cost int64) {
	w.costs = append(w.costs, cost)
	w.bytes += cost
}

// Trim forgets the oldest records while a limit is passed and returns how
// many it forgot; the caller drops the same number of its oldest records.
// Once a limit is passed it drops until the window is a tenth below both
// limits, so the next call with one more record drops nothing.
func (w *Window) Trim() int {
	maxRecords, maxBytes := w.limits.MaxRecords, w.limits.MaxBytes
	over := (maxRecords > 0 && len(w.costs) > maxRecords) || (maxBytes > 0 && w.bytes > maxBytes)
	if !over {
		return 0
	}
	keepRecords := len(w.costs)
	if maxRecords > 0 {
		keepRecords = min(keepRecords, maxRecords-maxRecords/hysteresis)
	}
	keepBytes := w.bytes
	if maxBytes > 0 {
		keepBytes = min(keepBytes, maxBytes-maxBytes/hysteresis)
	}
	n := 0
	for n < len(w.costs) && (len(w.costs)-n > keepRecords || w.bytes > keepBytes) {
		w.bytes -= w.costs[n]
		n++
	}
	w.costs = w.costs[n:]
	w.dropped += int64(n)
	return n
}

// Len returns the number of records kept.
func (w *Window) Len() int { return len(w.costs) }

// Bytes returns the estimated memory of the records kept.
func (w *Window) Bytes() int64 { return w.bytes }

// Dropped returns how many records were dropped since the last Reset.
func (w *Window) Dropped() int64 { return w.dropped }

// Reset forgets every record, for a restarted stream.
func (w *Window) Reset() {
	w.costs = nil
	w.bytes = 0
	w.dropped = 0
}

// TruncateLine cuts line to limit bytes, at a UTF-8 boundary, and appends a
// notice that names the original length. A line within limit is returned
// as it is.
func TruncateLine(line string, limit int) string {
	if len(line) <= limit {
		return line
	}
	return cutAt(line, limit) + truncationNotice(len(line), limit)
}

const truncationMark = " [kc] line truncated: "

func truncationNotice(total, limit int) string {
	return truncationMark + strconv.Itoa(total) + " bytes, the first " + strconv.Itoa(limit) + " kept"
}

// IsTruncated reports whether line ends with the notice of TruncateLine or
// ReadLine.
func IsTruncated(line string) bool {
	if !strings.HasSuffix(line, " kept") {
		return false
	}
	_, rest, found := strings.CutLast(line, truncationMark)
	return found && !strings.ContainsRune(rest, '[')
}

// cutAt returns the longest prefix of s within limit bytes that does not end
// inside a UTF-8 sequence. len(s) must be greater than limit.
func cutAt(s string, limit int) string {
	cut := limit
	for back := 0; back < utf8.UTFMax && cut > 0 && !utf8.RuneStart(s[cut]); back++ {
		cut--
	}
	if !utf8.RuneStart(s[cut]) {
		cut = limit // not valid UTF-8 there; cut at the byte limit
	}
	return s[:cut]
}

// ReadLine reads one line like r.ReadString('\n') but keeps at most limit
// bytes of it, not counting the line break. The rest of a longer line is
// read and dropped, the kept part ends with a notice, and the line break,
// when there is one, follows it. Memory use is bounded by limit and r's
// buffer, however long the line is.
func ReadLine(r *bufio.Reader, limit int) (string, error) {
	var kept []byte
	total := 0
	for {
		chunk, err := r.ReadSlice('\n')
		total += len(chunk)
		if room := limit + 1 - len(kept); room > 0 {
			kept = append(kept, chunk[:min(room, len(chunk))]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		newline := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		size := total
		if newline {
			size--
		}
		if size <= limit {
			return string(kept), err
		}
		line := cutAt(string(kept), limit) + truncationNotice(size, limit)
		if newline {
			line += "\n"
		}
		return line, err
	}
}
