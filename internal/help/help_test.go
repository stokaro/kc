package help_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/help"
)

// declaredIDs returns the values of the ID constants in ids.go.
func declaredIDs(t *testing.T) []help.ID {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "ids.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []help.ID
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("constant %s is not a string literal", vs.Names[0].Name)
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, help.ID(s))
			}
		}
	}
	return ids
}

func TestEveryIDHasAPageAndEveryPageAnID(t *testing.T) {
	t.Parallel()
	ids := declaredIDs(t)
	for _, id := range ids {
		if _, ok := help.Lookup(id); !ok {
			t.Errorf("no page topics/%s.md for constant %q", id, id)
		}
	}
	for _, topic := range help.Topics() {
		if !slices.Contains(ids, topic.ID) {
			t.Errorf("page %s.md has no constant in ids.go", topic.ID)
		}
	}
	files, err := filepath.Glob(filepath.Join("topics", "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if filepath.Ext(f) != ".md" {
			t.Errorf("%s: topics holds only .md pages", f)
		}
	}
}

func TestPagesAreWellFormed(t *testing.T) {
	t.Parallel()
	for _, topic := range help.Topics() {
		t.Run(string(topic.ID), func(t *testing.T) {
			t.Parallel()
			if topic.Title == "" {
				t.Fatal(`the first line is not a "# Title" heading`)
			}
			if strings.Count(topic.Markdown, "\n# ") > 0 {
				t.Error("a page has one top-level heading")
			}
			if len(strings.TrimSpace(strings.TrimPrefix(topic.Markdown, "# "+topic.Title))) < 40 {
				t.Error("the page has no text")
			}
			for _, link := range help.Links(topic.Markdown) {
				if _, ok := help.Lookup(link.Target); !ok {
					t.Errorf("link %q goes to missing page %q", link.Text, link.Target)
				}
			}
			fenced := false
			for n, line := range strings.Split(topic.Markdown, "\n") {
				if strings.HasPrefix(line, "```") {
					fenced = !fenced
					continue
				}
				if fenced && len([]rune(line)) > 70 {
					t.Errorf("line %d: a code line wider than 70 columns does not fit the window", n+1)
				}
				if !fenced && strings.HasPrefix(strings.TrimSpace(line), "|") {
					t.Errorf("line %d: tables do not fit the help window; use a list", n+1)
				}
			}
		})
	}
}

func TestIndexLinksEveryPage(t *testing.T) {
	t.Parallel()
	index, ok := help.Lookup(help.Index)
	if !ok {
		t.Fatal("no index page")
	}
	linked := make(map[help.ID]bool)
	for _, l := range help.Links(index.Markdown) {
		linked[l.Target] = true
	}
	for _, topic := range help.Topics() {
		if topic.ID != help.Index && !linked[topic.ID] {
			t.Errorf("the index does not link %s", topic.ID)
		}
	}
}

func TestTargetOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dest string
		want help.ID
		ok   bool
	}{
		{"row-filter.md", "row-filter", true},
		{"./row-filter.md", "row-filter", true},
		{"https://example.com/a.md", "", false},
		{"../README.md", "", false},
		{"row-filter.md#keys", "", false},
		{"row-filter", "", false},
		{".md", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.dest, func(t *testing.T) {
			t.Parallel()
			got, ok := help.TargetOf(tc.dest)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("TargetOf(%q) = %q, %v; want %q, %v", tc.dest, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestLinksAndReplaceLinks(t *testing.T) {
	t.Parallel()
	md := "See [the filter](row-filter.md), ![img](x.png) and [web](https://x.io)."
	links := help.Links(md)
	if len(links) != 1 || links[0] != (help.Link{Text: "the filter", Target: "row-filter"}) {
		t.Fatalf("Links = %+v", links)
	}
	got := help.ReplaceLinks(md, func(text, dest string) string { return "<" + text + "|" + dest + ">" })
	want := "See <the filter|row-filter.md>, ![img](x.png) and <web|https://x.io>."
	if got != want {
		t.Fatalf("ReplaceLinks = %q, want %q", got, want)
	}
}
