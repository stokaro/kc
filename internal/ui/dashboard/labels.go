package dashboard

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/stokaro/kc/internal/promhistory"
)

// chartTitle is the first line of a chart: the resource and unit, where
// the line comes from, and how much of the range it covers.
func (m *Model) chartTitle(v *view, wd widget, w int) string {
	name := "CPU cores"
	if wd == widgetMemory {
		name = "Memory"
	}
	mark := "  "
	if v.focus == wd {
		mark = "▸ "
	}
	head := mark + name
	if v.focus == wd {
		head = focusStyle.Render(head)
	}
	return fit(head+" · "+m.sourceLabel(v, wd)+m.legend(v, wd), w)
}

// legend names the lines of a chart, or why the requests and limits have
// no history to draw.
func (m *Model) legend(v *view, wd widget) string {
	legend := " " + useStyle.Render("█use")
	if u := v.prom.usage; m.usesProm(v) && !u.Resources.Known() {
		why := "no kube-state-metrics container series"
		if u.ResourcesErr != "" {
			why = u.ResourcesErr
		}
		return legend + dimStyle.Render(" · no req/lim history: "+why)
	}
	legend += " " + reqStyle.Render("┄req") + " " + limStyle.Render("┄lim")
	if (wd == widgetCPU && v.now.CPU.Capacity > 0) || (wd == widgetMemory && v.now.Memory.Capacity > 0) {
		legend += " " + allocStyle.Render("┄alloc")
	}
	return legend
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
	return "session: " + m.sessionCoverage(v)
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
	in := m.sess.within(v.target.Key(), end.Add(-v.span(m)), end)
	if len(in) == 0 {
		// A range outside what was kept stays where it is; it does not
		// move to the samples there are.
		if first, last, ok := m.sess.kept(v.target.Key()); ok {
			return "nothing recorded in this range; kept " + first.In(m.loc()).Format("15:04:05") + "–" + last.In(m.loc()).Format("15:04:05")
		}
		return "no samples yet"
	}
	last := in[len(in)-1]
	text := fmt.Sprintf("%d samples from %s", len(in), in[0].at.In(m.loc()).Format("15:04:05"))
	if last.expected > 0 && last.measured < last.expected {
		text += fmt.Sprintf(" · partial: %d of %d pods measured", last.measured, last.expected)
	}
	return text
}

// topLines lists the pods that use the most memory, now.
func (m *Model) topLines(v *view, w, h int) []string {
	head := "  Top pods by memory, now (Metrics API)"
	if v.focus == widgetTop {
		head = focusStyle.Render("▸ Top pods by memory, now (Metrics API)") + dimStyle.Render("  Enter: dashboard  o: open")
	}
	lines := []string{fit(head, w)}
	if len(v.now.Top) == 0 {
		note := v.now.Note
		if note == "" {
			note = "no measured pods"
		}
		return append(lines, fit("  "+note, w))
	}
	nameW := max(10, w-24)
	first := 0
	if rows := h - 1; v.sel >= rows {
		first = v.sel - rows + 1
	}
	for i := first; i < len(v.now.Top) && len(lines) < h; i++ {
		c := v.now.Top[i]
		name := c.Namespace + "/" + c.Name
		if v.target.Kind != KindScope || v.target.Namespace != "" {
			name = c.Name
		}
		row := fmt.Sprintf("  %-*s %9s %10s", nameW, truncate(name, nameW), formatCores(c.CPU), formatBytes(c.Memory))
		if v.focus == widgetTop && i == v.sel {
			row = selStyle.Render(fit(row, w))
		}
		lines = append(lines, fit(row, w))
	}
	return lines
}

// keysLine lists the keys, the key bold and its action dim; a paged
// layout says Tab shows the next widget. The draw mode key shows from
// 100 columns; F1 lists it at every width.
func (m *Model) keysLine(v *view, rects map[widget]rect, w int) string {
	keys := [][2]string{{"Tab", "focus"}, {"Enter", "expand"}, {"←→", "cursor"}, {"Space", "pause"}, {"+-", "zoom"}, {"o", "open"}, {"F1", "help"}, {"Esc", "back"}}
	if len(rects) == 1 && !v.expanded {
		keys[0][1] = "next widget"
	}
	if w >= 100 {
		keys = append(keys[:6], append([][2]string{{"l", "logs"}, {"w", "window"}, {"b", m.nextDrawName()}}, keys[6:]...)...)
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = focusStyle.Render(k[0]) + " " + dimStyle.Render(k[1])
	}
	return fit(" "+strings.Join(parts, " "), w)
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
