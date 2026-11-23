package info

import (
	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// The Info content scrolls like a viewer, and its top pods are links
// (ADR 0176). Down selects the next top pod (without a selection, the
// first one on screen) when it is on screen and scrolls one line
// otherwise. Up selects the previous one the same way, clears the
// selection above the first pod, and only scrolls without a selection, so
// it can always leave the pods. PgUp, PgDn, Home, End and the
// wheel scroll and keep the selection only while it stays on screen.
// Enter, or a click on the selected pod, opens it in the other panel. None
// of this touches the other panel until a pod is opened.

// clampOffset limits a scroll offset to the lines that fill a page.
func clampOffset(offset, total, page int) int {
	return max(0, min(offset, total-page))
}

// consumerKey identifies a top pod. Every top pod is a pod, so its
// namespace and name are its full identity.
func consumerKey(c models.InfoConsumer) string { return c.Namespace + "/" + c.Name }

// focusIndex is the index of the selected top pod, or -1.
func (w *Widget) focusIndex() int {
	if w.focus == "" {
		return -1
	}
	for i, c := range w.summary.Top {
		if consumerKey(c) == w.focus {
			return i
		}
	}
	return -1
}

// spans returns the number of content lines and the first and last line of
// each top pod.
func (w *Widget) spans() (total int, spans [][2]int) {
	all := w.content(w.width)
	spans = make([][2]int, len(w.summary.Top))
	for i := range spans {
		spans[i] = [2]int{-1, -1}
	}
	for n, l := range all {
		if l.consumer < 0 {
			continue
		}
		if spans[l.consumer][0] < 0 {
			spans[l.consumer][0] = n
		}
		spans[l.consumer][1] = n
	}
	return len(all), spans
}

func (w *Widget) page() int { return max(1, w.height) }

func (w *Widget) visible(span [2]int) bool {
	return span[0] >= w.offset && span[1] < w.offset+w.page()
}

// firstVisible is the first top pod on screen, or -1: Down without a
// selection starts there.
func (w *Widget) firstVisible(spans [][2]int) int {
	for i, s := range spans {
		if w.visible(s) {
			return i
		}
	}
	return -1
}

func (w *Widget) setFocus(i int) {
	if i < 0 || i >= len(w.summary.Top) {
		w.focus = ""
		return
	}
	w.focus = consumerKey(w.summary.Top[i])
}

// scrollTo moves the offset and drops a selection that leaves the screen.
func (w *Widget) scrollTo(offset, total int, spans [][2]int) {
	w.offset = clampOffset(offset, total, w.page())
	if i := w.focusIndex(); i >= 0 && !w.visible(spans[i]) {
		w.focus = ""
	}
}

func (w *Widget) handleKey(key string) (tea.Cmd, bool) {
	total, spans := w.spans()
	cur := w.focusIndex()
	switch key {
	case "down":
		next := cur + 1
		if cur < 0 {
			next = w.firstVisible(spans)
		}
		if next >= 0 && next < len(spans) && w.visible(spans[next]) {
			w.setFocus(next)
			return nil, true
		}
		w.scrollTo(w.offset+1, total, spans)
	case "up":
		// Without a selection Up only scrolls, so it can leave the pods.
		prev := cur - 1
		switch {
		case prev >= 0 && w.visible(spans[prev]):
			w.setFocus(prev)
		case cur == 0:
			w.focus = ""
		default:
			w.scrollTo(w.offset-1, total, spans)
		}
	case "pgdown":
		w.scrollTo(w.offset+w.page()-1, total, spans)
	case "pgup":
		w.scrollTo(w.offset-w.page()+1, total, spans)
	case "home":
		w.scrollTo(0, total, spans)
	case "end":
		w.scrollTo(total, total, spans)
	case "enter":
		if cur < 0 {
			return nil, false
		}
		return w.open(cur), true
	default:
		return nil, false
	}
	return nil, true
}

func (w *Widget) handleMouse(m panelcontent.MouseMsg) (tea.Cmd, bool) {
	total, spans := w.spans()
	switch m.Intent {
	case panelcontent.MouseIntentWheel:
		w.scrollTo(w.offset+m.DeltaY, total, spans)
		return nil, true
	case panelcontent.MouseIntentClick:
		if m.Button != tea.MouseLeft {
			return nil, false
		}
		line := w.offset + m.Row
		for i, s := range spans {
			if line < s[0] || line > s[1] {
				continue
			}
			if i == w.focusIndex() {
				return w.open(i), true
			}
			w.setFocus(i)
			return nil, true
		}
		return nil, true
	}
	return nil, false
}

// open asks the app to show top pod i in the other panel.
func (w *Widget) open(i int) tea.Cmd {
	link := models.NewObjectRefLink(w.summary.Top[i].Ref())
	return func() tea.Msg { return models.OpenObjectRequest{Link: link} }
}
