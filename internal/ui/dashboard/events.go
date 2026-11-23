package dashboard

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/internal/investigation"
	"github.com/stokaro/kc/internal/models"
)

// logsAround is the investigation window l sets around an event.
const logsAround = 5 * time.Minute

var (
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true)
	rolloutStyle = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
)

// Lane marks, strongest first.
const (
	markWarning = '▲'
	markRollout = '◆'
	markNormal  = '•'
	markUnknown = '·' // before the oldest Event the API server kept
)

// rolloutKinds are the controllers whose Normal Events are rollouts.
var rolloutKinds = map[string]bool{"Deployment": true, "ReplicaSet": true, "StatefulSet": true, "DaemonSet": true}

// IsRollout reports whether e is a step of a rollout: a Normal Event of a
// controller, such as ScalingReplicaSet.
func IsRollout(e models.UsageEvent) bool {
	return e.Type != corev1.EventTypeWarning && rolloutKinds[e.Kind]
}

func eventMark(e models.UsageEvent) rune {
	switch {
	case e.Type == corev1.EventTypeWarning:
		return markWarning
	case IsRollout(e):
		return markRollout
	}
	return markNormal
}

func markRank(r rune) int {
	switch r {
	case markWarning:
		return 3
	case markRollout:
		return 2
	case markNormal:
		return 1
	}
	return 0
}

// overlaps reports whether e happened in [from, to]: its first or last
// time, or the span between them for a repeated Event.
func overlaps(e models.UsageEvent, from, to time.Time) bool {
	first := e.First
	if !e.FirstKnown || first.After(e.Last) {
		first = e.Last
	}
	return !e.Last.Before(from) && !first.After(to)
}

// shownEvents are the Events of v in the shown range, or in the cursor
// cell while there is a cursor, newest first.
func (m *Model) shownEvents(v *view) []models.UsageEvent {
	end := m.shownEnd(v)
	from, to := end.Add(-v.span(m)), end
	if v.cursor >= 0 && m.cells > 0 {
		span := to.Sub(from)
		from, to = from.Add(span*time.Duration(v.cursor)/time.Duration(m.cells)), from.Add(span*time.Duration(v.cursor+1)/time.Duration(m.cells))
	}
	var out []models.UsageEvent
	for _, e := range slices.Backward(v.now.Events.Items) {
		if overlaps(e, from, to) {
			out = append(out, e)
		}
	}
	return out
}

// selectedEvent is the Event under the selection of the events widget.
func (m *Model) selectedEvent(v *view) (models.UsageEvent, bool) {
	evs := m.shownEvents(v)
	if v.focus != widgetEvents || v.evSel >= len(evs) {
		return models.UsageEvent{}, false
	}
	return evs[v.evSel], true
}

// laneCells marks each plot cell with the strongest Event in it, and the
// cells before the oldest kept Event as unknown.
func (m *Model) laneCells(v *view, cells int) []rune {
	out := make([]rune, cells)
	for i := range out {
		out[i] = ' '
	}
	end := m.shownEnd(v)
	start := end.Add(-v.span(m))
	span := end.Sub(start)
	if cells <= 0 || span <= 0 {
		return out
	}
	cellOf := func(t time.Time) int { return int(float64(t.Sub(start)) / float64(span) * float64(cells)) }
	if ev := v.now.Events; ev.Missing == "" && !ev.Oldest.IsZero() && ev.Oldest.After(start) {
		for i := 0; i < min(cells, cellOf(ev.Oldest)); i++ {
			out[i] = markUnknown
		}
	}
	for _, e := range v.now.Events.Items {
		if !overlaps(e, start, end) {
			continue
		}
		mark := eventMark(e)
		for _, at := range []time.Time{e.First, e.Last} {
			if at.Before(start) || at.After(end) || (at.Equal(e.First) && !e.FirstKnown) {
				continue
			}
			i := min(cells-1, cellOf(at))
			if markRank(mark) > markRank(out[i]) {
				out[i] = mark
			}
		}
	}
	return out
}

// eventLines are the events widget: a title, the lane under the charts'
// plot cells, and the Events of the range or of the cursor cell.
func (m *Model) eventLines(v *view, w, h int) []string {
	shown := m.shownEvents(v)
	lines := []string{fit(m.eventsHead(v, len(shown)), w)}
	if h < 2 {
		return lines
	}
	lines = append(lines, m.laneLine(v, w))
	if rows := h - 2; rows > 0 {
		lines = append(lines, m.eventRows(v, shown, w, rows)...)
	}
	return lines
}

