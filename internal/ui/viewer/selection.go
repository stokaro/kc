package viewer

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// CopyRequestMsg asks the application to put Text on the clipboard. What
// names the content for the confirmation message ("selection", "output").
type CopyRequestMsg struct {
	Text string
	What string
}

// textPos is a position in the content: a raw line and a display column.
type textPos struct {
	line int
	col  int
}

func (p textPos) before(o textPos) bool {
	return p.line < o.line || (p.line == o.line && p.col < o.col)
}

// selection is a mouse selection: drag with the left button to select,
// release to copy; a plain click clears it.
type selection struct {
	active   bool // a selection is shown
	dragging bool // the left button is held
	anchor   textPos
	cursor   textPos
}

func (s selection) bounds() (start, end textPos) {
	if s.cursor.before(s.anchor) {
		return s.cursor, s.anchor
	}
	return s.anchor, s.cursor
}

// PlainText returns the whole content without ANSI styling.
func (w *Widget) PlainText() string {
	return ansi.Strip(strings.Join(w.rawLines, "\n"))
}

// Selecting reports whether a mouse drag is in progress.
func (w *Widget) Selecting() bool { return w.sel.dragging }

// HasSelection reports whether text is selected.
func (w *Widget) HasSelection() bool { return w.sel.active }

// ClearSelection removes the selection highlight.
func (w *Widget) ClearSelection() { w.sel = selection{} }

// posAt maps a point inside the viewer to a content position.
func (w *Widget) posAt(x, y int) textPos {
	if len(w.rawLines) == 0 {
		return textPos{}
	}
	y = max(0, min(y, w.page()-1))
	x = max(0, x)
	if !w.wrapMode {
		line := min(w.offset+y, len(w.rawLines)-1)
		return textPos{line: line, col: w.hOffset + x}
	}
	// Wrapped: walk the visible lines, each taking ceil(width/viewport) rows.
	width := max(1, w.viewportWidth())
	row := 0
	for line := w.offset; line < len(w.rawLines); line++ {
		rows := max(1, (ansi.StringWidth(w.rawLines[line])+width-1)/width)
		if y < row+rows {
			return textPos{line: line, col: (y-row)*width + x}
		}
		row += rows
	}
	return textPos{line: len(w.rawLines) - 1, col: ansi.StringWidth(w.rawLines[len(w.rawLines)-1])}
}

// handleMouseSelection updates the selection for a left-button mouse event.
func (w *Widget) handleMouseSelection(msg tea.MouseMsg) (tea.Cmd, bool) {
	mouse := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft || mouse.Y < 0 || mouse.Y >= w.page() {
			return nil, false
		}
		p := w.posAt(mouse.X, mouse.Y)
		w.sel = selection{dragging: true, anchor: p, cursor: p}
		return nil, true
	case tea.MouseMotionMsg:
		if !w.sel.dragging {
			return nil, false
		}
		// Dragging past the edges scrolls, so a selection can span pages.
		switch {
		case mouse.Y < 0:
			w.scrollBy(-1)
		case mouse.Y >= w.page():
			w.scrollBy(1)
		}
		w.sel.cursor = w.posAt(mouse.X, mouse.Y)
		w.sel.active = w.sel.anchor != w.sel.cursor
		return nil, true
	case tea.MouseReleaseMsg:
		if !w.sel.dragging {
			return nil, false
		}
		w.sel.dragging = false
		w.sel.cursor = w.posAt(mouse.X, mouse.Y)
		if w.sel.anchor == w.sel.cursor {
			w.sel = selection{}
			return nil, true
		}
		w.sel.active = true
		text := w.selectedText()
		if text == "" {
			return nil, true
		}
		return func() tea.Msg { return CopyRequestMsg{Text: text, What: "selection"} }, true
	}
	return nil, false
}

// selectedText returns the selected plain text; the end column is inclusive
// so that dragging onto the last character selects it.
func (w *Widget) selectedText() string {
	if !w.sel.active {
		return ""
	}
	start, end := w.sel.bounds()
	var b strings.Builder
	for line := start.line; line <= end.line && line < len(w.rawLines); line++ {
		text := ansi.Strip(w.rawLines[line])
		from, to := 0, ansi.StringWidth(text)
		if line == start.line {
			from = start.col
		}
		if line == end.line {
			to = min(to, end.col+1)
		}
		if from < to {
			b.WriteString(ansi.Cut(text, from, to))
		}
		if line != end.line {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// selectionRange returns the selected columns [start, end) on a line.
func (w *Widget) selectionRange(line int) (from, to int, ok bool) {
	if !w.sel.active {
		return 0, 0, false
	}
	start, end := w.sel.bounds()
	if line < start.line || line > end.line {
		return 0, 0, false
	}
	from, to = 0, 1<<30
	if line == start.line {
		from = start.col
	}
	if line == end.line {
		to = end.col + 1
	}
	return from, to, from < to
}

// withSelection overlays the selection on a line's search matches. Matches
// are clipped around the selection so ranges stay sorted and disjoint.
func withSelection(matches []displayMatch, from, to int) []displayMatch {
	out := make([]displayMatch, 0, len(matches)+1)
	inserted := false
	for _, m := range matches {
		if m.end <= from {
			out = append(out, m)
			continue
		}
		if m.start < from {
			left := m
			left.end = from
			out = append(out, left)
		}
		if !inserted {
			out = append(out, displayMatch{start: from, end: to, selected: true})
			inserted = true
		}
		if m.end > to {
			right := m
			right.start = max(m.start, to)
			out = append(out, right)
		}
	}
	if !inserted {
		out = append(out, displayMatch{start: from, end: to, selected: true})
	}
	return out
}
