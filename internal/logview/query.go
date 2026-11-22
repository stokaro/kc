package logview

import (
	"fmt"
	"regexp"
	"strings"
)

// Query filters records and selects fields to show. It is parsed from one
// line of space-separated terms:
//
//	timeout             the line contains "timeout" (any case)
//	"conn refused"      the line contains the quoted text
//	/5\d\d/             the line matches the regular expression (RE2); it
//	                    ends at the slash before a space or the end, and
//	                    quotes inside it are literal
//	level=error,warn    the field equals one of the values (any case)
//	msg~^GET            the field matches the regular expression
//	!term               drop lines that match term
//	.msg .http.status   show these fields instead of the whole record
//
// A record is shown when it matches every include term and no exclude term.
// Field terms never match a line that is not JSON, so they hide text lines
// when used to include and keep them when used to exclude. Extraction paths
// apply to JSON records in the formatted view; a record that has none of the
// fields is shown whole.
//
// The zero Query and a nil *Query match everything.
type Query struct {
	text    string
	include []matcher
	exclude []matcher
	extract []Path
}

type matcher interface {
	match(r *Record) bool
}

// ParseQuery parses s. An empty or blank s gives a query that matches all.
func ParseQuery(s string) (*Query, error) {
	terms, err := splitTerms(s)
	if err != nil {
		return nil, err
	}
	q := &Query{text: strings.Join(terms, " ")}
	for _, term := range terms {
		neg := false
		body := term
		if strings.HasPrefix(body, "!") {
			neg = true
			body = body[1:]
			if body == "" {
				return nil, fmt.Errorf("%q: nothing after the ! negation", term)
			}
		}
		if path, ok, err := extractTerm(body); err != nil {
			return nil, err
		} else if ok {
			if neg {
				return nil, fmt.Errorf("%q: a field to show cannot be excluded", term)
			}
			q.extract = append(q.extract, path)
			continue
		}
		m, err := parseMatcher(body)
		if err != nil {
			return nil, err
		}
		if neg {
			q.exclude = append(q.exclude, m)
		} else {
			q.include = append(q.include, m)
		}
	}
	return q, nil
}

// MustParseQuery is ParseQuery for constant queries; it panics on an error.
func MustParseQuery(s string) *Query {
	q, err := ParseQuery(s)
	if err != nil {
		panic(err)
	}
	return q
}

// String returns the query in normalized spacing.
func (q *Query) String() string {
	if q == nil {
		return ""
	}
	return q.text
}

// IsZero reports whether the query neither filters nor extracts.
func (q *Query) IsZero() bool {
	return q == nil || (len(q.include) == 0 && len(q.exclude) == 0 && len(q.extract) == 0)
}

// Filters reports whether the query can hide records.
func (q *Query) Filters() bool {
	return q != nil && (len(q.include) > 0 || len(q.exclude) > 0)
}

// Extract returns the paths to show, in order.
func (q *Query) Extract() []Path {
	if q == nil {
		return nil
	}
	return q.extract
}

// Match reports whether r passes the filters.
func (q *Query) Match(r *Record) bool {
	if q == nil {
		return true
	}
	for _, m := range q.exclude {
		if m.match(r) {
			return false
		}
	}
	for _, m := range q.include {
		if !m.match(r) {
			return false
		}
	}
	return true
}

// extractTerm reports whether body is a bare path such as .msg.
func extractTerm(body string) (Path, bool, error) {
	if !strings.HasPrefix(body, ".") || strings.ContainsAny(body, "=~") {
		return Path{}, false, nil
	}
	p, err := ParsePath(body)
	if err != nil {
		return Path{}, false, err
	}
	return p, true, nil
}

func parseMatcher(body string) (matcher, error) {
	switch {
	case strings.HasPrefix(body, `"`):
		text := unquote(body)
		if text == "" {
			return nil, fmt.Errorf("%s: empty text", body)
		}
		return textMatcher(strings.ToLower(text)), nil
	case len(body) >= 2 && strings.HasPrefix(body, "/") && strings.HasSuffix(body, "/"):
		re, err := regexp.Compile(body[1 : len(body)-1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", body, err)
		}
		return regexMatcher{re: re}, nil
	}
	if i := strings.IndexAny(body, "=~"); i > 0 && !strings.Contains(body[:i], `"`) {
		path, err := ParsePath(body[:i])
		if err != nil {
			return nil, err
		}
		if len(path.steps) == 0 {
			return nil, fmt.Errorf("%s: compare a field, not the whole record", body)
		}
		value := unquote(body[i+1:])
		if body[i] == '~' {
			re, err := regexp.Compile(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", body, err)
			}
			return fieldRegexMatcher{path: path, re: re}, nil
		}
		return fieldEqualMatcher{path: path, values: strings.Split(value, ",")}, nil
	}
	return textMatcher(strings.ToLower(unquote(body))), nil
}

type textMatcher string

func (m textMatcher) match(r *Record) bool {
	return containsFold(r.Raw, string(m))
}

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) match(r *Record) bool { return m.re.MatchString(r.Raw) }

type fieldEqualMatcher struct {
	path   Path
	values []string
}

func (m fieldEqualMatcher) match(r *Record) bool {
	v, ok := r.Field(m.path)
	if !ok {
		return false
	}
	for _, want := range m.values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

type fieldRegexMatcher struct {
	path Path
	re   *regexp.Regexp
}

func (m fieldRegexMatcher) match(r *Record) bool {
	v, ok := r.Field(m.path)
	return ok && m.re.MatchString(v)
}

// containsFold reports whether s contains the lower-case needle, ignoring
// case. ASCII lines, the common case, are checked without allocating.
func containsFold(s, needle string) bool {
	if needle == "" {
		return true
	}
	if !isASCII(s) || !isASCII(needle) {
		return strings.Contains(strings.ToLower(s), needle)
	}
	n := len(needle)
	for i := 0; i+n <= len(s); i++ {
		if lowerASCII(s[i]) != needle[0] {
			continue
		}
		j := 1
		for j < n && lowerASCII(s[i+j]) == needle[j] {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func lowerASCII(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// splitTerms splits s at spaces outside double quotes. Quotes stay in the
// terms; unquote removes them.
func splitTerms(s string) ([]string, error) {
	var terms []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '/' && !inQuote && (cur.Len() == 0 || cur.String() == "!") {
			// A /regex/ term ends at the slash before a space or the end;
			// quotes inside it are literal.
			if end := regexTermEnd(s, i); end > i {
				cur.WriteString(s[i : end+1])
				i = end
				continue
			}
		}
		switch {
		case inQuote && c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case !inQuote && (c == ' ' || c == '\t'):
			if cur.Len() > 0 {
				terms = append(terms, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("missing closing quote")
	}
	if cur.Len() > 0 {
		terms = append(terms, cur.String())
	}
	return terms, nil
}

// regexTermEnd returns the index of the slash that closes the regex term
// opened at s[start], or -1. The closing slash is followed by a space or the
// end of s; a backslash escapes the next byte.
func regexTermEnd(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			i++
		case s[i] == '/' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t'):
			return i
		}
	}
	return -1
}

// unquote removes double quotes and the backslash before \" and \\ inside
// them.
func unquote(s string) string {
	if !strings.Contains(s, `"`) {
		return s
	}
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\'):
			i++
			b.WriteByte(s[i])
		case c == '"':
			inQuote = !inQuote
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