// eventsHead is the title of the events widget: how many Events, since
// when the API server kept them, and the legend.
func (m *Model) eventsHead(v *view, shown int) string {
	ev := v.now.Events
	head := "  Events"
	if v.focus == widgetEvents {
		head = focusStyle.Render("▸ Events")
	}
	switch {
	case v.now.Loading:
		head += dimStyle.Render(" · loading")
	case ev.Missing != "":
		head += " · cannot be read: " + ev.Missing
	default:
		where := "in range"
		if v.cursor >= 0 {
			where = "in the cell"
		}
		head += fmt.Sprintf(" · %d %s", shown, where)
		if !ev.Oldest.IsZero() {
			head += dimStyle.Render(" · kept from " + ev.Oldest.In(m.loc()).Format(m.timeLayout(v)))
		}
		head += " " + warnStyle.Render(string(markWarning)+"warning") + " " + rolloutStyle.Render(string(markRollout)+"rollout")
	}
	return head
}

// laneLine is the lane, right-aligned like the plots, the cursor cell
// reversed.
func (m *Model) laneLine(v *view, w int) string {
	cells := min(m.cells, max(0, w-1))
	lane := m.laneCells(v, cells)
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", max(0, w-cells)))
	for i, r := range lane {
		s := string(r)
		switch r {
		case markWarning:
			s = warnStyle.Render(s)
		case markRollout:
			s = rolloutStyle.Render(s)
		case markNormal, markUnknown:
			s = dimStyle.Render(s)
		}
		if i == v.cursor {
			s = selStyle.Render(string(r))
		}
		b.WriteString(s)
	}
	return fit(b.String(), w)
}

// eventRows lists the shown Events in rows lines, scrolled to the
// selection, or says why there are none.
func (m *Model) eventRows(v *view, shown []models.UsageEvent, w, rows int) []string {
	ev := v.now.Events
	var lines []string
	v.evSel = min(v.evSel, max(0, len(shown)-1))
	first := 0
	if v.evSel >= rows {
		first = v.evSel - rows + 1
	}
	for i := first; i < len(shown) && len(lines) < rows; i++ {
		row := m.eventRow(v, shown[i])
		if v.focus == widgetEvents && i == v.evSel {
			row = selStyle.Render(fit(ansi.Strip(row), w))
		}
		lines = append(lines, fit(row, w))
	}
	if len(shown) == 0 && ev.Missing == "" && !v.now.Loading {
		note := "  no Events in this range"
		if v.cursor >= 0 {
			note = "  no Events in the cell"
		}
		if !ev.Oldest.IsZero() && m.shownEnd(v).Before(ev.Oldest) {
			note = "  the API server keeps no Events this old"
		}
		lines = append(lines, fit(dimStyle.Render(note), w))
	}
	return lines
}

func (m *Model) timeLayout(v *view) string {
	if v.span(m) >= 24*time.Hour {
		return "01-02 15:04"
	}
	return "15:04:05"
}

// eventRow is one Event: when, its mark, the reason, the object, how
// often, and the message.
func (m *Model) eventRow(v *view, e models.UsageEvent) string {
	mark := string(eventMark(e))
	switch eventMark(e) {
	case markWarning:
		mark = warnStyle.Render(mark)
	case markRollout:
		mark = rolloutStyle.Render(mark)
	}
	obj := e.Kind + " " + e.Object.Name
	if count := e.Count; count > 1 {
		obj += fmt.Sprintf(" ×%d", count)
	}
	return fmt.Sprintf("  %s %s %s %s  %s", e.Last.In(m.loc()).Format(m.timeLayout(v)), mark, e.Reason, obj, dimStyle.Render(firstLine(e.Message)))
}

// eventLogs is the logs request of l: the pod or workload of the
// selected Event, else the dashboard's own pod or workload, around the
// Event's time or the cursor cell.
func (m *Model) eventLogs(v *view) Action {
	var ref models.ObjectRef
	var at time.Time
	if e, ok := m.selectedEvent(v); ok {
		ref, at = e.Object, e.Last
		if ref.GVR != podsGVR && !models.IsInfoWorkload(ref.GVR) {
			return Action{Note: "Logs: " + e.Kind + " " + e.Object.Name + " has no logs"}
		}
	} else {
		r, ok := v.target.Object(v.now.UID)
		if !ok || (v.target.Kind != KindPod && v.target.Kind != KindWorkload) {
			return Action{Note: "Logs: select an Event of a pod or workload, or open a pod or workload dashboard"}
		}
		ref, at = r, m.cursorTime(v)
	}
	from, to := at.Add(-logsAround), at.Add(logsAround)
	if now := m.now(); to.After(now) {
		to = time.Time{} // up to now: a live end
		if from.After(now) {
			from = now
		}
	}
	return Action{Logs: &ref, Window: investigation.Window{Start: from, End: to}}
}

// cursorTime is the middle of the cursor cell, else the shown end.
func (m *Model) cursorTime(v *view) time.Time {
	end := m.shownEnd(v)
	if v.cursor < 0 || m.cells <= 0 {
		return end
	}
	span := v.span(m)
	start := end.Add(-span)
	return start.Add(span * time.Duration(2*v.cursor+1) / time.Duration(2*m.cells))
}
