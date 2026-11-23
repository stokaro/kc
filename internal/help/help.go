// Package help holds the help pages that F1 shows: one short markdown page
// per feature, embedded in the binary. A page is topics/<id>.md; its first
// line is the "# Title" heading. Pages link to each other with relative
// links such as [Row filter](row-filter.md), which also work when the files
// are read on GitHub.
//
// The pages are the source of truth for how a feature behaves; the README
// links to them. See docs/adr for the layout and the context mapping.
package help

import (
	"embed"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
)

//go:embed topics/*.md
var files embed.FS

// ID names a help page: the base name of its file without ".md".
type ID string

// Topic is one help page.
type Topic struct {
	ID       ID
	Title    string // the text of the first "# " heading
	Markdown string // the whole page, heading included
}

// Link is a link from one page to another.
type Link struct {
	Text   string
	Target ID
}

var load = sync.OnceValue(func() map[ID]Topic {
	topics := make(map[ID]Topic)
	entries, err := fs.Glob(files, "topics/*.md")
	if err != nil {
		return topics
	}
	for _, name := range entries {
		data, err := files.ReadFile(name)
		if err != nil {
			continue
		}
		id := ID(strings.TrimSuffix(path.Base(name), ".md"))
		md := string(data)
		topics[id] = Topic{ID: id, Title: title(md), Markdown: md}
	}
	return topics
})

func title(md string) string {
	first, _, _ := strings.Cut(strings.TrimLeft(md, "\n"), "\n")
	if t, ok := strings.CutPrefix(first, "# "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// Lookup returns the page id names.
func Lookup(id ID) (Topic, bool) {
	t, ok := load()[id]
	return t, ok
}

// Topics returns every page, sorted by ID.
func Topics() []Topic {
	all := load()
	out := make([]Topic, 0, len(all))
	for _, t := range all {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Topic) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out
}

// linkPattern matches an inline markdown link. Images ("![...]") are not
// links; callers skip them by the preceding "!".
var linkPattern = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)

// TargetOf returns the page a link destination names: "row-filter.md" and
// "./row-filter.md" name row-filter. Other destinations (URLs, anchors,
// files elsewhere) name no page.
func TargetOf(dest string) (ID, bool) {
	dest = strings.TrimPrefix(dest, "./")
	name, ok := strings.CutSuffix(dest, ".md")
	if !ok || name == "" || strings.ContainsAny(name, "/:#?") {
		return "", false
	}
	return ID(name), true
}

// Links returns the links to other pages in md, in order. Links inside
// code are included; pages keep links out of code.
func Links(md string) []Link {
	var out []Link
	for _, m := range linkPattern.FindAllStringSubmatchIndex(md, -1) {
		if m[0] > 0 && md[m[0]-1] == '!' {
			continue
		}
		if id, ok := TargetOf(md[m[4]:m[5]]); ok {
			out = append(out, Link{Text: md[m[2]:m[3]], Target: id})
		}
	}
	return out
}

// ReplaceLinks rewrites every inline link of md with what repl returns for
// its text and destination. Images stay as they are.
func ReplaceLinks(md string, repl func(text, dest string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range linkPattern.FindAllStringSubmatchIndex(md, -1) {
		if m[0] > 0 && md[m[0]-1] == '!' {
			continue
		}
		b.WriteString(md[last:m[0]])
		b.WriteString(repl(md[m[2]:m[3]], md[m[4]:m[5]]))
		last = m[1]
	}
	b.WriteString(md[last:])
	return b.String()
}
