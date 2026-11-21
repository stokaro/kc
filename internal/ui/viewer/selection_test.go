package viewer

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func mouse(kind string, x, y int) tea.MouseMsg {
	m := tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}
	switch kind {
	case "press":
		return tea.MouseClickMsg(m)
	case "move":
		return tea.MouseMotionMsg(m)
	default:
		return tea.MouseReleaseMsg(m)
	}
}

func TestMouseSelectionCopiesText(t *testing.T) {
	w := New("")
	w.SetPlainMode(true)
	w.SetContent("alpha beta\ngamma delta\nepsilon", Metadata{})
	w.Resize(40, 10)

	w.Update(mouse("press", 6, 0))
	w.Update(mouse("move", 4, 1))
	if !w.HasSelection() || !w.Selecting() {
		t.Fatalf("drag should select")
	}
	cmd, _ := w.Update(mouse("release", 4, 1))
	if cmd == nil {
		t.Fatalf("release should request a copy")
	}
	req, ok := cmd().(CopyRequestMsg)
	if !ok || req.Text != "beta\ngamma" || req.What != "selection" {
		t.Fatalf("unexpected copy request %#v", req)
	}
	if !w.HasSelection() {
		t.Fatalf("selection stays highlighted after copy")
	}

	// Backwards drag selects the same way.
	w.Update(mouse("press", 4, 1))
	w.Update(mouse("move", 6, 0))
	cmd, _ = w.Update(mouse("release", 6, 0))
	if req := cmd().(CopyRequestMsg); req.Text != "beta\ngamma" {
		t.Fatalf("backwards selection = %q", req.Text)
	}

	// A plain click clears the selection and copies nothing.
	w.Update(mouse("press", 1, 2))
	cmd, _ = w.Update(mouse("release", 1, 2))
	if cmd != nil || w.HasSelection() {
		t.Fatalf("a click should clear the selection")
	}
}

func TestSelectionHonorsHorizontalOffset(t *testing.T) {
	w := New("")
	w.SetPlainMode(true)
	w.SetContent("0123456789abcdef", Metadata{})
	w.Resize(8, 3)
	w.hOffset = 10
	w.Update(mouse("press", 0, 0))
	w.Update(mouse("move", 2, 0))
	cmd, _ := w.Update(mouse("release", 2, 0))
	if got := cmd().(CopyRequestMsg).Text; got != "abc" {
		t.Fatalf("selection with offset = %q", got)
	}
}

func TestWithSelectionSplitsMatches(t *testing.T) {
	got := withSelection([]displayMatch{{start: 0, end: 4}, {start: 6, end: 10}}, 2, 8)
	want := []displayMatch{{start: 0, end: 2}, {start: 2, end: 8, selected: true}, {start: 8, end: 10}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

func TestPlainTextIncludesAppendedLines(t *testing.T) {
	w := New("")
	w.SetPlainMode(true)
	w.SetContent("one", Metadata{})
	w.AppendLines([]string{"\x1b[31mtwo\x1b[0m"})
	if got := w.PlainText(); got != "one\ntwo" {
		t.Fatalf("PlainText = %q", got)
	}
}
