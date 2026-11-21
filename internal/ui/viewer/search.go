package viewer

import (
	"fmt"
	"regexp"
	"strings"
)

// SearchMode selects how the search string is interpreted.
type SearchMode int

const (
	// SearchNormal matches the string literally.
	SearchNormal SearchMode = iota
	// SearchRegex treats the string as a Go regular expression.
	SearchRegex
	// SearchWildcard supports shell-like wildcards: * and ?.
	SearchWildcard
)

// SearchOptions mirrors the Midnight Commander search dialog.
type SearchOptions struct {
	Mode          SearchMode
	CaseSensitive bool
	Backwards     bool
	WholeWords    bool
}

// compileSearch turns a search string and options into a regular expression.
func compileSearch(query string, opts SearchOptions) (*regexp.Regexp, error) {
	if query == "" {
		return nil, nil
	}
	var pattern string
	switch opts.Mode {
	case SearchRegex:
		pattern = query
	case SearchWildcard:
		pattern = wildcardPattern(query)
	default:
		pattern = regexp.QuoteMeta(query)
	}
	if opts.WholeWords {
		pattern = `\b(?:` + pattern + `)\b`
	}
	if !opts.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid search pattern: %w", err)
	}
	return re, nil
}

func wildcardPattern(query string) string {
	var b strings.Builder
	for _, r := range query {
		switch r {
		case '*':
			b.WriteString(".*?")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}
