package ui

// White-box: the F1 mapping is unexported tables keyed by dialog names and
// folder type names that only the source shows; these tests read the
// source so a new dialog or folder without a help page fails here.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/help"
	"github.com/stokaro/kc/internal/models"
)

// parseDir parses the non-test Go files of dir.
func parseDir(t *testing.T, dir string) []*ast.File {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

// stringConsts returns the string constants declared in files.
func stringConsts(files []*ast.File) map[string]string {
	consts := make(map[string]string)
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, v := range vs.Values {
					if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							consts[vs.Names[i].Name] = s
						}
					}
				}
			}
		}
	}
	return consts
}

// registeredModalNames returns the name of every dialog the package
// registers with the modal manager.
func registeredModalNames(t *testing.T) map[string]bool {
	t.Helper()
	files := parseDir(t, ".")
	consts := stringConsts(files)
	names := make(map[string]bool)
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Register" {
				return true
			}
			if inner, ok := sel.X.(*ast.SelectorExpr); !ok || inner.Sel.Name != "modalManager" {
				return true
			}
			switch arg := call.Args[0].(type) {
			case *ast.BasicLit:
				s, err := strconv.Unquote(arg.Value)
				if err != nil {
					t.Fatal(err)
				}
				names[s] = true
			case *ast.Ident:
				s, ok := consts[arg.Name]
				if !ok {
					t.Fatalf("Register(%s, ...): not a string constant; name the dialog with one", arg.Name)
				}
				names[s] = true
			case *ast.CallExpr:
				if fn, ok := arg.Fun.(*ast.Ident); ok && fn.Name == "panelModeModalKey" {
					names[panelModeModalKey(0)] = true
					names[panelModeModalKey(1)] = true
					return true
				}
				t.Fatalf("Register with a computed name at %v; add it to registeredModalNames", call.Pos())
			default:
				t.Fatalf("Register with an unexpected name expression at %v", call.Pos())
			}
			return true
		})
	}
	if len(names) < 30 {
		t.Fatalf("found only %d dialogs; the source scan is broken", len(names))
	}
	return names
}

func TestEveryDialogHasAHelpPage(t *testing.T) {
	t.Parallel()
	names := registeredModalNames(t)
	for name := range names {
		if name == helpModalName {
			continue
		}
		page, ok := modalHelpPages[name]
		if !ok {
			t.Errorf("dialog %q has no help page: add it to modalHelpPages", name)
			continue
		}
		if _, ok := help.Lookup(page); !ok {
			t.Errorf("dialog %q: page %q does not exist", name, page)
		}
	}
	for name := range modalHelpPages {
		if !names[name] {
			t.Errorf("modalHelpPages names %q, which no code registers", name)
		}
	}
	if got := modalHelpPage(helpModalName); got != "" {
		t.Errorf("the help window has help page %q; F1 there is its own key", got)
	}
}

func TestEveryPanelModeHasAHelpPage(t *testing.T) {
	t.Parallel()
	for _, mode := range PanelModeOrder() {
		page, ok := modeHelpPages[mode]
		if !ok {
			t.Errorf("panel mode %s has no help page", modeLabel(mode))
			continue
		}
		if _, ok := help.Lookup(page); !ok {
			t.Errorf("panel mode %s: page %q does not exist", modeLabel(mode), page)
		}
	}
}

func TestEveryFolderTypeHasAHelpPage(t *testing.T) {
	t.Parallel()
	types := make(map[string]bool)
	for _, f := range parseDir(t, filepath.Join("..", "models")) {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts := spec.(*ast.TypeSpec)
				if _, isStruct := ts.Type.(*ast.StructType); isStruct && strings.HasSuffix(ts.Name.Name, "Folder") {
					types[ts.Name.Name] = true
				}
			}
		}
	}
	if len(types) < 20 {
		t.Fatalf("found only %d folder types; the source scan is broken", len(types))
	}
	for name := range types {
		page, ok := folderHelpPages[name]
		if !ok {
			t.Errorf("folder type %s has no help page: add it to folderHelpPages", name)
			continue
		}
		if _, ok := help.Lookup(page); !ok {
			t.Errorf("folder type %s: page %q does not exist", name, page)
		}
	}
	for name := range folderHelpPages {
		if !types[name] {
			t.Errorf("folderHelpPages names %s, which is not a folder type of internal/models", name)
		}
	}
}

func TestRowHelpPagesExist(t *testing.T) {
	t.Parallel()
	for seg, page := range rowHelpPages {
		if _, ok := help.Lookup(page); !ok {
			t.Errorf("row %q: page %q does not exist", seg, page)
		}
	}
}

func TestRowHelpPage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		item models.Item
		want help.ID
		ok   bool
	}{
		{"diagnosis entry", models.NewSimpleItem("ns/web/diagnosis", []string{"/diagnosis"}, []string{"pods", "web", "diagnosis"}, nil), help.Diagnosis, true},
		{"timeline entry", models.NewSimpleItem("ns/web/timeline", []string{"/timeline"}, []string{"pods", "web", "timeline"}, nil), help.Timeline, true},
		{"attention entry", models.NewSimpleItem("overview/attention", []string{"/attention"}, []string{"overview", "attention"}, nil), help.Attention, true},
		{"quiet checks entry", models.NewSimpleItem("quiet/nodes", []string{"/3 quiet checks"}, []string{"overview", "nodes quiet checks"}, nil), help.QuietChecks, true},
		{"overview entry", models.NewSimpleItem(models.OverviewRowID, []string{"/overview"}, []string{"x"}, nil), help.Overview, true},
		{"plain row", models.NewSimpleItem("web", []string{"web"}, []string{"pods", "web"}, nil), "", false},
		{"nil", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := rowHelpPage(tc.item)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("rowHelpPage = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestEveryPageRenders(t *testing.T) {
	t.Parallel()
	for _, topic := range help.Topics() {
		t.Run(string(topic.ID), func(t *testing.T) {
			t.Parallel()
			for _, width := range []int{helpTextWidth, 40} {
				lines, links := renderHelpPage(topic.Markdown, width)
				if len(lines) < 3 {
					t.Fatalf("width %d: %d lines", width, len(lines))
				}
				for n, line := range lines {
					if strings.ContainsAny(line, helpLinkOpen+helpLinkClose) {
						t.Fatalf("width %d, line %d keeps a link marker", width, n)
					}
				}
				want := help.Links(topic.Markdown)
				if len(links) != len(want) {
					t.Fatalf("width %d: %d links rendered, the page has %d", width, len(links), len(want))
				}
				for i, l := range links {
					if l.target != want[i].Target || len(l.spans) == 0 {
						t.Fatalf("width %d: link %d = %+v, want %q", width, i, l, want[i].Target)
					}
				}
			}
		})
	}
}
