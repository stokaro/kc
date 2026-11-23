package logview

import "strings"

// Severity is the level a record states, as the formatted view reads it.
type Severity int

const (
	// SeverityUnknown is a record without a level field: a text line or a
	// JSON object with none of the level keys or an unknown value. kc does
	// not guess a level from the text.
	SeverityUnknown Severity = iota
	// SeverityTrace to SeverityError follow the labels of the formatted
	// view; SeverityError includes fatal and panic.
	SeverityTrace
	SeverityDebug
	SeverityInfo
	SeverityWarn
	SeverityError
)

// Severity returns the level r states in the first level column key it
// has (DefaultFormat: level, severity, lvl and the others).
func (f Format) Severity(r *Record) Severity {
	if r == nil || r.obj == nil {
		return SeverityUnknown
	}
	for _, col := range f.Columns {
		if col.Kind != ColumnLevel {
			continue
		}
		for _, key := range col.Keys {
			v, ok := r.obj.get(key)
			if !ok {
				continue
			}
			label, _ := levelLabel(valueText(v))
			return severityOf(strings.TrimSpace(label))
		}
	}
	return SeverityUnknown
}

func severityOf(label string) Severity {
	switch label {
	case "TRACE", "VERBOSE":
		return SeverityTrace
	case "DEBUG", "DBG":
		return SeverityDebug
	case "INFO", "INFORMATION", "NOTICE":
		return SeverityInfo
	case "WARN", "WARNING":
		return SeverityWarn
	case "ERROR", "ERR", "FATAL", "PANIC", "DPANIC", "CRITICAL", "CRIT", "ALERT", "EMERGENCY", "EMERG":
		return SeverityError
	}
	return SeverityUnknown
}

// Severity returns the level of record seq in the log's format.
func (l *Log) Severity(seq Seq) Severity {
	r, err := l.Record(seq)
	if err != nil {
		return SeverityUnknown
	}
	return l.format.Severity(r)
}

// Next returns the first shown record after from (before it when
// backward) for which match is true. from need not be kept: a dropped
// from starts a forward search at the oldest record. ok is false when no
// shown record matches.
func (l *Log) Next(from Seq, backward bool, match func(Seq) bool) (Seq, bool) {
	n := len(l.records)
	if n == 0 {
		return 0, false
	}
	i := int(from - l.first)
	step, start := 1, i+1
	if backward {
		step, start = -1, i-1
	}
	if from < l.first {
		if backward {
			return 0, false
		}
		start = 0
	}
	if backward && start >= n {
		start = n - 1
	}
	for j := start; j >= 0 && j < n; j += step {
		if !l.shown[j] {
			continue
		}
		if seq := l.first + Seq(j); match(seq) {
			return seq, true
		}
	}
	return 0, false
}
