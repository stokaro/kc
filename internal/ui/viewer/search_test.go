package viewer

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func setSearch(t *testing.T, w *Widget, query string, opts SearchOptions) {
	t.Helper()
	re, err := compileSearch(query, opts)
	if err != nil {
		t.Fatalf("compile %q: %v", query, err)
	}
	w.searchQuery = query
	w.searchOpts = opts
	w.searchRe = re
	w.updateSearchMatches()
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "f7":
		return tea.KeyPressMsg{Code: tea.KeyF7}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func typeText(w *Widget, s string) {
	for _, r := range s {
		w.Update(key(string(r)))
	}
}

func TestCompileSearchModes(t *testing.T) {
	line := "Name: foo.bar  FOO  foobar"
	tests := []struct {
		name  string
		query string
		opts  SearchOptions
		want  []string
	}{
		{"normal ignores case", "foo", SearchOptions{}, []string{"foo", "FOO", "foo"}},
		{"normal is literal", "o.b", SearchOptions{}, []string{"o.b"}},
		{"case sensitive", "FOO", SearchOptions{CaseSensitive: true}, []string{"FOO"}},
		{"whole words", "foo", SearchOptions{WholeWords: true}, []string{"foo", "FOO"}},
		{"regex", `fo+\.\w+`, SearchOptions{Mode: SearchRegex}, []string{"foo.bar"}},
		{"wildcard", "f?o*r", SearchOptions{Mode: SearchWildcard}, []string{"foo.bar", "FOO  foobar"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re, err := compileSearch(tt.query, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			got := re.FindAllString(line, -1)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("matches = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := compileSearch("(", SearchOptions{Mode: SearchRegex}); err == nil {
		t.Fatalf("expected an error for an invalid regular expression")
	}
}

func TestSearchDialogFlow(t *testing.T) {
	w := New("")
	w.SetPlainMode(true)
	w.SetContent("alpha\nbeta\nAlpha beta\ngamma", Metadata{})
	w.Resize(60, 20)

	w.Update(key("f7"))
	if !w.SearchMode() {
		t.Fatalf("F7 should open the search dialog")
	}
	view := ansi.Strip(w.View(Frame{Width: 60, Height: 20}))
	for _, want := range []string{"Search", "Enter search string:", "(*) Normal", "[ ] Case sensitive", "[< OK >]", "[ Cancel ]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog view misses %q:\n%s", want, view)
		}
	}

	typeText(w, "alpha")
	// Tab to "Normal", right to "Case sensitive", toggle it.
	w.Update(key("tab"))
	w.Update(key("right"))
	w.Update(key("space"))
	w.Update(key("enter"))
	if w.SearchMode() {
		t.Fatalf("Enter should accept the dialog")
	}
	if !w.searchOpts.CaseSensitive || w.searchQuery != "alpha" {
		t.Fatalf("unexpected search state: %q %+v", w.searchQuery, w.searchOpts)
	}
	if len(w.searchMatches) != 1 {
		t.Fatalf("case-sensitive search should find one match, got %d", len(w.searchMatches))
	}

	// Reopening keeps the previous query and options; Esc cancels.
	w.Update(key("f7"))
	if w.searchDialog.query() != "alpha" || !w.searchDialog.opts.CaseSensitive {
		t.Fatalf("dialog should start from the previous search")
	}
	w.Update(key("esc"))
	if w.SearchMode() {
		t.Fatalf("Esc should cancel the dialog")
	}
}

func TestSearchDialogInvalidRegexStaysOpen(t *testing.T) {
	w := New("")
	w.SetContent("abc", Metadata{})
	_ = w.beginSearch()
	w.searchDialog.opts.Mode = SearchRegex
	typeText(w, "(")
	w.Update(key("enter"))
	if !w.SearchMode() || w.searchDialog.err == "" {
		t.Fatalf("invalid regex should keep the dialog open with an error")
	}
}

func TestSearchBackwardsDirection(t *testing.T) {
	w := New("")
	w.SetContent("x\nx\nx\nx", Metadata{})
	w.Resize(20, 2)
	setSearch(t, w, "x", SearchOptions{Backwards: true})
	if w.activeMatch != 1 {
		t.Fatalf("backwards search should start at the bottom of the view, got %d", w.activeMatch)
	}
	w.nextMatch()
	if w.activeMatch != 0 {
		t.Fatalf("next in backwards mode should move up, got %d", w.activeMatch)
	}
	w.prevMatch()
	if w.activeMatch != 1 {
		t.Fatalf("previous in backwards mode should move down, got %d", w.activeMatch)
	}
}
