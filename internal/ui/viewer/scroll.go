package viewer

// Offset returns the zero-based index of the first visible line.
func (w *Widget) Offset() int { return w.offset }

// ScrollToLine puts line at the top of the viewport, or as close as the end
// of the text allows, and resets horizontal scrolling.
func (w *Widget) ScrollToLine(line int) {
	maxScroll := max(0, len(w.lines)-w.page())
	w.offset = min(max(0, line), maxScroll)
	w.hOffset = 0
}
