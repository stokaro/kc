package viewer

import "testing"

func TestWidgetSearchMatches(t *testing.T) {
	w := New("")
	w.SetContent("foo\nbar\nFoo baz\nnoop", Metadata{})

	setSearch(t, w, "foo", SearchOptions{})

	if !w.HasSearchMatches() {
		t.Fatalf("expected search matches")
	}
	if len(w.searchMatches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(w.searchMatches))
	}
	if w.activeMatch != 0 {
		t.Fatalf("expected initial match index 0, got %d", w.activeMatch)
	}
	if !w.advanceMatch() || w.activeMatch != 1 {
		t.Fatalf("expected to advance to second match, got %d", w.activeMatch)
	}
	if !w.previousMatch() || w.activeMatch != 0 {
		t.Fatalf("expected to move back to first match")
	}
	w.clearSearchState()
	if w.HasSearchMatches() {
		t.Fatalf("expected matches cleared after reset")
	}
}

func TestWidgetFooterCursorNil(t *testing.T) {
	w := New("")
	w.SetContent("foo\nbar", Metadata{})
	_ = w.beginSearch()
	if cur := w.FooterCursor(20); cur != nil {
		t.Fatalf("the search dialog draws its own cursor; footer cursor must be nil")
	}
}

func TestWidgetAppendLinesExtendsMatches(t *testing.T) {
	w := New("")
	w.SetPlainMode(true)
	w.SetContent("alpha", Metadata{})
	setSearch(t, w, "beta", SearchOptions{})
	if w.HasSearchMatches() {
		t.Fatalf("did not expect initial matches")
	}
	w.AppendLines([]string{"beta"})
	if !w.HasSearchMatches() {
		t.Fatalf("expected matches after append")
	}
	if len(w.searchMatches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(w.searchMatches))
	}
	if !w.advanceMatch() {
		t.Fatalf("expected advanceMatch to succeed on appended data")
	}
}
