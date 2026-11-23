package viewer

import "strings"

// maxSearchMatches bounds the matches a search records, so a short pattern
// on a large log cannot take more memory than the log itself. The search
// summary then shows the count with a "+".
const maxSearchMatches = 100_000

// DropFirstLines removes the n oldest lines, for a log view that keeps only
// its recent lines. The view keeps showing the same lines when they are
// still there; lost reports that the top line of the view was among the
// dropped ones, so the view now starts at the oldest kept line. Search
// matches and the selection on the kept lines stay.
func (w *Widget) DropFirstLines(n int) (lost bool) {
	n = min(n, len(w.rawLines))
	if n <= 0 {
		return false
	}
	lost = w.offset < n
	// Measuring every kept line again is slow for a large log; it is only
	// needed when the widest line goes.
	widest := false
	for _, line := range w.rawLines[:n] {
		if runeWidth(line) >= w.maxLineWidth {
			widest = true
			break
		}
	}
	// Clear the dropped slots so their strings can be collected; the
	// slices lose their front and reallocate as they grow.
	clear(w.rawLines[:n])
	w.rawLines = w.rawLines[n:]
	if len(w.lines) >= n {
		clear(w.lines[:n])
		w.lines = w.lines[n:]
	}
	if w.styled != nil && len(w.styled) >= n {
		clear(w.styled[:n])
		w.styled = w.styled[n:]
	}
	if !w.plain {
		w.raw = strings.Join(w.rawLines, "\n")
		w.highlight()
	}
	w.offset = max(0, w.offset-n)
	if widest {
		w.updateMaxLineWidth()
	}
	w.dropSelectionLines(n)
	w.dropSearchLines(n)
	return lost
}

func (w *Widget) dropSelectionLines(n int) {
	if !w.sel.active && !w.sel.dragging {
		return
	}
	w.sel.anchor.line -= n
	w.sel.cursor.line -= n
	if w.sel.anchor.line < 0 || w.sel.cursor.line < 0 {
		w.sel = selection{}
	}
}

func (w *Widget) dropSearchLines(n int) {
	if len(w.searchMatches) == 0 {
		return
	}
	gone := 0
	for gone < len(w.searchMatches) && w.searchMatches[gone].line < n {
		gone++
	}
	kept := w.searchMatches[:0]
	for _, m := range w.searchMatches[gone:] {
		m.line -= n
		kept = append(kept, m)
	}
	w.searchMatches = kept
	w.searchMatchIndex = make(map[int][]int, len(kept))
	for i, m := range kept {
		w.searchMatchIndex[m.line] = append(w.searchMatchIndex[m.line], i)
	}
	switch {
	case len(kept) == 0:
		w.activeMatch = -1
	case w.activeMatch >= gone:
		w.activeMatch -= gone
	default:
		// The active match was dropped; n goes to the oldest kept one.
		w.activeMatch = -1
	}
}
