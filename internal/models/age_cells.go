package models

import (
	"time"

	"k8s.io/apimachinery/pkg/util/duration"
)

// ageCellHook rewrites relative time cells on the fly: each column shows
// the time since its own moment. Age columns count from the creation
// timestamp; an Event's Last Seen and First Seen count from its
// occurrences.
type ageCellHook struct {
	cols  []int
	times []time.Time
	now   func() time.Time
}

// newAgeCellHook returns a hook that shows the time since created in every
// column of cols, or nil when there is nothing to show.
func newAgeCellHook(cols []int, created time.Time) *ageCellHook {
	times := make([]time.Time, len(cols))
	for i := range times {
		times[i] = created
	}
	return newTimeCellHook(cols, times)
}

// newTimeCellHook returns a hook that shows the time since times[i] in
// column cols[i]. A zero time keeps the server's cell. It returns nil when
// no column has a time.
func newTimeCellHook(cols []int, times []time.Time) *ageCellHook {
	h := &ageCellHook{now: time.Now}
	for i, c := range cols {
		if i < len(times) && !times[i].IsZero() {
			h.cols = append(h.cols, c)
			h.times = append(h.times, times[i])
		}
	}
	if len(h.cols) == 0 {
		return nil
	}
	return h
}

func (h *ageCellHook) apply(cells []string) []string {
	if h == nil || len(h.cols) == 0 || cells == nil {
		return cells
	}
	nowFn := h.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	for i, idx := range h.cols {
		if idx >= 0 && idx < len(cells) {
			cells[idx] = duration.HumanDuration(now.Sub(h.times[i]))
		}
	}
	return cells
}

// nextInterval is how long the cells stay the same: the shortest step of
// the most recent time.
func (h *ageCellHook) nextInterval(now time.Time) time.Duration {
	if h == nil || len(h.cols) == 0 {
		return 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	var interval time.Duration
	for _, t := range h.times {
		d := ageStep(max(now.Sub(t), 0))
		if interval == 0 || d < interval {
			interval = d
		}
	}
	return interval
}

// ageStep is the precision HumanDuration prints an elapsed time with.
func ageStep(elapsed time.Duration) time.Duration {
	switch {
	case elapsed < 10*time.Minute:
		return time.Second
	case elapsed < 8*time.Hour:
		return time.Minute
	case elapsed < 8*24*time.Hour:
		return time.Hour
	default:
		return 24 * time.Hour
	}
}
