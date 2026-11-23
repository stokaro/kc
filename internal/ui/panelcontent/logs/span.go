package logs

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/investigation"
)

// Span is the investigation window as the Logs panel applies it. The
// stream starts at the window's start (--since-time) and reads the whole
// window, as far as the panel's line limits allow. A window that ended
// does not follow; its lines after the end are hidden, which needs the
// kubelet timestamps, so they are turned on.
type Span struct {
	Window investigation.Window
	// Loc is the time zone of the labels.
	Loc *time.Location
}

// spanState is the span resolved for the current target. A live window
// resolves its start when the target changes, not on every redraw, so
// the same target keeps its stream.
type spanState struct {
	span         Span
	since, until time.Time
	// hidden counts lines after the end; first is the time of the first
	// line kept, when the lines carry timestamps.
	hidden int
	first  time.Time
}

// SetSpan applies the investigation window; the zero Span turns it off. A
// running stream restarts with the window.
func (w *Widget) SetSpan(s Span) tea.Cmd {
	w.span.span = s
	w.resolveSpan()
	if w.state == stateIdle {
		return nil
	}
	return w.restart(false)
}

// Span returns the applied window.
func (w *Widget) Span() Span { return w.span.span }

func (w *Widget) resolveSpan() {
	w.span.since, w.span.until, _ = w.span.span.Window.Bounds(w.now())
}

func (w *Widget) now() time.Time {
	if w.opts.Now != nil {
		return w.opts.Now()
	}
	return time.Now()
}

// applySpan narrows r to the window.
func (w *Widget) applySpan(r Request) Request {
	if w.span.since.IsZero() {
		return r
	}
	r.Spec.SinceTime = w.span.since
	r.Spec.SinceSeconds = 0
	// The whole window; the panel's line limits drop the oldest lines.
	r.Spec.TailLines = -1
	if !w.span.until.IsZero() {
		r.Spec.Follow = false
		r.Spec.Timestamps = true
	}
	return r
}

// cutAfterEnd drops lines stamped after the window's end and notes the
// first kept line's time. Lines without a timestamp (kc's own notes) stay.
func (w *Widget) cutAfterEnd(lines []string) []string {
	if w.span.since.IsZero() {
		return lines
	}
	out := lines[:0]
	for _, line := range lines {
		at, ok := lineTime(line)
		if ok && !w.span.until.IsZero() && at.After(w.span.until) {
			w.span.hidden++
			continue
		}
		if ok && w.span.first.IsZero() {
			w.span.first = at
		}
		out = append(out, line)
	}
	return out
}

// lineTime reads the kubelet timestamp at the start of line, after the
// "[pod/container] " prefix of a merged stream.
func lineTime(line string) (time.Time, bool) {
	if strings.HasPrefix(line, "[") {
		if i := strings.Index(line, "] "); i > 0 {
			line = line[i+2:]
		}
	}
	end := strings.IndexByte(line, ' ')
	if end < len("2006-01-02T15:04:05Z") {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, line[:end])
	return t, err == nil
}

// spanStatus is the window as the header shows it, with what the stream
// covers.
func (w *Widget) spanStatus() string {
	if w.span.since.IsZero() {
		return ""
	}
	loc := w.span.span.Loc
	if loc == nil {
		loc = time.Local
	}
	parts := []string{"window " + w.span.span.Window.Label(loc)}
	if !w.span.first.IsZero() && w.span.first.Sub(w.span.since) > time.Minute {
		parts = append(parts, "first line "+w.span.first.In(loc).Format("15:04:05"))
	}
	if w.span.hidden > 0 {
		parts = append(parts, fmt.Sprintf("%d after the end hidden", w.span.hidden))
	}
	return strings.Join(parts, ", ")
}
