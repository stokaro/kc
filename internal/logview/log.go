package logview

// Mode selects how records are shown.
type Mode int

const (
	// ModeFormatted shows JSON records as time, level, message and
	// key=value pairs, and text records as they are.
	ModeFormatted Mode = iota
	// ModeRaw shows every record as it arrived.
	ModeRaw
)

// String returns "formatted" or "raw".
func (m Mode) String() string {
	if m == ModeRaw {
		return "raw"
	}
	return "formatted"
}

// Log keeps every record of a stream and the display settings. Each record
// gives at most one display line, so the display can be rebuilt at any time
// from the retained records. A Log is not safe for concurrent use; the
// viewer owns it on the update goroutine.
type Log struct {
	records []Record
	format  Format
	mode    Mode
	query   *Query
	shown   int
}

// NewLog returns an empty log in the formatted view.
func NewLog(format Format) *Log {
	return &Log{format: format}
}

// Append adds lines from src and returns the display lines of those that
// pass the query.
func (l *Log) Append(src Source, lines ...string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		l.records = append(l.records, NewRecord(line, src))
		r := &l.records[len(l.records)-1]
		if !l.query.Match(r) {
			continue
		}
		l.shown++
		out = append(out, l.render(r))
	}
	return out
}

// Lines returns the display lines of all records that pass the query.
func (l *Log) Lines() []string {
	out := make([]string, 0, l.shown)
	for i := range l.records {
		r := &l.records[i]
		if l.query.Match(r) {
			out = append(out, l.render(r))
		}
	}
	return out
}

// Originals returns the original lines of the records that pass the query,
// unchanged (no formatting, no extraction, invalid UTF-8 kept).
func (l *Log) Originals() []string {
	out := make([]string, 0, l.shown)
	for i := range l.records {
		r := &l.records[i]
		if l.query.Match(r) {
			out = append(out, r.Raw)
		}
	}
	return out
}

// Records returns the retained records. The slice is shared; do not modify.
func (l *Log) Records() []Record { return l.records }

// Len returns the number of retained records.
func (l *Log) Len() int { return len(l.records) }

// Shown returns the number of records that pass the query.
func (l *Log) Shown() int { return l.shown }

// Mode returns the current view mode.
func (l *Log) Mode() Mode { return l.mode }

// SetMode changes the view mode. Call Lines to get the new display.
func (l *Log) SetMode(m Mode) { l.mode = m }

// Query returns the current query; nil means none.
func (l *Log) Query() *Query { return l.query }

// SetQuery replaces the query; nil clears it. Call Lines to get the new
// display.
func (l *Log) SetQuery(q *Query) {
	if q.IsZero() {
		q = nil
	}
	l.query = q
	l.shown = 0
	for i := range l.records {
		if q.Match(&l.records[i]) {
			l.shown++
		}
	}
}

// Reset drops all records and keeps the settings, for a restarted stream.
func (l *Log) Reset() {
	l.records = nil
	l.shown = 0
}

func (l *Log) render(r *Record) string {
	if l.mode == ModeRaw {
		return l.format.Raw(r)
	}
	return l.format.Formatted(r, l.query.Extract())
}
