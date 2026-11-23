package info

import (
	"fmt"
	"strings"
	"time"
)

// seriesCoverage is how much of an interval one series covers. A sample
// covers the gapAfter that follows it, the same tolerance the sparkline
// uses: two samples no more than gapAfter apart are continuous, and a
// last sample older than gapAfter is stale.
type seriesCoverage struct {
	any     bool      // the series has samples at all
	first   time.Time // the first covered instant; zero when none is covered
	late    bool      // the interval starts before first
	gaps    int       // holes between first and the last sample
	staleAt time.Time // the last sample, when it no longer covers the end
}

// coverage is how much of [from, to] s covers. Samples older than
// historyWindow before now are not shown, so they do not cover.
func (s series) coverage(from, to, now time.Time) seriesCoverage {
	c := seriesCoverage{any: len(s.samples) > 0}
	lo := from
	if shown := now.Add(-historyWindow); lo.Before(shown) {
		lo = shown
	}
	if !to.After(lo) {
		return c
	}
	type span struct{ start, end, last time.Time }
	var spans []span
	for _, sm := range s.samples {
		if sm.at.After(to) {
			break
		}
		end := sm.at.Add(gapAfter)
		if n := len(spans); n > 0 && !sm.at.After(spans[n-1].end) {
			spans[n-1].end, spans[n-1].last = end, sm.at
			continue
		}
		spans = append(spans, span{start: sm.at, end: end, last: sm.at})
	}
	var kept []span
	for _, sp := range spans {
		if sp.end.After(lo) {
			kept = append(kept, sp)
		}
	}
	if len(kept) == 0 {
		return c
	}
	c.first = kept[0].start
	if c.first.Before(lo) {
		c.first = lo
	}
	c.late = c.first.After(from)
	c.gaps = len(kept) - 1
	if last := kept[len(kept)-1]; last.end.Before(to) {
		c.staleAt = last.last
	}
	return c
}

func (c seriesCoverage) complete() bool {
	return !c.first.IsZero() && !c.late && c.gaps == 0 && c.staleAt.IsZero()
}

// describe says what is missing, as "from 11:58:00 only, 1 gap, stale
// since 11:59:00"; "" when the interval is covered.
func (c seriesCoverage) describe(loc *time.Location) string {
	if c.first.IsZero() {
		return "no samples"
	}
	var parts []string
	if c.late {
		parts = append(parts, "from "+c.first.In(loc).Format("15:04:05")+" only")
	}
	switch {
	case c.gaps == 1:
		parts = append(parts, "1 gap")
	case c.gaps > 1:
		parts = append(parts, fmt.Sprintf("%d gaps", c.gaps))
	}
	if !c.staleAt.IsZero() {
		parts = append(parts, "stale since "+c.staleAt.In(loc).Format("15:04:05"))
	}
	return strings.Join(parts, ", ")
}

// coverageText is what windowCoverage says after the window label, from
// the coverage of the CPU and the memory history.
func coverageText(cpu, mem seriesCoverage, loc *time.Location) string {
	switch {
	case !cpu.any && !mem.any:
		return "no session usage history yet (incomplete)"
	case cpu.first.IsZero() && mem.first.IsZero():
		return "no session usage history for it (incomplete)"
	case cpu.complete() && mem.complete():
		return "covered by the session usage history"
	}
	c, m := cpu.describe(loc), mem.describe(loc)
	if c == m {
		return "session usage history " + c + " (incomplete)"
	}
	if c == "" {
		c = "covered"
	}
	if m == "" {
		m = "covered"
	}
	return "session usage history: CPU " + c + ", memory " + m + " (incomplete)"
}
