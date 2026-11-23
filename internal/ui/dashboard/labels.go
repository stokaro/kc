package dashboard

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/promhistory"
	"github.com/stokaro/kc/internal/ui/chart"
)

// legend is the first line inside a chart's frame: in words, what each
// line is and its value now, or in the cursor cell; the share of the
// request in use and how close the usage is to the limit.
//
//nolint:gocyclo // one short case per state of the legend
func (m *Model) legend(v *view, wd widget, c chart.Chart) []string {
	a := v.now.CPU
	if wd == widgetMemory {
		a = v.now.Memory
	}
	switch {
	case v.now.Loading:
		return []string{noteStyle().Render("Loading the cluster's objects…")}
	case v.now.Err != "":
		return []string{badStyle().Render("Metrics API: " + v.now.Err)}
	case !v.now.Found && v.now.Note == "":
		return []string{badStyle().Render(v.target.Title() + " is not in the lists kc reads; it may be gone")}
	}
	use, useKnown := a.Used, a.UsedKnown
	req, lim := a.Requests, a.Limits
	limKnown := !a.LimitsPartial
	at := "Now"
	if v.paused {
		at = "At pause"
	}
	if v.cursor >= 0 {
		cv := c.Cell(v.cursor)
		at = "At " + cv.From.In(m.loc()).Format("15:04:05")
		use, useKnown = cv.V, cv.OK
		var ok bool
		if req, ok = c.RefAt(refRequests, v.cursor); !ok {
			req = 0
		}
		// A history has a limit only where every container had one.
		lim, limKnown = c.RefAt(refLimits, v.cursor)
		limKnown = limKnown && lim > 0
	}
	parts := []string{valStyle().Render(at + ":")}
	swatch := func(st lipgloss.Style, r string) string {
		return on(lipgloss.NewStyle().Foreground(st.GetForeground()).Bold(true)).Render(r)
	}
	useText := "no data"
	switch {
	case v.cursor >= 0 && !useKnown:
		useText = "no sample"
	case v.cursor >= 0 && use <= 0:
		useText = "none"
	case v.cursor >= 0:
		// A cell pools several samples; the chart draws their peak.
		useText = c.Format(use) + " (highest)"
	case useKnown:
		useText = c.Format(use)
	case v.now.Note != "":
		useText = v.now.Note
	}
	parts = append(parts, swatch(useStyle, "█")+" usage "+valStyle().Render(useText))
	if u := v.prom.usage; m.usesProm(v) && !u.Resources.Known() {
		why := "no kube-state-metrics container series"
		if u.ResourcesErr != "" {
			why = u.ResourcesErr
		}
		return append(parts, noteStyle().Render("no request or limit history: "+why))
	}
	share := func(ref float64, of string) string {
		if !useKnown || ref <= 0 {
			return ""
		}
		return " (" + strconv.Itoa(int(use/ref*100+0.5)) + "% " + of + ")"
	}
	above := func(i int) string {
		x := v.cursor
		if x < 0 {
			x = c.Cells() - 1
		}
		if c.OffScaleAt(i, x) {
			return " ↑ above"
		}
		return ""
	}
	reqText := "none set"
	if req > 0 {
		reqText = c.Format(req) + share(req, "used") + above(refRequests)
	}
	reqPart := swatch(reqStyle, "┄") + " request " + reqText
	if useKnown && req > 0 && use > req {
		reqPart = swatch(reqStyle, "┄") + overStyle().Render(" over the request "+c.Format(req)+share(req, "of it"))
	}
	parts = append(parts, reqPart)
	limPart := swatch(limStyle, "┄") + " limit "
	switch {
	case !limKnown:
		limPart += "missing on some pods"
	case lim <= 0:
		limPart += "none set"
	case useKnown && use >= limitNear*lim:
		limPart = swatch(limStyle, "┄") + badStyle().Render(" near the limit "+c.Format(lim)+share(lim, "of it"))
	default:
		limPart += c.Format(lim) + above(refLimits)
	}
	parts = append(parts, limPart)
	if a.Capacity > 0 {
		parts = append(parts, swatch(allocStyle, "┄")+" allocatable "+c.Format(a.Capacity)+above(refCapacity))
	}
	return parts
}

// legendLines packs the parts of a legend into lines of width w, two at
// most: a narrow chart gives up a plot row rather than cut the legend.
func legendLines(parts []string, w int) []string {
	var lines []string
	cur := ""
	for _, p := range parts {
		switch {
		case cur == "":
			cur = p
		case ansi.StringWidth(cur)+2+ansi.StringWidth(p) <= w:
			cur += "  " + p
		default:
			lines = append(lines, cur)
			cur = "      " + p
		}
	}
	lines = append(lines, cur)
	if len(lines) > 2 {
		lines = []string{lines[0], strings.Join(lines[1:], "  ")}
	}
	return lines
}

// minFit is the least range a live session chart narrows to.
const minFit = 30 * time.Second

// fits reports whether v's chart starts at its first session sample, and
// where: the session history is shorter than the range, and no window or
// pan set the range.
func (m *Model) fits(v *view) (time.Time, bool) {
	if !m.sessionShown(v) || v.follow || v.custom > 0 {
		return time.Time{}, false
	}
	first, _, ok := m.sess.kept(v.target.Key())
	end := m.shownEnd(v)
	if !ok || !first.After(end.Add(-v.span(m))) || !first.Before(end) {
		return time.Time{}, false
	}
	return minTime(first, end.Add(-minFit)), true
}

// shownStart is the start of the range v shows: the zoom range, or the
// first session sample while the session is shorter (fits).
func (m *Model) shownStart(v *view) time.Time {
	if start, ok := m.fits(v); ok {
		return start
	}
	return m.shownEnd(v).Add(-v.span(m))
}

