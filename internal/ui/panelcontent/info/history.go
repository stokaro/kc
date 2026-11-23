package info

import (
	"fmt"
	"math"
	"strings"
	"time"

	models "github.com/stokaro/kc/internal/models"
)

const (
	// historyWindow is the time a sparkline shows, whatever the pane
	// width. kc keeps the samples in memory for this session only.
	historyWindow = 15 * time.Minute
	// pollInterval is how often usage is measured.
	pollInterval = models.UsagePollInterval
	// gapAfter is the longest time between two samples that still counts
	// as continuous: one missed poll is jitter, two are a gap.
	gapAfter = 2*pollInterval + pollInterval/2
	// maxCells is one cell per poll; a wider pane does not stretch the
	// window, so empty cells between polls never look like gaps.
	maxCells = int(historyWindow / pollInterval)
	// maxSamples bounds a series if usage is measured faster than polled.
	maxSamples = 4 * maxCells
)

const (
	gapCell     = '·' // no measurement there
	pendingCell = ' ' // before the first sample
)

// sample is one measurement and when it was taken.
type sample struct {
	at time.Time
	v  float64
}

// series holds the samples of one bar, oldest first, all of one base.
type series struct {
	base    string
	samples []sample
}

func (s *series) add(at time.Time, v float64, base string) {
	if base != s.base {
		// Percentages of another base do not compare; start again.
		s.base, s.samples = base, nil
	}
	s.samples = append(s.samples, sample{at: at, v: v})
	// Keep one sample older than the window: it tells whether the first
	// cell continues a measurement or starts after a gap.
	cut := 0
	for cut+1 < len(s.samples) && at.Sub(s.samples[cut+1].at) >= historyWindow {
		cut++
	}
	if len(s.samples)-cut > maxSamples {
		cut = len(s.samples) - maxSamples
	}
	s.samples = s.samples[cut:]
}

// history holds the usage samples of one scope, node or workload.
type history struct {
	last     time.Time
	cpu, mem series
}

func (h *history) add(at time.Time, cpu, mem usageSample) {
	if !at.After(h.last) {
		return
	}
	h.last = at
	h.cpu.add(at, cpu.percent, cpu.base)
	h.mem.add(at, mem.percent, mem.base)
}

// usageSample is one bar's measured percentage and its base.
type usageSample struct {
	percent float64
	base    string
}

// sparkView is a rendered history: the sparkline and the line that says
// what it shows.
type sparkView struct {
	line, label string
}

// render draws the window that ends at now in at most width cells, with
// the label that says what the cells show.
func (s series) render(now time.Time, width int) (sparkView, bool) {
	inWindow := 0
	for _, sm := range s.samples {
		if now.Sub(sm.at) < historyWindow {
			inWindow++
		}
	}
	what := "history, use %"
	if s.base != "" {
		what = "history, % of " + s.base
	}
	n := min(width, maxCells)
	switch {
	case len(s.samples) == 0 || n <= 0:
		return sparkView{}, false
	case inWindow == 0:
		return sparkView{label: what + ": no sample yet"}, true
	case inWindow == 1:
		return sparkView{label: fmt.Sprintf("%s: collecting, 1 sample", what)}, true
	}
	return sparkView{line: s.cells(now, n), label: s.label(now, what, inWindow)}, true
}

// cells draws n cells of historyWindow/n each, aligned to absolute time,
// so the same sample stays in the same cell as now moves. A cell holds the
// peak of its samples. A cell without one continues the previous value
// when its neighbors are no more than gapAfter apart and shows gapCell
// otherwise; cells before the first sample stay blank.
func (s series) cells(now time.Time, n int) string {
	d := historyWindow / time.Duration(n)
	end := now.UnixNano() / int64(d)
	cells := make([]rune, n)
	j := 0 // the first sample not before the current cell
	var prev *sample
	for k := range n {
		start := time.Unix(0, (end-int64(n-1)+int64(k))*int64(d))
		stop := start.Add(d)
		for j < len(s.samples) && s.samples[j].at.Before(start) {
			prev = &s.samples[j]
			j++
		}
		peak, found := 0.0, false
		for j < len(s.samples) && s.samples[j].at.Before(stop) {
			peak, found = math.Max(peak, s.samples[j].v), true
			prev = &s.samples[j]
			j++
		}
		switch {
		case found:
			cells[k] = sparkRune(peak)
		case prev == nil:
			cells[k] = pendingCell
		case s.continues(prev, j, now):
			cells[k] = sparkRune(prev.v)
		default:
			cells[k] = gapCell
		}
	}
	return string(cells)
}

// continues reports whether the time after prev, up to the sample at
// next (or up to now when there is none), is no gap.
func (s series) continues(prev *sample, next int, now time.Time) bool {
	if next < len(s.samples) {
		return s.samples[next].at.Sub(prev.at) <= gapAfter
	}
	return now.Sub(prev.at) <= gapAfter
}

// label names the base, the samples in the window and their span, the
// gaps, a stale end and the retention.
func (s series) label(now time.Time, what string, inWindow int) string {
	first, last, gaps := -1, -1, 0
	for i, sm := range s.samples {
		if now.Sub(sm.at) >= historyWindow {
			continue
		}
		if first < 0 {
			first = i
		} else if sm.at.Sub(s.samples[i-1].at) > gapAfter {
			gaps++
		}
		last = i
	}
	parts := []string{what, fmt.Sprintf("%d samples over %s", inWindow, shortAge(s.samples[last].at.Sub(s.samples[first].at)))}
	switch {
	case gaps == 1:
		parts = append(parts, "1 gap")
	case gaps > 1:
		parts = append(parts, fmt.Sprintf("%d gaps", gaps))
	}
	if age := now.Sub(s.samples[last].at); age > gapAfter {
		parts = append(parts, fmt.Sprintf("stale, last %s ago", shortAge(age)))
	}
	return strings.Join(parts, " · ")
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// sparkRune is the block for a percentage (0 to 100).
func sparkRune(v float64) rune {
	v = math.Max(0, math.Min(v, 100))
	return sparks[int(math.Round(v/100*float64(len(sparks)-1)))]
}
