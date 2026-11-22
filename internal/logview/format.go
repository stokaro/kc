package logview

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ColumnKind says how a leading column is shown.
type ColumnKind int

const (
	// ColumnTime shows the value dimmed.
	ColumnTime ColumnKind = iota
	// ColumnLevel shows the value upper-case and colored by severity.
	ColumnLevel
	// ColumnMessage shows the value as plain text.
	ColumnMessage
	// ColumnField shows the value as plain text, like a message.
	ColumnField
)

// Column is a leading field of the formatted view. The first of Keys that the
// record has is shown; that key is then left out of the key=value rest.
type Column struct {
	Kind ColumnKind
	Keys []string
}

// Format controls the formatted view.
type Format struct {
	// Columns come first, in this order. Keys are top-level keys.
	Columns []Column
	// Color adds ANSI colors: level by severity, time and keys dimmed.
	Color bool
	// SourceStyle, when set, renders the `[pod/container]` prefix of a
	// record with a source, for example in the source's own color. It
	// returns the prefix without the space after it.
	SourceStyle func(Source) string
}

// DefaultFormat shows time, level and message first, with the key names
// common in Go (slog, zap, logr), Java, Node.js (pino, bunyan) and Python
// structured loggers.
func DefaultFormat() Format {
	return Format{
		Columns: []Column{
			{Kind: ColumnTime, Keys: []string{"time", "ts", "timestamp", "@timestamp", "t"}},
			{Kind: ColumnLevel, Keys: []string{"level", "lvl", "severity", "loglevel", "@l"}},
			{Kind: ColumnMessage, Keys: []string{"msg", "message", "@m"}},
		},
		Color: true,
	}
}

// Colors change only the foreground and end with "default foreground", so
// the viewer's background and bold stay for the rest of the line (the
// widget's chroma highlighting does the same).
const (
	sgrReset = "\x1b[39m"
	sgrDim   = "\x1b[90m"
)

// Formatted renders r for the formatted view. Text records are shown as they
// are (with invalid UTF-8 replaced). For JSON records, paths from extract are
// shown instead of the whole record when the record has at least one of them.
func (f Format) Formatted(r *Record, extract []Path) string {
	var b strings.Builder
	f.writeHead(&b, r)
	if r.obj == nil {
		b.WriteString(displayText(r.Raw))
		return b.String()
	}
	if len(extract) > 0 && f.writeExtract(&b, r, extract) {
		return b.String()
	}
	f.writeRecord(&b, r.obj)
	return strings.TrimRight(b.String(), " ")
}

// Raw renders r for the raw view: the original line with invalid UTF-8
// replaced, after the source when there is one.
func (f Format) Raw(r *Record) string {
	if r.Source == (Source{}) {
		return displayText(r.Raw)
	}
	return f.source(r.Source) + " " + displayText(r.Raw)
}

// source renders the prefix of a record with a source.
func (f Format) source(src Source) string {
	if f.SourceStyle != nil {
		return f.SourceStyle(src)
	}
	return "[" + escapeControl(src.String()) + "]"
}

func (f Format) writeHead(b *strings.Builder, r *Record) {
	if r.Source != (Source{}) {
		b.WriteString(f.source(r.Source) + " ")
	}
	if r.Prefix != "" && r.obj != nil {
		f.dim(b, r.Prefix)
		b.WriteByte(' ')
	}
}

func (f Format) writeExtract(b *strings.Builder, r *Record, extract []Path) bool {
	found := false
	for _, p := range extract {
		v, ok := lookup(r.obj, p)
		if !ok {
			continue
		}
		if found {
			b.WriteByte(' ')
		}
		found = true
		b.WriteString(escapeControl(valueText(v)))
	}
	return found
}

func (f Format) writeRecord(b *strings.Builder, obj *object) {
	used := make(map[string]bool, len(f.Columns))
	for _, col := range f.Columns {
		for _, key := range col.Keys {
			v, ok := obj.get(key)
			if !ok {
				continue
			}
			used[key] = true
			text := escapeControl(valueText(v))
			switch col.Kind {
			case ColumnTime:
				f.dim(b, text)
			case ColumnLevel:
				f.level(b, text)
			default:
				b.WriteString(text)
			}
			b.WriteByte(' ')
			break
		}
	}
	for i, key := range obj.keys {
		if used[key] {
			continue
		}
		f.writePair(b, key, obj.vals[i])
	}
}

// writePair writes key=value; nested objects are flattened to dotted keys.
func (f Format) writePair(b *strings.Builder, key string, v any) {
	if nested, ok := v.(*object); ok && len(nested.keys) > 0 {
		for i, k := range nested.keys {
			f.writePair(b, key+"."+k, nested.vals[i])
		}
		return
	}
	f.dim(b, escapeControl(key)+"=")
	b.WriteString(quoteValue(v))
	b.WriteByte(' ')
}

func (f Format) dim(b *strings.Builder, s string) {
	if !f.Color {
		b.WriteString(s)
		return
	}
	b.WriteString(sgrDim + s + sgrReset)
}

func (f Format) level(b *strings.Builder, s string) {
	label, color := levelLabel(s)
	if !f.Color || color == "" {
		b.WriteString(label)
		return
	}
	b.WriteString(color + label + sgrReset)
}

// levelLabel normalizes a level to upper case, padded to five columns, and
// picks its color. Numeric levels follow pino and bunyan (10 trace .. 60
// fatal).
func levelLabel(s string) (string, string) {
	name := strings.ToLower(strings.TrimSpace(s))
	if n, err := strconv.Atoi(name); err == nil {
		switch {
		case n >= 60:
			name = "fatal"
		case n >= 50:
			name = "error"
		case n >= 40:
			name = "warn"
		case n >= 30:
			name = "info"
		case n >= 20:
			name = "debug"
		default:
			name = "trace"
		}
	}
	color := ""
	switch name {
	case "trace", "verbose":
		color = "\x1b[90m"
	case "debug", "dbg":
		color = "\x1b[36m"
	case "info", "information", "notice":
		color = "\x1b[32m"
	case "warn", "warning":
		color = "\x1b[33m"
	case "error", "err":
		color = "\x1b[31m"
	case "fatal", "panic", "dpanic", "critical", "crit", "alert", "emergency", "emerg":
		color = "\x1b[91m"
	}
	label := strings.ToUpper(name)
	if n := utf8.RuneCountInString(label); n < 5 {
		label += strings.Repeat(" ", 5-n)
	}
	return label, color
}

// quoteValue writes a value for key=value: plain when it has no spaces,
// quotes, '=' or control characters, Go-quoted otherwise. Objects and arrays
// are compact JSON.
func quoteValue(v any) string {
	switch val := v.(type) {
	case string:
		if needsQuote(val) {
			return strconv.Quote(val)
		}
		return val
	case json.Number, bool, nil:
		return valueText(val)
	}
	var buf bytes.Buffer
	writeJSON(&buf, v)
	return escapeControl(buf.String())
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r == ' ' || r == '"' || r == '=' || r == utf8.RuneError || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}

// escapeControl replaces control characters (line breaks, tabs, escape
// sequences) with Go escapes, so a decoded value can neither break the line
// nor send commands to the terminal.
func escapeControl(s string) string {
	s = strings.ToValidUTF8(s, "�")
	clean := true
	for _, r := range s {
		if isControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isControl(r) {
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0)
}

// displayText prepares an original text line for the screen. It keeps the
// line as the container wrote it (including its own colors) and only replaces
// invalid UTF-8.
func displayText(s string) string {
	return strings.ToValidUTF8(s, "�")
}
