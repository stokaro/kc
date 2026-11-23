package appconfig_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/stokaro/kc/pkg/appconfig"
)

// uiSources is the tree whose key handling the built-in hotkey list must
// cover.
const uiSources = "../../internal/ui"

// keysWithoutLiteral are keys internal/ui matches by key code instead of a
// "alt+..." string, so the source scan cannot see them.
var keysWithoutLiteral = map[string]string{
	"M-A": "isCopyOriginalsKey compares the key code",
}

// altKeyLiteral matches the key strings Bubble Tea reports for Meta keys.
var altKeyLiteral = regexp.MustCompile(`^alt\+(shift\+)?(.)$`)

// chordFuncs complete the C-x prefix with kc's own chords; their case
// labels are the second keys.
var chordFuncs = []string{"handleCtrlXKey", "handlePinChord"}

// handledHotkeys derives the Meta keys and C-x chords that internal/ui
// handles from its sources, in ParseHotkey notation.
func handledHotkeys(t *testing.T) map[string]string {
	t.Helper()
	keys := make(map[string]string)
	fset := token.NewFileSet()
	var files []*ast.File
	err := filepath.WalkDir(uiSources, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	consts := make(map[string]string)
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							consts[name.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if m := altKeyLiteral.FindStringSubmatch(s); m != nil {
				r := []rune(m[2])[0]
				if m[1] != "" {
					r = unicode.ToUpper(r)
				}
				keys["M-"+string(r)] = fset.Position(lit.Pos()).String()
			}
			return true
		})
	}
	chords := 0
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !slices.Contains(chordFuncs, fn.Name.Name) {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				cc, ok := n.(*ast.CaseClause)
				if !ok {
					return true
				}
				for _, e := range cc.List {
					var s string
					switch e := e.(type) {
					case *ast.Ident:
						s = consts[e.Name]
					case *ast.BasicLit:
						s, _ = strconv.Unquote(e.Value)
					}
					if s == "" {
						t.Fatalf("%s: a case label that is not a string constant", fset.Position(e.Pos()))
					}
					if letter, ok := strings.CutPrefix(s, "ctrl+"); ok {
						s = "C-" + letter
					}
					keys["C-x "+s] = fset.Position(e.Pos()).String()
					chords++
				}
				return true
			})
		}
	}
	if chords == 0 || len(keys) < 20 {
		t.Fatalf("found %d keys and %d chords; the source scan is broken", len(keys), chords)
	}
	return keys
}

// A custom command's hotkey runs before kc's own keys, so the built-in list
// must name every key kc handles, or a command would shadow it.
func TestBuiltinHotkeysMatchKeyHandling(t *testing.T) {
	t.Parallel()
	handled := handledHotkeys(t)
	for key, where := range handled {
		norm, err := appconfig.ParseHotkey(key)
		if err != nil {
			t.Errorf("%s (%s): %v", key, where, err)
			continue
		}
		if _, ok := appconfig.BuiltinHotkey(norm); !ok {
			t.Errorf("%s is handled at %s but a custom command may take it: add it to builtinHotkeys", norm, where)
		}
	}
	for key := range appconfig.BuiltinHotkeys() {
		if _, ok := handled[key]; ok {
			continue
		}
		if _, ok := keysWithoutLiteral[key]; ok {
			continue
		}
		t.Errorf("%s is reserved but internal/ui does not handle it: remove it from builtinHotkeys", key)
	}
}

// The help page of custom commands lists the reserved keys; it must name
// each one.
func TestBuiltinHotkeysOnHelpPage(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("../../internal/help/topics/custom-commands.md")
	if err != nil {
		t.Fatal(err)
	}
	for key := range appconfig.BuiltinHotkeys() {
		if !strings.Contains(string(page), "`"+key+"`") {
			t.Errorf("custom-commands.md does not list %s among the keys kc uses", key)
		}
	}
}
