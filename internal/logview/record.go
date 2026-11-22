package logview

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// MaxParseBytes is the longest line that is parsed as JSON. Longer lines are
// kept and shown as text, so one huge record cannot stall the viewer.
const MaxParseBytes = 256 << 10

// maxDepth limits the nesting of a parsed JSON value. Deeper lines are shown
// as text.
const maxDepth = 100

// Source identifies where a line came from. It is empty for a single stream;
// a viewer that merges several pods or containers sets it per line.
type Source struct {
	Pod       string
	Container string
}

// String returns "pod/container", "pod" or "".
func (s Source) String() string {
	switch {
	case s.Pod != "" && s.Container != "":
		return s.Pod + "/" + s.Container
	default:
		return s.Pod + s.Container
	}
}

// Record is one log line as it arrived, with its JSON object when the line
// holds one.
type Record struct {
	// Raw is the line exactly as read, without the line break.
	Raw string
	// Source is where the line came from.
	Source Source
	// Prefix is the kubelet timestamp in front of the JSON object when the
	// stream was opened with timestamps, for example
	// "2026-11-22T10:00:00.123456789Z". It is part of Raw.
	Prefix string

	obj *object
}

// IsJSON reports whether the line holds a JSON object.
func (r *Record) IsJSON() bool { return r.obj != nil }

// Field returns the value at path as display text. ok is false when the line
// is not JSON or the path does not exist.
func (r *Record) Field(path Path) (string, bool) {
	if r.obj == nil {
		return "", false
	}
	v, ok := lookup(r.obj, path)
	if !ok {
		return "", false
	}
	return valueText(v), true
}

// NewRecord parses line. A line is JSON when, after optional spaces and an
// optional RFC 3339 timestamp, it is exactly one JSON object. Anything else,
// including broken JSON, stays a text record.
func NewRecord(line string, src Source) Record {
	r := Record{Raw: line, Source: src}
	if len(line) > MaxParseBytes {
		return r
	}
	body := strings.TrimSpace(line)
	if prefix, rest, ok := splitTimestamp(body); ok {
		r.Prefix = prefix
		body = rest
	}
	if !strings.HasPrefix(body, "{") || !strings.HasSuffix(body, "}") {
		r.Prefix = ""
		return r
	}
	obj, err := decodeObject(body)
	if err != nil {
		r.Prefix = ""
		return r
	}
	r.obj = obj
	return r
}

// splitTimestamp splits "<RFC 3339 time> <rest>" as kubectl --timestamps
// writes it.
func splitTimestamp(line string) (string, string, bool) {
	i := strings.IndexByte(line, ' ')
	if i < len("2006-01-02T15:04:05Z") || i > len(time.RFC3339Nano)+6 {
		return "", "", false
	}
	if _, err := time.Parse(time.RFC3339Nano, line[:i]); err != nil {
		return "", "", false
	}
	return line[:i], strings.TrimLeft(line[i+1:], " "), true
}

// object is a JSON object that keeps its key order.
type object struct {
	keys []string
	vals []any
}

// get returns the value of key. With duplicate keys the last one wins, as in
// encoding/json.
func (o *object) get(key string) (any, bool) {
	for i := len(o.keys) - 1; i >= 0; i-- {
		if o.keys[i] == key {
			return o.vals[i], true
		}
	}
	return nil, false
}

var errNotObject = errors.New("not a JSON object")

// decodeObject decodes one JSON object and nothing after it. Values are
// string, json.Number, bool, nil, *object or []any.
func decodeObject(s string) (*object, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, errNotObject
	}
	obj, err := readObject(dec, 1)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errNotObject
	}
	return obj, nil
}

var errTooDeep = errors.New("JSON nested too deep")

func readValue(dec *json.Decoder, tok json.Token, depth int) (any, error) {
	switch tok {
	case json.Delim('{'):
		return readObject(dec, depth+1)
	case json.Delim('['):
		return readArray(dec, depth+1)
	}
	return tok, nil
}

func readObject(dec *json.Decoder, depth int) (*object, error) {
	if depth > maxDepth {
		return nil, errTooDeep
	}
	obj := &object{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errNotObject
		}
		tok, err = dec.Token()
		if err != nil {
			return nil, err
		}
		val, err := readValue(dec, tok, depth)
		if err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, key)
		obj.vals = append(obj.vals, val)
	}
	if _, err := dec.Token(); err != nil { // '}'
		return nil, err
	}
	return obj, nil
}

func readArray(dec *json.Decoder, depth int) ([]any, error) {
	if depth > maxDepth {
		return nil, errTooDeep
	}
	items := []any{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		val, err := readValue(dec, tok, depth)
		if err != nil {
			return nil, err
		}
		items = append(items, val)
	}
	if _, err := dec.Token(); err != nil { // ']'
		return nil, err
	}
	return items, nil
}
