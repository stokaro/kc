package viewer

import "testing"

// White-box, like the other tests here: the search state SetLines must keep
// is not exported.
func TestWidgetSearchIgnoresColorCodes(t *testing.T) {
	w := New("")
	w.SetPlainMode(true)
	w.SetLines([]string{"\x1b[31mERROR\x1b[0m boom", "x 31m"})
	setSearch(t, w, "error boom", SearchOptions{})
	if len(w.searchMatches) != 1 {
		t.Fatalf("got %d matches across a color code, want 1", len(w.searchMatches))
	}
	if m := w.searchMatches[0]; m.start != 0 || m.end != 10 {
		t.Fatalf("match columns %d-%d, want 0-10", m.start, m.end)
	}
	setSearch(t, w, "31m", SearchOptions{})
	if len(w.searchMatches) != 1 || w.searchMatches[0].line != 1 {
		t.Fatalf("an escape sequence must not match: %+v", w.searchMatches)
	}
}

func TestWidgetSetLinesKeepsSearchAndOffset(t *testing.T) {
	w := New("")
	w.SetPlainMode(true)
	w.Resize(20, 2)
	w.SetContent("", Metadata{Title: "logs"})
	w.SetLines([]string{"a error", "b", "c error", "d", "e"})
	setSearch(t, w, "error", SearchOptions{})
	w.offset = 3

	w.SetLines([]string{"error x", "error y", "z"})

	if got := w.Metadata().Title; got != "logs" {
		t.Fatalf("metadata lost: %q", got)
	}
	if w.searchQuery != "error" || len(w.searchMatches) != 2 {
		t.Fatalf("search query %q with %d matches, want error with 2", w.searchQuery, len(w.searchMatches))
	}
	if w.offset != 1 {
		t.Fatalf("offset = %d, want it clamped to 1", w.offset)
	}
	if got := w.PlainText(); got != "error x\nerror y\nz" {
		t.Fatalf("PlainText() = %q", got)
	}

	w.SetLines(nil)
	if w.HasSearchMatches() || w.offset != 0 || w.PlainText() != "" {
		t.Fatal("empty lines must clear matches and offset")
	}
	w.AppendLines([]string{"error again"})
	if got := w.PlainText(); got != "error again" {
		t.Fatalf("appending after SetLines(nil) = %q, want no leading blank line", got)
	}
	if len(w.searchMatches) != 1 {
		t.Fatalf("appended line must be searched, got %d matches", len(w.searchMatches))
	}
}
