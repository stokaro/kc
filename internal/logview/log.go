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

// Log keeps the recent records of a stream, within its Limits, and the
// display settings. Each record gives at most one display line, so the
// display can be rebuilt at any time from the kept records. A Log is not
// safe for concurrent use; the viewer owns it on the update goroutine.
type Log struct {
	// records and shown are parallel: shown[i] says whether records[i]
	// passes the query.
	records []Record
	shown   []bool
	window  *Window
	format  Format
	mode    Mode
	query   *Query
	nShown  int
	// truncated counts the kept records whose line was cut.
	truncated int
	// first is the Seq of records[0].
	first Seq
	// solo, when soloOn, shows only the records of one source. It hides
	// records from the display; it never stops a stream.
	solo   Source
	soloOn bool
}

// NewLog returns an empty log in the formatted view with DefaultLimits.
func NewLog(format Format) *Log {
	return &Log{format: format, window: NewWindow(DefaultLimits())}
}

// SetLimits replaces the limits and drops the oldest records that no longer
// fit. It returns how many display lines were dropped from the front.
func (l *Log) SetLimits(limits Limits) int {
	l.window.limits = limits
	return l.trim()
}

// Limits returns the limits.
func (l *Log) Limits() Limits { return l.window.limits }

// Append adds lines from src and returns the display lines of those that
// pass the query, and how many display lines the caller must drop from the
// front of its display because their records no longer fit the limits.
// The dropped lines can include some of the returned ones when one call
// brings more than the limits hold, so append first, then drop. A line
// longer than ReadLimit is truncated with a notice.
func (l *Log) Append(src Source, lines ...string) (shown []string, dropped int) {
	shown = make([]string, 0, len(lines))
	for _, line := range lines {
		r := NewRecord(TruncateLine(line, ReadLimit), src)
		if IsTruncated(r.Raw) {
			l.truncated++
		}
		match := l.match(&r)
		l.records = append(l.records, r)
		l.shown = append(l.shown, match)
		l.window.Add(Cost(len(r.Raw), r.IsJSON()))
		if !match {
			continue
		}
		l.nShown++
		shown = append(shown, l.render(&l.records[len(l.records)-1]))
	}
	return shown, l.trim()
}

// trim drops the records the window let go and returns how many of them
// were shown.
func (l *Log) trim() int {
	n := l.window.Trim()
	if n == 0 {
		return 0
	}
	dropped := 0
	for i := range n {
		if l.shown[i] {
			dropped++
		}
		if IsTruncated(l.records[i].Raw) {
			l.truncated--
		}
	}
	// Clear the dropped slots so their lines and parsed objects can be
	// collected; the slices lose their front and reallocate as they grow.
	clear(l.records[:n])
	l.first += Seq(n)
	l.records = l.records[n:]
	l.shown = l.shown[n:]
	l.nShown -= dropped
	return dropped
}

// Lines returns the display lines of all records that pass the query.
func (l *Log) Lines() []string {
	out := make([]string, 0, l.nShown)
	for i := range l.records {
		if l.shown[i] {
			out = append(out, l.render(&l.records[i]))
		}
	}
	return out
}

// Originals returns the original lines of the records that pass the query,
// unchanged (no formatting, no extraction, invalid UTF-8 kept). A line
// that was too long ends with its truncation notice.
func (l *Log) Originals() []string {
	out := make([]string, 0, l.nShown)
	for i := range l.records {
		if l.shown[i] {
			out = append(out, l.records[i].Raw)
		}
	}
	return out
}

// Records returns the kept records. The slice is shared; do not modify.
func (l *Log) Records() []Record { return l.records }

// Len returns the number of kept records.
func (l *Log) Len() int { return len(l.records) }

// Shown returns the number of records that pass the query.
func (l *Log) Shown() int { return l.nShown }

// Dropped returns how many older records were dropped to stay within the
// limits since the stream started.
func (l *Log) Dropped() int64 { return l.window.Dropped() }

// Truncated returns how many kept records had their line truncated.
func (l *Log) Truncated() int { return l.truncated }

// Bytes returns the estimated memory of the kept records.
func (l *Log) Bytes() int64 { return l.window.Bytes() }

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
	l.rematch()
}

// Solo returns the source shown alone, and whether one is.
func (l *Log) Solo() (Source, bool) { return l.solo, l.soloOn }

// SetSolo shows only the records of src, or every source again with on
// false. The records of the other sources are kept and stay in the
// limits; clearing Solo shows them again. Call Lines to get the new
// display.
func (l *Log) SetSolo(src Source, on bool) {
	if !on {
		src = Source{}
	}
	l.solo, l.soloOn = src, on
	l.rematch()
}

// match reports whether r passes the query and Solo.
func (l *Log) match(r *Record) bool {
	if l.soloOn && r.Source != l.solo {
		return false
	}
	return l.query.Match(r)
}

// rematch decides again which kept records are shown.
func (l *Log) rematch() {
	l.nShown = 0
	for i := range l.records {
		l.shown[i] = l.match(&l.records[i])
		if l.shown[i] {
			l.nShown++
		}
	}
}

// SetFormat replaces the format. Call Lines to get the new display.
func (l *Log) SetFormat(f Format) { l.format = f }

// Reset drops all records and keeps the settings, for a restarted stream.
func (l *Log) Reset() {
	l.first += Seq(len(l.records))
	l.records = nil
	l.shown = nil
	l.nShown = 0
	l.truncated = 0
	l.window.Reset()
}

func (l *Log) render(r *Record) string {
	if l.mode == ModeRaw {
		return l.format.Raw(r)
	}
	return l.format.Formatted(r, l.query.Extract())
}
