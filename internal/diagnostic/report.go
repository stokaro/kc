package diagnostic

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Report is a tool's output as kc shows it: evidence from the named tool,
// not kc's own finding.
type Report struct {
	// Tool names the source, e.g. "krr".
	Tool string
	// Summary lines describe what the tool looked at.
	Summary []string
	// Notes are the limits of the result.
	Notes   []string
	Columns []string
	Rows    [][]string
	// ToolErrors are errors the tool reported in its output.
	ToolErrors []string
}

// Scope is an object a report is limited to. Empty fields match any value.
type Scope struct {
	Namespace string
	Kind      string
	Name      string
}

func (s Scope) String() string {
	var b strings.Builder
	if s.Kind != "" {
		b.WriteString(s.Kind + " ")
	}
	switch {
	case s.Namespace != "" && s.Name != "":
		b.WriteString(s.Namespace + "/" + s.Name)
	case s.Name != "":
		b.WriteString(s.Name)
	case s.Namespace != "":
		b.WriteString("namespace " + s.Namespace)
	default:
		b.WriteString("everything the tool returned")
	}
	return strings.TrimSpace(b.String())
}

// matches reports whether an object is inside the scope.
func (s Scope) matches(namespace, kind, name string) bool {
	return (s.Namespace == "" || s.Namespace == namespace) &&
		(s.Kind == "" || strings.EqualFold(s.Kind, kind)) &&
		(s.Name == "" || s.Name == name)
}

// inScopes reports whether an object is inside any of scopes; no scopes
// means no limit.
func inScopes(scopes []Scope, namespace, kind, name string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, s := range scopes {
		if s.matches(namespace, kind, name) {
			return true
		}
	}
	return false
}

// Parser reads a tool's standard output. It returns an error for output
// it does not understand, never an empty report in its place.
type Parser func(stdout []byte, scopes []Scope) (Report, error)

// Lookup returns the parser of adapter.
func Lookup(adapter string) (Parser, bool) {
	if adapter == "krr" {
		return ParseKRR, true
	}
	return nil, false
}

// Text renders the report as plain text.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Source: %s (output of an external tool, not checked by kc)\n", Sanitize(r.Tool))
	for _, l := range r.Summary {
		b.WriteString(Sanitize(l) + "\n")
	}
	if len(r.ToolErrors) > 0 {
		b.WriteString("\nErrors reported by " + Sanitize(r.Tool) + ":\n")
		for _, e := range r.ToolErrors {
			b.WriteString("  " + Sanitize(e) + "\n")
		}
	}
	b.WriteString("\n")
	if len(r.Rows) == 0 {
		b.WriteString("No rows.\n")
	} else {
		writeTable(&b, r.Columns, r.Rows)
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n")
		for _, n := range r.Notes {
			b.WriteString("- " + Sanitize(n) + "\n")
		}
	}
	return b.String()
}

func writeTable(b *strings.Builder, cols []string, rows [][]string) {
	widths := make([]int, len(cols))
	clean := make([][]string, len(rows))
	for i, c := range cols {
		widths[i] = utf8.RuneCountInString(c)
	}
	for r, row := range rows {
		clean[r] = make([]string, len(cols))
		for i := range cols {
			if i < len(row) {
				clean[r][i] = Sanitize(row[i])
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(clean[r][i]))
		}
	}
	line := func(cells []string) {
		for i, c := range cells {
			if i == len(cells)-1 {
				b.WriteString(c)
				break
			}
			b.WriteString(c + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)+2))
		}
		b.WriteString("\n")
	}
	line(cols)
	for _, row := range clean {
		line(row)
	}
}

// Sanitize replaces control characters, so a tool's text cannot move the
// cursor, change colors or write terminal links (OSC 8) in kc's screen.
// Newlines and tabs stay.
func Sanitize(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return '?'
		}
		return r
	}, s)
}

func unsafeRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}
