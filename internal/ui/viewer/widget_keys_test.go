package viewer

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWidgetF3AndF10Close(t *testing.T) {
	for _, key := range []string{"f3", "f10"} {
		w := New("")
		w.SetContent("foo\nbar", Metadata{})
		closed := false
		w.SetCallbacks(nil, nil, func() tea.Cmd { closed = true; return nil })
		if _, handled := w.handleKey(key); !handled || !closed {
			t.Fatalf("%s: expected viewer to close (handled=%t closed=%t)", key, handled, closed)
		}
	}
}

func TestWidgetShiftF7ContinuesSearch(t *testing.T) {
	w := New("")
	w.SetContent("foo\nbar\nfoo", Metadata{})
	setSearch(t, w, "foo", SearchOptions{})
	if _, handled := w.handleKey("shift+f7"); !handled || w.activeMatch != 1 {
		t.Fatalf("expected shift+f7 to move to the next match, got %d", w.activeMatch)
	}
}
