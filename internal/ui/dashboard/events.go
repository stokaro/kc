package dashboard

import (
	"slices"
	"strconv"
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
	warnStyle    = func() lipgloss.Style { return on(lipgloss.NewStyle().Foreground(lipgloss.BrightRed).Bold(true)) }
	rolloutStyle = func() lipgloss.Style { return on(lipgloss.NewStyle().Foreground(lipgloss.BrightCyan).Bold(true)) }
)

// eventsListAt is the first line of the Events list inside its frame:
// after the lane, its labels and the column headers.
const eventsListAt = 3

// Lane marks, strongest first.
const (
	markWarning = '▲'
	markRollout = '◆'
	markNormal  = '•'
	markUnknown = '░' // before the oldest Event the API server kept
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

// shownEvents are the Events of v in the chosen range (also before a
// chart narrowed to the session's data), or in the cursor cell while
// there is a cursor, newest first.
func (m *Model) shownEvents(v *view) []models.UsageEvent {
	end := m.shownEnd(v)
	from, to := end.Add(-v.span(m)), end
	if v.cursor >= 0 && m.cells > 0 {
		from = m.shownStart(v)
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
	start := m.shownStart(v)
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

// eventsFrame is the Events widget: the lane on the charts' time cells
// with the reasons written under the marks, then the Events of the range
// or of the cursor cell in columns.
func (m *Model) eventsFrame(v *view, r rect) []string {
	shown := m.shownEvents(v)
	ev := v.now.Events
	title := "Events"
	switch {
	case v.now.Loading:
		title += " · loading"
	case ev.Missing != "":
		title += " · cannot be read: " + ev.Missing
	case v.cursor >= 0:
		title += " in the cursor cell: " + strconv.Itoa(len(shown))
	default:
		title += " in the last " + longDuration(v.span(m)) + ": " + strconv.Itoa(len(shown))
	}
	if ev.Missing == "" && !v.now.Loading {
		title += " · ▲ warning ◆ rollout • other"
	}
	footer := ""
	if !ev.Oldest.IsZero() {
		footer = "the API server keeps them since " + ev.Oldest.In(m.loc()).Format(m.timeLayout(v))
	}
	inner := r.w - 2
	lead := max(0, r.plotX-(r.x+1))
	cells := min(m.cells, max(0, inner-lead))
	lane := m.laneCells(v, cells)
	body := []string{
		noteStyle().Render(laneLead(lead)) + m.laneLine(v, lane),
		strings.Repeat(" ", lead) + m.laneLabels(v, lane, cells),
		headStyle().Render(fit(eventHeader(inner, counted(shown)), inner)),
	}
	if rows := r.h - 2 - eventsListAt; rows > 0 {
		body = append(body, m.eventRows(v, shown, inner, rows)...)
	}
	return frame(title, footer, v.focus == widgetEvents, r.w, r.h, body)
}

// laneLine draws the lane cells, the cursor cell reversed.
func (m *Model) laneLine(v *view, lane []rune) string {
	var b strings.Builder
	for i, r := range lane {
		s := string(r)
		switch r {
		case markWarning:
			s = warnStyle().Render(s)
		case markRollout:
			s = rolloutStyle().Render(s)
		case markNormal:
			s = noteStyle().Render(s)
		case markUnknown:
			s = gapStyle.Render(" ")
		}
		if i == v.cursor {
			s = lipgloss.NewStyle().Reverse(true).Render(string(r))
		}
		b.WriteString(s)
	}
	return b.String()
}

// laneLabels writes under the lane what its marks are: the reason and the
// count of the strongest Events first, where they fit, and where kc does
// not know what happened.
func (m *Model) laneLabels(v *view, lane []rune, cells int) string {
	out := make([]rune, cells)
	for i := range out {
		out[i] = ' '
	}
	kind := make([]rune, cells)
	// A label keeps a blank cell before and after another label.
	free := func(at, n int) bool {
		if at < 0 || at+n > cells {
			return false
		}
		for i := max(0, at-1); i < min(cells, at+n+1); i++ {
			if out[i] != ' ' {
				return false
			}
		}
		return true
	}
	put := func(at int, text string, mark rune) {
		for i, r := range []rune(text) {
			out[at+i], kind[at+i] = r, mark
		}
	}
	unknown := 0
	for unknown < cells && lane[unknown] == markUnknown {
		unknown++
	}
	if n := m.earlierEvents(v); n > 0 && unknown == 0 {
		// The chart may cover seconds of a range of minutes; the lane
		// says where the others are rather than when the chart starts,
		// without "listed below" when the lane is too short for it.
		earlier := "← " + strings.Replace(plural(n, "Event"), " ", " earlier ", 1)
		for _, text := range []string{earlier + ", listed below", earlier} {
			if free(0, len([]rune(text))) {
				put(0, text, markNormal)
				break
			}
		}
	}
	for _, l := range m.markLabels(v, cells) {
		n := len([]rune(l.text))
		switch {
		case free(l.at, n):
			put(l.at, l.text, l.mark)
		case free(l.at-n+1, n):
			put(l.at-n+1, l.text, l.mark)
		}
	}
	// What kc does not know goes in the room the marks left, in as many
	// words as fit.
	if unknown > 0 {
		at := v.now.Events.Oldest.In(m.loc()).Format("15:04")
		for _, text := range []string{"no Events kept before " + at, "none kept before " + at, "none before " + at, "expired"} {
			if n := len([]rune(text)); free(0, n) {
				put(0, text, markUnknown)
				break
			}
		}
	}
	return styleLabels(out, kind)
}

// laneLabel is the text written under a mark of the lane.
type laneLabel struct {
	at   int
	text string
	mark rune
}

// markLabels are the labels of the Events in the chart's span, the
// strongest first and, among equals, the newest.
func (m *Model) markLabels(v *view, cells int) []laneLabel {
	var labels []laneLabel
	end := m.shownEnd(v)
	start := m.shownStart(v)
	span := end.Sub(start)
	for _, e := range v.now.Events.Items {
		if span <= 0 || e.Last.Before(start) || e.Last.After(end) {
			continue
		}
		at := min(cells-1, int(float64(e.Last.Sub(start))/float64(span)*float64(cells)))
		text := e.Reason
		if e.Count > 1 {
			text += " ×" + strconv.Itoa(int(e.Count))
		}
		labels = append(labels, laneLabel{at: at, text: text, mark: eventMark(e)})
	}
	slices.SortStableFunc(labels, func(a, b laneLabel) int {
		if c := markRank(b.mark) - markRank(a.mark); c != 0 {
			return c
		}
		return b.at - a.at
	})
	return labels
}

// styleLabels draws each label rune in the color of its mark.
func styleLabels(out, kind []rune) string {
	var b strings.Builder
	for i, r := range out {
		s := string(r)
		switch kind[i] {
		case markWarning:
			s = warnStyle().Render(s)
		case markRollout:
			s = rolloutStyle().Render(s)
		case markUnknown:
			s = gapStyle.Render(s)
		}
		b.WriteString(s)
	}
	return b.String()
}

// earlierEvents counts the Events of the chosen range before the chart's
// start, when the chart narrowed to the session's data (fits): the list
// shows them, the lane cannot.
func (m *Model) earlierEvents(v *view) int {
	start, ok := m.fits(v)
	if !ok {
		return 0
	}
	from := m.shownEnd(v).Add(-v.span(m))
	n := 0
	for _, e := range v.now.Events.Items {
		if e.Last.Before(start) && !e.Last.Before(from) {
			n++
		}
	}
	return n
}

// Column widths of the Events list.
const (
	colTime   = 10
	colType   = 10
	colReason = 18
	colObject = 34
	colCount  = 6
)

// eventHeader names the columns of the Events list; Count only when an
// Event of the list happened more than once.
func eventHeader(w int, count bool) string {
	h := pad("Time", colTime) + pad("Type", colType) + pad("Reason", colReason)
	if w >= 100 {
		h += pad("Object", colObject)
	}
	if count {
		h += lpad("Count", colCount) + "  "
	}
	return h + "Message"
}

// counted reports whether an Event of shown happened more than once.
func counted(shown []models.UsageEvent) bool {
	for _, e := range shown {
		if e.Count > 1 {
			return true
		}
	}
	return false
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
	count := counted(shown)
	for i := first; i < len(shown) && len(lines) < rows; i++ {
		row := m.eventRow(v, shown[i], w, count)
		if v.focus == widgetEvents && i == v.evSel {
			row = curStyle().Render(fit(ansi.Strip(row), w))
		}
		lines = append(lines, row)
	}
	if len(shown) == 0 && ev.Missing == "" && !v.now.Loading {
		note := "no Events in this range"
		if v.cursor >= 0 {
			note = "no Events in the cursor cell"
		}
		if !ev.Oldest.IsZero() && m.shownEnd(v).Before(ev.Oldest) {
			note = "the API server keeps no Events this old"
		}
		lines = append(lines, noteStyle().Render(note))
	}
	return lines
}

func (m *Model) timeLayout(v *view) string {
	if v.span(m) >= 24*time.Hour {
		return "01-02 15:04"
	}
	return "15:04:05"
}

// eventRow is one Event in the columns of eventHeader.
func (m *Model) eventRow(v *view, e models.UsageEvent, w int, count bool) string {
	typ := string(eventMark(e)) + " "
	st := noteStyle()
	switch eventMark(e) {
	case markWarning:
		typ, st = typ+"Warning", warnStyle()
	case markRollout:
		typ, st = typ+"Rollout", rolloutStyle()
	default:
		typ += "Normal"
	}
	row := baseStyle().Render(pad(e.Last.In(m.loc()).Format(m.timeLayout(v)), colTime)) +
		st.Render(pad(typ, colType)) + baseStyle().Render(pad(truncate(e.Reason, colReason-1), colReason))
	if w >= 100 {
		row += baseStyle().Render(pad(truncate(e.Kind+" "+e.Object.Name, colObject-1), colObject))
	}
	if count {
		row += baseStyle().Render(lpad(strconv.Itoa(int(max(1, e.Count))), colCount) + "  ")
	}
	return row + noteStyle().Render(firstLine(e.Message))
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
	start := m.shownStart(v)
	span := end.Sub(start)
	return start.Add(span * time.Duration(2*v.cursor+1) / time.Duration(2*m.cells))
}

// laneLead is the label left of the lane, in the room of the charts'
// value labels, or blanks when it does not fit.
func laneLead(lead int) string {
	return strings.Repeat(" ", lead)
}
