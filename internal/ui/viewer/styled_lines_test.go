package viewer

import (
	"strings"
	"testing"
)

func TestAppendStyledLines(t *testing.T) {
	const red = "\x1b[31m"
	const reset = "\x1b[m"
	w := New("dracula")
	w.SetPlainMode(true)
	w.SetContent("", Metadata{MimeType: "text/plain"})
	w.Resize(80, 10)

	w.AppendStyledLines(
		[]string{"[a/app] hello", "[b/app] world"},
		[]string{red + "[a/app]" + reset + " hello", red + "[b/app]" + reset + " world"},
	)
	w.AppendLines([]string{"plain line"})

	// Search runs on the text, so match columns ignore the styling.
	setSearch(t, w, "hello", SearchOptions{})
	if len(w.searchMatches) != 1 || w.searchMatches[0].start != len("[a/app] ") {
		t.Fatalf("matches = %+v, want one at column %d", w.searchMatches, len("[a/app] "))
	}
	if strings.Contains(w.PlainText(), "\x1b") {
		t.Fatalf("PlainText %q carries styling", w.PlainText())
	}
	shown := strings.Join(w.lines, "\n")
	if !strings.Contains(shown, red+"[a/app]") || !strings.Contains(shown, "plain line") {
		t.Fatalf("shown lines %q lack the styled prefix or the plain line", shown)
	}

	// A theme change rebuilds the shown lines from the styled form.
	w.SetTheme("monokai")
	if got := strings.Join(w.lines, "\n"); got != shown {
		t.Fatalf("after a theme change the lines are %q, want %q", got, shown)
	}
	if len(w.lines) != len(w.rawLines) {
		t.Fatalf("%d shown lines for %d text lines", len(w.lines), len(w.rawLines))
	}

	// New content drops the styled form.
	w.SetContent("x", Metadata{MimeType: "text/plain"})
	if w.styled != nil {
		t.Fatal("SetContent kept the styled lines")
	}
}