// shownSpan is how long the range v shows is.
func (m *Model) shownSpan(v *view) time.Duration { return m.shownEnd(v).Sub(m.shownStart(v)) }

// rangeNote is the chart title's note on a range narrowed to the data.
func (m *Model) rangeNote(v *view) string {
	if _, ok := m.fits(v); !ok {
		return ""
	}
	return roughDuration(m.shownSpan(v)) + " of " + shortDuration(v.span(m)) + " collected so far"
}

// roughDuration writes a duration in whole minutes, or seconds below one.
func roughDuration(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(int(d/time.Second)) + "s"
	}
	return strconv.Itoa(int(d/time.Minute)) + "m"
}

// sourceLabel says where a chart's line comes from and what it covers:
// Prometheus with its averaging window or peak resolution and its pod
// coverage, else the Metrics API history of this session and why.
func (m *Model) sourceLabel(v *view, wd widget) string {
	if u := v.prom.usage; m.usesProm(v) {
		label := seriesLabel(u, wd) + " · " + coverageLabel(u.Coverage())
		if u.Clipped {
			layout := "15:04"
			if m.now().Sub(u.Range.Start) >= 24*time.Hour {
				layout = "01-02 15:04"
			}
			label += " · created " + u.Range.Start.In(m.loc()).Format(layout)
		}
		if v.prom.pending != "" {
			label += " · reading…"
		}
		return label
	}
	label := "Metrics API, " + m.sessionCoverage(v)
	if t := m.sourceTitle(v); t != "Metrics API session" {
		label += " · " + t
	}
	return label
}

// seriesLabel says what one Prometheus sample of the chart is: the CPU
// rate window, or the memory working set and its peak resolution.
func seriesLabel(u *promhistory.Usage, wd widget) string {
	if wd != widgetMemory {
		return shortDuration(u.RateWindow) + " avg"
	}
	if u.Range.Step > u.PeakResolution {
		return "peak per " + shortDuration(u.Range.Step) + " at " + shortDuration(u.PeakResolution)
	}
	return "working set"
}

// sourceTitle names the source of the charts in the title bar, and why
// it is the Metrics API session history when it is.
func (m *Model) sourceTitle(v *view) string {
	switch {
	case m.src.Usage == nil:
		return "no Prometheus (C-x m)"
	case v.prom.err != nil:
		// promhistory names the backend first; the title bar has it.
		msg := strings.TrimPrefix(firstLine(v.prom.err.Error()), "prometheus "+m.src.Backend+": ")
		return "Metrics API: Prometheus failed: " + msg
	case v.prom.usage == nil && v.prom.pending != "":
		return "reading Prometheus…"
	case v.prom.usage == nil:
		return "Metrics API session"
	}
	return "Prometheus " + m.src.Backend
}

// coverageLabel is how many steps of a Prometheus range had every
// running pod measured.
func coverageLabel(c promhistory.Coverage) string {
	switch {
	case c.Steps == 0:
		return "no steps"
	case !c.Known:
		if c.Empty == c.Steps {
			return "no data"
		}
		return "coverage unknown (no kube-state-metrics phase)"
	case c.Complete():
		return "complete"
	case c.Empty == c.Steps:
		return "no data in range"
	}
	parts := make([]string, 0, 4)
	if c.Partial > 0 {
		parts = append(parts, fmt.Sprintf("partial %d/%d steps", c.Partial, c.Steps))
	}
	if c.Empty > 0 {
		parts = append(parts, fmt.Sprintf("gaps %d/%d steps", c.Empty, c.Steps))
	}
	return strings.Join(parts, ", ")
}

// sessionCoverage counts the session samples in the shown range and
// says how many pods the newest one measured.
func (m *Model) sessionCoverage(v *view) string {
	end := m.shownEnd(v)
	in := m.sess.within(v.target.Key(), m.shownStart(v), end)
	if len(in) == 0 {
		// A range outside what was kept stays where it is; it does not
		// move to the samples there are.
		if first, last, ok := m.sess.kept(v.target.Key()); ok {
			return "nothing recorded in this range (kept " + first.In(m.loc()).Format("15:04:05") + "–" + last.In(m.loc()).Format("15:04:05") + ")"
		}
		return "no samples yet"
	}
	last := in[len(in)-1]
	text := plural(len(in), "sample")
	if last.expected > 0 {
		text += fmt.Sprintf(" · %d of %d pods measured", last.measured, last.expected)
	}
	return text
}

func truncate(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:max(0, w-1)]) + "…"
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	return first
}

// formatCores writes cores with up to three significant digits.
func formatCores(v float64) string {
	switch {
	case v == 0:
		return "0"
	case v < 0.01:
		return strconv.FormatFloat(v, 'f', 3, 64)
	case v < 10:
		return trimZeros(strconv.FormatFloat(v, 'f', 2, 64))
	default:
		return trimZeros(strconv.FormatFloat(v, 'f', 1, 64))
	}
}

// formatBytes writes bytes in binary units, Ki to Ti.
func formatBytes(v float64) string {
	units := []string{"", "Ki", "Mi", "Gi", "Ti"}
	i := 0
	for i < len(units)-1 && math.Abs(v) >= 1024 {
		v /= 1024
		i++
	}
	if v < 10 && i > 0 {
		return trimZeros(strconv.FormatFloat(v, 'f', 1, 64)) + units[i]
	}
	return strconv.FormatFloat(math.Round(v), 'f', 0, 64) + units[i]
}

func trimZeros(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// shortDuration writes 15m, 1h, 3h, 7d, 30s.
func shortDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	case d >= time.Hour && d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d >= time.Minute && d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	default:
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
	}
}
