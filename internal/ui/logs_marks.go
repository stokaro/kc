package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/logview"
)

// Keys of the marks and the severity jumps in the logs viewer
// (docs/adr/0273-log-marks-and-severity-jumps.md). They are plain letters,
// like the viewer's j, k, g, G, n and N, so they take no modifier key from
// custom commands.
const (
	logsMarkKey      = "m"
	logsNextMarkKey  = "u"
	logsPrevMarkKey  = "U"
	logsNextIssueKey = "e"
	logsPrevIssueKey = "E"
	logsCopyMarksKey = "c"
)

// logsMaxMarks bounds the marks of one viewer. A mark holds a record
// number only: the record itself stays under the retention limits and a
// mark of a dropped record is reported and forgotten.
const logsMaxMarks = 100

// logsMarks are the marked records of a viewer and the record kc points
// the user at after a jump. Both are record numbers (logview.Seq), so
// they keep to their records when the filter, the view mode or Solo
// change the display lines.
type logsMarks struct {
	set     map[logview.Seq]struct{}
	pointer logview.Seq
	pointed bool
}

func (m *logsMarks) clearPointer() { m.pointed = false }

func (m *logsMarks) has(seq logview.Seq) bool {
	_, ok := m.set[seq]
	return ok
}

// sorted returns the marks in record order.
func (m *logsMarks) sorted() []logview.Seq {
	out := make([]logview.Seq, 0, len(m.set))
	for seq := range m.set {
		out = append(out, seq)
	}
	slices.Sort(out)
	return out
}

// pruneMarks forgets the marks whose records were dropped to stay within
// the limits and returns how many.
func (v *LogsViewer) pruneMarks() int {
	n := 0
	for seq := range v.marks.set {
		if _, err := v.log.Record(seq); err != nil {
			delete(v.marks.set, seq)
			n++
		}
	}
	return n
}

// pointedRecord returns the record kc points at, while it is shown.
func (v *LogsViewer) pointedRecord() (logview.Seq, bool) {
	if !v.marks.pointed || v.context != nil {
		return 0, false
	}
	if _, ok := v.log.DisplayLine(v.marks.pointer); !ok {
		return 0, false
	}
	return v.marks.pointer, true
}

// syncPointer highlights the pointed record's display line, which moves
// as older lines are dropped or the filter changes.
func (v *LogsViewer) syncPointer() {
	line := -1
	if seq, ok := v.pointedRecord(); ok {
		line, _ = v.log.DisplayLine(seq)
	}
	v.inner.SetPointer(line)
}

// pointAt points at seq and scrolls it into view; the view stops
// following.
func (v *LogsViewer) pointAt(seq logview.Seq) {
	v.marks.pointer, v.marks.pointed = seq, true
	v.showRecord(seq)
}

// handleMarkKey handles the mark and jump keys. The context view has its
// own rows, so they work in the log view only.
func (v *LogsViewer) handleMarkKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if v.inner.SearchMode() {
		return nil, false
	}
	switch msg.String() {
	case logsMarkKey, logsNextMarkKey, logsPrevMarkKey, logsNextIssueKey, logsPrevIssueKey, logsCopyMarksKey:
	default:
		return nil, false
	}
	if msg.String() == logsCopyMarksKey {
		return v.copyMarks(), true
	}
	if v.context != nil {
		v.note = "Marks and jumps work in the log view; M-z leaves the context"
		return nil, true
	}
	switch msg.String() {
	case logsMarkKey:
		v.toggleMark()
	case logsNextMarkKey, logsPrevMarkKey:
		v.jumpToMark(msg.String() == logsPrevMarkKey)
	case logsNextIssueKey, logsPrevIssueKey:
		v.jumpToIssue(msg.String() == logsPrevIssueKey)
	}
	return nil, true
}

