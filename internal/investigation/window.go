// Package investigation holds the time window an investigation looks at,
// shared by the panes that can show history for a period: the Logs and
// Info panels.
package investigation

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxRelative bounds a live window. Longer ones would ask the API server
// for logs nobody reads in a panel and that the panel could not keep.
const MaxRelative = 7 * 24 * time.Hour

// Window is a period of time. The zero Window is off: each pane uses its
// own settings.
type Window struct {
	// Last is a live window that ends now and moves with the clock.
	Last time.Duration
	// Start and End are a fixed window. End may be zero: up to now.
	Start, End time.Time
}

// Off reports whether no window is set.
func (w Window) Off() bool { return w.Last <= 0 && w.Start.IsZero() }

// Live reports whether the window ends now: a relative window, or a fixed
// one without an end or with an end in the future.
func (w Window) Live(now time.Time) bool {
	if w.Off() || w.Last > 0 {
		return !w.Off()
	}
	return w.End.IsZero() || w.End.After(now)
}

// Bounds returns the window's start and end at now. end is zero for a live
// window. ok is false when the window is off.
func (w Window) Bounds(now time.Time) (start, end time.Time, ok bool) {
	switch {
	case w.Off():
		return time.Time{}, time.Time{}, false
	case w.Last > 0:
		return now.Add(-w.Last), time.Time{}, true
	}
	if !w.End.IsZero() && w.End.After(now) {
		return w.Start, time.Time{}, true
	}
	return w.Start, w.End, true
}

// layouts are the accepted times of a fixed window, in the given location.
var layouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"}

// clockLayouts are times of the current day.
var clockLayouts = []string{"15:04:05", "15:04"}

// Parse reads a window as the dialog accepts it:
//
//   - "" or "off": no window;
//   - a duration such as "15m", "2h" or "1h30m": the last 15 minutes, live;
//   - "START .. END" or "START ..": a fixed window, each side
//     "2006-01-02 15:04[:05]", "15:04[:05]" (today) or RFC 3339, in loc
//     unless RFC 3339 gives the offset.
//
// now is used for "today" and to refuse a window that starts in the future.
func Parse(s string, now time.Time, loc *time.Location) (Window, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "off") {
		return Window{}, nil
	}
	if !strings.Contains(s, "..") {
		d, err := time.ParseDuration(s)
		if err != nil {
			return Window{}, errors.New(`use a duration such as 15m or 2h, or "START .. END"`)
		}
		if d <= 0 {
			return Window{}, errors.New("the duration must be positive")
		}
		if d > MaxRelative {
			return Window{}, fmt.Errorf("the longest live window is %s", formatDuration(MaxRelative))
		}
		return Window{Last: d}, nil
	}
	left, right, _ := strings.Cut(s, "..")
	start, err := parseTime(strings.TrimSpace(left), now, loc)
	if err != nil {
		return Window{}, fmt.Errorf("start: %w", err)
	}
	var end time.Time
	if r := strings.TrimSpace(right); r != "" {
		if end, err = parseTime(r, now, loc); err != nil {
			return Window{}, fmt.Errorf("end: %w", err)
		}
		if !end.After(start) {
			return Window{}, errors.New("the end must be after the start")
		}
	}
	if start.After(now) {
		return Window{}, errors.New("the start is in the future")
	}
	return Window{Start: start, End: end}, nil
}

func parseTime(s string, now time.Time, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("missing")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	day := now.In(loc)
	for _, l := range clockLayouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a time such as 2026-11-23 10:00 or 10:00", s)
}

// Text is the window as Parse reads it back, in loc.
func (w Window) Text(loc *time.Location) string {
	switch {
	case w.Off():
		return ""
	case w.Last > 0:
		return formatDuration(w.Last)
	}
	s := w.Start.In(loc).Format("2006-01-02 15:04:05") + " .."
	if !w.End.IsZero() {
		s += " " + w.End.In(loc).Format("2006-01-02 15:04:05")
	}
	return s
}

// Label is the window as a pane shows it, with the time zone: "last 1h"
// or "2026-11-23 10:00:00 .. 11:30:00 +01:00".
func (w Window) Label(loc *time.Location) string {
	switch {
	case w.Off():
		return ""
	case w.Last > 0:
		return "last " + formatDuration(w.Last)
	}
	start := w.Start.In(loc)
	s := start.Format("2006-01-02 15:04:05") + " .. "
	switch end := w.End.In(loc); {
	case w.End.IsZero():
		s += "now"
	case end.Year() == start.Year() && end.YearDay() == start.YearDay():
		s += end.Format("15:04:05")
	default:
		s += end.Format("2006-01-02 15:04:05")
	}
	return s + " " + start.Format("-07:00")
}

func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
