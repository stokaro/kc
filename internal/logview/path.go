package logview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Path selects a value inside a JSON record: .msg, .http.status,
// .items[0].name, .["key with spaces"]. The leading dot is optional; "." alone
// selects the whole record.
//
// A dotted name also matches a literal key with dots, so http.status finds
// both {"http":{"status":200}} and {"http.status":200}.
type Path struct {
	steps []step
	text  string
}

type step struct {
	key   string
	index int
	isKey bool
}

// String returns the path as written.
func (p Path) String() string { return p.text }

// ParsePath parses a path.
func ParsePath(s string) (Path, error) {
	text := s
	if s == "." {
		return Path{text: text}, nil
	}
	s = strings.TrimPrefix(s, ".")
	if s == "" {
		return Path{}, fmt.Errorf("empty path")
	}
	var steps []step
	for len(s) > 0 {
		switch s[0] {
		case '[':
			end := closingBracket(s)
			if end < 0 {
				return Path{}, fmt.Errorf("path %q: missing ]", text)
			}
			inner := s[1:end]
			s = s[end+1:]
			if strings.HasPrefix(inner, `"`) {
				key, err := strconv.Unquote(inner)
				if err != nil {
					return Path{}, fmt.Errorf("path %q: bad quoted key %s", text, inner)
				}
				steps = append(steps, step{key: key, isKey: true})
			} else {
				n, err := strconv.Atoi(inner)
				if err != nil || n < 0 {
					return Path{}, fmt.Errorf("path %q: index %q is not a number", text, inner)
				}
				steps = append(steps, step{index: n})
			}
		case '.':
			return Path{}, fmt.Errorf("path %q: empty name", text)
		default:
			end := strings.IndexAny(s, ".[")
			if end < 0 {
				end = len(s)
			}
			name := s[:end]
			if strings.ContainsAny(name, " \t]\"") {
				return Path{}, fmt.Errorf("path %q: bad name %q", text, name)
			}
			steps = append(steps, step{key: name, isKey: true})
			s = s[end:]
		}
		if strings.HasPrefix(s, ".") {
			s = s[1:]
			if s == "" || s[0] == '.' || s[0] == '[' {
				return Path{}, fmt.Errorf("path %q: empty name", text)
			}
		} else if s != "" && s[0] != '[' {
			return Path{}, fmt.Errorf("path %q: unexpected %q", text, s[:1])
		}
	}
	return Path{steps: steps, text: text}, nil
}

// closingBracket returns the index of the ] that ends the bracket at s[0],
// skipping a quoted key.
func closingBracket(s string) int {
	inQuote := false
	for i := 1; i < len(s); i++ {
		switch {
		case inQuote && s[i] == '\\':
			i++
		case s[i] == '"':
			inQuote = !inQuote
		case !inQuote && s[i] == ']':
			return i
		}
	}
	return -1
}

// lookup resolves steps against v. Consecutive names are first tried as one
// literal dotted key (longest first), then one by one.
func lookup(v any, p Path) (any, bool) {
	return resolve(v, p.steps)
}

func resolve(v any, steps []step) (any, bool) {
	if len(steps) == 0 {
		return v, true
	}
	switch node := v.(type) {
	case *object:
		if !steps[0].isKey {
			return nil, false
		}
		names := 0
		for names < len(steps) && steps[names].isKey {
			names++
		}
		for n := names; n >= 1; n-- {
			var key strings.Builder
			key.WriteString(steps[0].key)
			for _, s := range steps[1:n] {
				key.WriteString("." + s.key)
			}
			if child, ok := node.get(key.String()); ok {
				if out, ok := resolve(child, steps[n:]); ok {
					return out, true
				}
			}
		}
	case []any:
		if steps[0].isKey || steps[0].index >= len(node) {
			return nil, false
		}
		return resolve(node[steps[0].index], steps[1:])
	}
	return nil, false
}

// valueText is the plain text of a value: strings as they are, numbers and
// literals as written, objects and arrays as compact JSON.
func valueText(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case json.Number:
		return val.String()
	case bool:
		return strconv.FormatBool(val)
	case nil:
		return "null"
	}
	var buf bytes.Buffer
	writeJSON(&buf, v)
	return buf.String()
}

// writeJSON writes v as compact JSON in its original key order.
func writeJSON(buf *bytes.Buffer, v any) {
	switch val := v.(type) {
	case *object:
		buf.WriteByte('{')
		for i, k := range val.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			writeJSON(buf, val.vals[i])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSON(buf, item)
		}
		buf.WriteByte(']')
	case string:
		writeString(buf, val)
	default:
		buf.WriteString(valueText(val))
	}
}

func writeString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)           // a string always encodes
	buf.Truncate(buf.Len() - 1) // Encode adds a newline
}
