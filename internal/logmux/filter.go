package logmux

import (
	"fmt"
	"path"
	"strings"
)

// Filter picks the targets of a merged log by pod and container name. It
// is a list of terms; a term is `pod[/container]`, each part a glob
// (path.Match syntax), an empty or missing part matching every name. A
// term with a leading `-` excludes what it matches. A target is selected
// when no include term exists or one matches, and no exclude term
// matches. Matching uses names only, so a filter keeps its meaning for
// pods that are created later and for restarted containers.
//
// Examples: `-/istio-proxy` drops a sidecar from every pod; `web-0 web-1`
// follows two pods; `api-*/app` follows one container of matching pods.
type Filter struct {
	terms []filterTerm
}

type filterTerm struct {
	exclude   bool
	pod       string
	container string
}

// ParseFilter parses the space-separated terms of s. An empty s selects
// every target.
func ParseFilter(s string) (Filter, error) {
	var f Filter
	for word := range strings.FieldsSeq(s) {
		t := filterTerm{}
		if rest, ok := strings.CutPrefix(word, "-"); ok {
			t.exclude, word = true, rest
		}
		pod, container, _ := strings.Cut(word, "/")
		if strings.Contains(container, "/") {
			return Filter{}, fmt.Errorf("%q: use pod/container", word)
		}
		if pod == "" && container == "" {
			return Filter{}, fmt.Errorf("%q: name a pod or a container", word)
		}
		t.pod, t.container = orAll(pod), orAll(container)
		for _, p := range []string{t.pod, t.container} {
			if _, err := path.Match(p, ""); err != nil {
				return Filter{}, fmt.Errorf("%q: %w", word, err)
			}
		}
		f.terms = append(f.terms, t)
	}
	return f, nil
}

func orAll(p string) string {
	if p == "" {
		return "*"
	}
	return p
}

// Match reports whether f selects t.
func (f Filter) Match(t Target) bool {
	hasInclude, included := false, false
	for _, term := range f.terms {
		matched := term.match(t)
		if term.exclude {
			if matched {
				return false
			}
			continue
		}
		hasInclude = true
		included = included || matched
	}
	return included || !hasInclude
}

func (t filterTerm) match(target Target) bool {
	// The patterns were checked by ParseFilter, so Match cannot fail.
	pod, _ := path.Match(t.pod, target.Pod)
	container, _ := path.Match(t.container, target.Container)
	return pod && container
}

// IsZero reports a filter that selects every target.
func (f Filter) IsZero() bool { return len(f.terms) == 0 }

// String returns the filter in the form ParseFilter reads.
func (f Filter) String() string {
	words := make([]string, 0, len(f.terms))
	for _, t := range f.terms {
		var b strings.Builder
		if t.exclude {
			b.WriteByte('-')
		}
		switch {
		case t.container == "*":
			b.WriteString(t.pod)
		case t.pod == "*":
			b.WriteString("/" + t.container)
		default:
			b.WriteString(t.pod + "/" + t.container)
		}
		words = append(words, b.String())
	}
	return strings.Join(words, " ")
}
