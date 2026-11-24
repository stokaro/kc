package dashboard

import (
	"time"

	"github.com/stokaro/kc/internal/investigation"
)

// The time a view shows is one of:
//
//   - the investigation window (C-x w), while the view follows it: a live
//     window ends now, a fixed one at its end;
//   - a span of the zoom steps, or the span a detached window left, ending
//     now (live) or at the paused end.
//
// Zooming, panning and pausing a live window detach the view from the
// window: the range stays where it is, and the title says the window is
// another. w makes the shown range the investigation window again, so
// Logs and Info panels read the same period.

// window is the investigation window kc has now.
func (m *Model) window() investigation.Window {
	if m.src.Window == nil {
		return investigation.Window{}
	}
	return m.src.Window()
}

func sameWindow(a, b investigation.Window) bool {
	return a.Last == b.Last && a.Start.Equal(b.Start) && a.End.Equal(b.End)
}

// followWindow points every view at the investigation window when it
// changed since the last call, and cancels their reads of the old range.
// It reports whether it changed anything.
func (m *Model) followWindow() bool {
	w := m.window()
	if sameWindow(w, m.lastWin) {
		return false
	}
	m.lastWin = w
	for _, v := range m.stack {
		if w.Off() {
			m.detach(v)
			continue
		}
		v.cancelRead()
		m.follow(v, w)
	}
	return true
}

// follow points v at window w, which is set.
func (m *Model) follow(v *view, w investigation.Window) {
	v.follow, v.win, v.custom = true, w, 0
	v.paused, v.end, v.cursor = false, time.Time{}, -1
	if !w.Live(m.now()) {
		_, end, _ := w.Bounds(m.now())
		v.paused, v.end = true, end
	}
}

// windowBounds is the range of the window v follows.
func (m *Model) windowBounds(v *view) (start, end time.Time) {
	now := m.now()
	start, end, _ = v.win.Bounds(now)
	if end.IsZero() {
		end = now
	}
	return start, end
}

// detach keeps the range v shows and stops following the window.
func (m *Model) detach(v *view) {
	if !v.follow {
		return
	}
	span, end := v.span(m), m.shownEnd(v)
	v.follow, v.custom = false, span
	if v.paused || !v.win.Live(m.now()) {
		v.paused, v.end = true, end
	}
}

// zoomFrom is the zoom index one step in or out from a span that is not
// a zoom step.
func zoomFrom(spans []time.Duration, span time.Duration, step int) int {
	if step < 0 {
		i := 0
		for j, s := range spans {
			if s < span {
				i = j
			}
		}
		return i
	}
	for j, s := range spans {
		if s > span {
			return j
		}
	}
	return len(spans) - 1
}

// windowOf is the investigation window of the range v shows: live while
// v is live, else fixed.
func (m *Model) windowOf(v *view) investigation.Window {
	if v.follow {
		return v.win
	}
	span := min(v.span(m), investigation.MaxRelative)
	if !v.paused {
		return investigation.Window{Last: span}
	}
	return investigation.Window{Start: v.end.Add(-span), End: v.end}
}

// useAsWindow makes the range v shows the investigation window (w).
func (m *Model) useAsWindow(v *view) {
	if m.src.SetWindow == nil {
		return
	}
	w := m.windowOf(v)
	m.src.SetWindow(w)
	m.lastWin = w
	for _, x := range m.stack {
		x.follow, x.win, x.custom = true, w, 0
	}
}

// rejoinWindow points every view at the investigation window again, when
// one is set, and cancels their reads of the ranges they showed.
func (m *Model) rejoinWindow() {
	w := m.window()
	if w.Off() {
		return
	}
	m.lastWin = w
	for _, v := range m.stack {
		v.cancelRead()
		m.follow(v, w)
	}
}

// windowState is the title's state of a view that follows the window, or
// "" when it does not.
func (m *Model) windowState(v *view) string {
	if !v.follow {
		return ""
	}
	if v.win.Last > 0 {
		return "LIVE window " + shortDuration(v.win.Last)
	}
	return "WINDOW " + v.win.Label(m.loc())
}