// toggleMark marks the focused record, or unmarks it.
func (v *LogsViewer) toggleMark() {
	dropped := v.pruneMarks()
	seq, ok := v.focusRecord()
	if !ok {
		v.note = "No line to mark"
		return
	}
	switch {
	case v.marks.has(seq):
		delete(v.marks.set, seq)
		v.note = fmt.Sprintf("Unmarked; %d marked", len(v.marks.set))
	case len(v.marks.set) >= logsMaxMarks:
		v.note = fmt.Sprintf("At most %d lines can be marked; unmark one first", logsMaxMarks)
		return
	default:
		if v.marks.set == nil {
			v.marks.set = make(map[logview.Seq]struct{})
		}
		v.marks.set[seq] = struct{}{}
		v.note = fmt.Sprintf("Marked; %d marked. u and U go to the next and previous mark, c copies them", len(v.marks.set))
	}
	v.marks.pointer, v.marks.pointed = seq, true
	v.note += droppedMarksNote(dropped)
}

// jumpToMark points at the next marked record that is shown, or the
// previous one.
func (v *LogsViewer) jumpToMark(backward bool) {
	dropped := v.pruneMarks()
	if len(v.marks.set) == 0 {
		v.note = "No marked lines; m marks the line in focus" + droppedMarksNote(dropped)
		return
	}
	v.jump(backward, v.marks.has, "marked line")
	v.note += droppedMarksNote(dropped)
}

// jumpToIssue points at the next shown record whose level is warn or
// error, or the previous one.
func (v *LogsViewer) jumpToIssue(backward bool) {
	issue := func(seq logview.Seq) bool { return v.log.Severity(seq) >= logview.SeverityWarn }
	v.jump(backward, issue, "warning or error (a JSON level field)")
}

// jump points at the next shown record that matches after the pointed
// one, or the previous one. Without a pointed record it searches the view
// from its top line down, or from its bottom line up, so a match already
// on screen is found first.
func (v *LogsViewer) jump(backward bool, match func(logview.Seq) bool, what string) {
	from, ok := v.jumpOrigin(backward)
	if !ok {
		v.note = "No lines"
		return
	}
	seq, ok := v.log.Next(from, backward, match)
	if !ok {
		dir := "after"
		if backward {
			dir = "before"
		}
		v.note = fmt.Sprintf("No %s %s this line among the shown lines", what, dir)
		return
	}
	v.pointAt(seq)
}

// jumpOrigin is the record a jump starts after (before, backward).
func (v *LogsViewer) jumpOrigin(backward bool) (logview.Seq, bool) {
	if seq, ok := v.pointedRecord(); ok {
		return seq, true
	}
	end, total := v.inner.Position()
	if total == 0 {
		return 0, false
	}
	if backward {
		seq, ok := v.log.SeqAt(end - 1)
		return seq + 1, ok
	}
	seq, ok := v.log.SeqAt(v.inner.Offset())
	return seq - 1, ok
}

// copyMarks copies the marked records in record order: each with its
// source in a merged log and its original line, which starts with the
// kubelet timestamp when the stream has timestamps.
func (v *LogsViewer) copyMarks() tea.Cmd {
	dropped := v.pruneMarks()
	if len(v.marks.set) == 0 {
		v.note = "No marked lines to copy" + droppedMarksNote(dropped)
		return nil
	}
	var b strings.Builder
	for _, seq := range v.marks.sorted() {
		r, err := v.log.Record(seq)
		if err != nil {
			continue
		}
		if src := r.Source.String(); src != "" {
			b.WriteString("[" + src + "] ")
		}
		b.WriteString(r.Raw)
		b.WriteByte('\n')
	}
	what := fmt.Sprintf("%d marked lines", len(v.marks.set))
	if dropped > 0 {
		what += fmt.Sprintf(" (%d more were dropped to stay within the limits)", dropped)
	}
	return viewerCopyRequest(strings.TrimSuffix(b.String(), "\n"), what)
}

// marksStatus counts the marks for the status line.
func (v *LogsViewer) marksStatus() string {
	if len(v.marks.set) == 0 {
		return ""
	}
	text := fmt.Sprintf("%d marked", len(v.marks.set))
	if seq, ok := v.focusRecord(); ok && v.marks.has(seq) {
		text += ", this line marked"
	}
	return text
}

func droppedMarksNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d marked lines were dropped to stay within the limits)", n)
}
