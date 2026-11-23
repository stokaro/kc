package logview

import (
	"errors"
	"slices"
)

// Field is one value of a JSON record and the path that selects it.
type Field struct {
	Path  Path
	Value string
}

// Fields returns the values of a JSON record, nested objects flattened,
// in the record's key order. Arrays are one value, as compact JSON. A text
// record has none.
func (r *Record) Fields() []Field {
	if r.obj == nil {
		return nil
	}
	var out []Field
	var walk func(o *object, keys []string)
	walk = func(o *object, keys []string) {
		for i, k := range o.keys {
			path := slices.Concat(keys, []string{k})
			if child, ok := o.vals[i].(*object); ok && len(child.keys) > 0 {
				walk(child, path)
				continue
			}
			out = append(out, Field{Path: KeyPath(path...), Value: valueText(o.vals[i])})
		}
	}
	walk(r.obj, nil)
	return out
}

// ErrDropped reports a record that is no longer kept.
var ErrDropped = errors.New("the line was dropped to stay within the limits")

// Seq numbers the records of a Log from the start of the stream; a record
// keeps its number while older ones are dropped.
type Seq int64

// SeqAt returns the record shown on display line i, the i-th record that
// passes the query.
func (l *Log) SeqAt(i int) (Seq, bool) {
	if i < 0 {
		return 0, false
	}
	n := 0
	for j, shown := range l.shown {
		if !shown {
			continue
		}
		if n == i {
			return l.first + Seq(j), true
		}
		n++
	}
	return 0, false
}

// Record returns the kept record seq.
func (l *Log) Record(seq Seq) (*Record, error) {
	i := int(seq - l.first)
	if seq < l.first || i >= len(l.records) {
		return nil, ErrDropped
	}
	return &l.records[i], nil
}

// DisplayLine returns the display line of record seq, or false when the
// record is dropped or does not pass the query.
func (l *Log) DisplayLine(seq Seq) (int, bool) {
	i := int(seq - l.first)
	if seq < l.first || i >= len(l.records) || !l.shown[i] {
		return 0, false
	}
	n := 0
	for _, shown := range l.shown[:i] {
		if shown {
			n++
		}
	}
	return n, true
}

// ContextKind tells the lines of a context apart.
type ContextKind int

const (
	// ContextAround is a line shown only as context.
	ContextAround ContextKind = iota
	// ContextMatch is a line that also passes the query.
	ContextMatch
	// ContextAnchor is the record the context is around.
	ContextAnchor
)

// ContextLine is one line of a context.
type ContextLine struct {
	Seq  Seq
	Kind ContextKind
	Text string
}

// Context returns the record seq with up to before records ahead of it
// and after records behind it, whatever the query: of the same source when
// sameSource is set, else of the merged stream. Lines are rendered as the
// display renders them. complete is false when the records ahead ran out
// because older ones were dropped.
func (l *Log) Context(seq Seq, before, after int, sameSource bool) (lines []ContextLine, complete bool, err error) {
	if _, err := l.Record(seq); err != nil {
		return nil, false, err
	}
	at := int(seq - l.first)
	src := l.records[at].Source
	pick := func(i int) bool { return !sameSource || l.records[i].Source == src }
	start := at
	for n := 0; n < before && start > 0; {
		start--
		if pick(start) {
			n++
		}
	}
	// Ran out of kept records before finding enough: complete only when
	// nothing was ever dropped.
	complete = true
	if start == 0 {
		n := 0
		for i := range at {
			if pick(i) {
				n++
			}
		}
		complete = n >= before || l.Dropped() == 0
	}
	end := at
	for n := 0; n < after && end < len(l.records)-1; {
		end++
		if pick(end) {
			n++
		}
	}
	for i := start; i <= end; i++ {
		if !pick(i) {
			continue
		}
		kind := ContextAround
		switch {
		case i == at:
			kind = ContextAnchor
		case l.shown[i]:
			kind = ContextMatch
		}
		lines = append(lines, ContextLine{Seq: l.first + Seq(i), Kind: kind, Text: l.render(&l.records[i])})
	}
	return lines, complete, nil
}
