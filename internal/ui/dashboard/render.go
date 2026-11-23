package dashboard

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/chart"
)

// rect is where a widget is on the screen; plotX is the screen column of
// the first plot cell of a chart.
type rect struct{ x, y, w, h, plotX int }

func (r rect) contains(x, y int) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

const (
	// wideWidth puts the two charts side by side.
	wideWidth = 120
	// minChartHeight is a chart's title, two plot rows and the time axis.
	minChartHeight = 4
	// maxTopRows bounds the consumers shown below the charts.
	maxTopRows = 6
	// labelWidth is the value label width a range read assumes.
	labelWidth = 6
)

var (
	titleStyle  = lipgloss.NewStyle().Reverse(true).Bold(true)
	focusStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	selStyle    = lipgloss.NewStyle().Reverse(true)
	useStyle    = lipgloss.NewStyle().Foreground(lipgloss.Green)
	reqStyle    = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	limStyle    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	allocStyle  = lipgloss.NewStyle().Foreground(lipgloss.Blue)
	pausedStyle = lipgloss.NewStyle().Reverse(true).Bold(true).Foreground(lipgloss.Yellow)
)

// layout places the widgets of v on a w x h screen: title, status line,
// body, keys. The charts sit side by side from wideWidth columns, else
// one above the other; the consumers go below. When the body is too low
// for that, only the focused widget shows and Tab pages through them.
func layout(v *view, w, h int) map[widget]rect {
	out := make(map[widget]rect)
	y0, body := 2, h-3
	if body < 1 || w < 1 {
		return out
	}
	full := rect{x: 0, y: y0, w: w, h: body}
	if v.expanded {
		out[v.focus] = full
		return out
	}
	hasTop := len(v.widgets()) == 3
	topH := 0
	if hasTop {
		topH = min(maxTopRows+1, max(3, len(v.now.Top)+1))
	}
	switch {
	case w >= wideWidth && body-topH >= minChartHeight:
		half := (w - 1) / 2
		out[widgetCPU] = rect{x: 0, y: y0, w: half, h: body - topH}
		out[widgetMemory] = rect{x: half + 1, y: y0, w: w - half - 1, h: body - topH}
	case (body-topH)/2 >= minChartHeight:
		ch := (body - topH) / 2
		out[widgetCPU] = rect{x: 0, y: y0, w: w, h: ch}
		out[widgetMemory] = rect{x: 0, y: y0 + ch, w: w, h: ch}
		topH = body - 2*ch
	default:
		out[v.focus] = full
		return out
	}
	if hasTop {
		out[widgetTop] = rect{x: 0, y: y0 + body - topH, w: w, h: topH}
	}
	return out
}

// View renders the dashboard at its size.
func (m *Model) View() string {
	w, h := m.w, m.h
	if w < 20 || h < 6 {
		return fit("Dashboard: the terminal is too small", w)
	}
	v := m.top()
	rects := layout(v, w, h)
	charts := make(map[widget]chart.Chart)
	m.rects = make(map[widget]rect, len(rects))
	cells := 0
	for wd, r := range rects {
		m.rects[wd] = r
		if wd == widgetTop {
			continue
		}
		c := m.chartOf(v, wd, r.w, r.h-1)
		charts[wd] = c
		if n := c.Cells(); cells == 0 || n < cells {
			cells = n
		}
	}
	// One cell grid for both charts, so the cursor is the same time in
	// each: their value labels differ in width.
	if cells > 0 {
		m.cells = cells
	}
	for wd, c := range charts {
		c.PlotCells = m.cells
		charts[wd] = c
		r := m.rects[wd]
		r.plotX = r.x + r.w - c.Cells()
		m.rects[wd] = r
	}
	if v.cursor >= m.cells {
		v.cursor = m.cells - 1
	}
	screen := make([]string, h)
	screen[0] = m.titleLine(v, w)
	screen[1] = m.statusLine(v, charts, w)
	for i := 2; i < h-1; i++ {
		screen[i] = strings.Repeat(" ", w)
	}
	screen[h-1] = m.keysLine(v, rects, w)
	for wd, r := range rects {
		var lines []string
		if wd == widgetTop {
			lines = m.topLines(v, r.w, r.h)
		} else {
			lines = append([]string{m.chartTitle(v, wd, r.w)}, charts[wd].Render()...)
		}
		for i, l := range lines {
			if i >= r.h || r.y+i >= h-1 {
				break
			}
			screen[r.y+i] = place(screen[r.y+i], fit(l, r.w), r.x, r.w, w)
		}
	}
	return strings.Join(screen, "\n")
}

// place puts cell text s of width sw at column x of line.
func place(line, s string, x, sw, w int) string {
	left := ansi.Truncate(line, x, "")
	right := ansi.TruncateLeft(line, x+sw, "")
	return fit(left+s+right, w)
}

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	if lw := lipgloss.Width(s); lw > w {
		s = ansi.Truncate(s, w, "…")
	} else if lw < w {
		s += strings.Repeat(" ", w-lw)
	}
	return s
}

// chartOf builds the chart of a CPU or memory widget.
func (m *Model) chartOf(v *view, wd widget, w, h int) chart.Chart {
	end := m.shownEnd(v)
	c := chart.Chart{
		Start: end.Add(-v.span(m)), End: end, Width: w, Height: h, Mode: m.mode,
		Style: useStyle, Cursor: -1, Loc: m.src.Loc, Format: formatCores,
	}
	if v.cursor >= 0 {
		c.Cursor = v.cursor
	}
	amount := v.now.CPU
	if wd == widgetMemory {
		c.Format, c.Scale, amount = formatBytes, chart.BinaryTop, v.now.Memory
	}
	if pts, hold, ok := m.promPoints(v, wd); ok {
		c.Points, c.Hold = pts, hold
	} else {
		cpu, mem := m.sess.points(v.target.Key())
		c.Points, c.Hold = cpu, sessionHold
		if wd == widgetMemory {
			c.Points = mem
		}
	}
	if amount.Capacity > 0 {
		c.Refs = append(c.Refs, chart.Ref{V: amount.Capacity, Style: allocStyle})
	}
	if amount.Limits > 0 && !amount.LimitsPartial {
		c.Refs = append(c.Refs, chart.Ref{V: amount.Limits, Style: limStyle})
	}
	if amount.Requests > 0 {
		c.Refs = append(c.Refs, chart.Ref{V: amount.Requests, Style: reqStyle})
	}
	return c
}

// promPoints are the Prometheus points of a widget, when there is a read
// result for the range shown.
func (m *Model) promPoints(v *view, wd widget) ([]chart.Point, time.Duration, bool) {
	u := v.prom.usage
	if m.src.Usage == nil || u == nil || v.prom.err != nil {
		return nil, 0, false
	}
	src := u.CPU
	if wd == widgetMemory {
		src = u.MemoryPeak
		if len(src) == 0 {
			src = u.Memory
		}
	}
	pts := make([]chart.Point, len(src))
	for i, p := range src {
		pts[i] = chart.Point{At: p.At, V: p.Value}
	}
	// A step stands until the next one; a missing step is a gap.
	return pts, u.Range.Step + u.Range.Step/2, true
}

// titleLine names the dashboard, its target and context, and the state.
func (m *Model) titleLine(v *view, w int) string {
	title := v.target.Title()
	if len(m.stack) > 1 {
		title = m.stack[0].target.Title() + " > " + title
	}
	parts := []string{" " + title, m.src.Context, m.sourceTitle(v)}
	if w >= 100 {
		parts[0] = " Dashboard │ " + title
	}
	state := "LIVE " + shortDuration(v.span(m))
	if v.paused {
		state = "PAUSED at " + v.end.In(m.loc()).Format("15:04:05") + " " + shortDuration(v.span(m))
	}
	left := titleStyle.Render(fit(strings.Join(parts, " │ ")+" │ ", max(0, w-lipgloss.Width(state)-2)))
	st := titleStyle
	if v.paused {
		st = pausedStyle
	}
	return fit(left+st.Render(" "+state+" "), w)
}

func (m *Model) loc() *time.Location {
	if m.src.Loc != nil {
		return m.src.Loc
	}
	return time.UTC
}

// statusLine is the values under the cursor, else the current
// measurement with requests and limits.
func (m *Model) statusLine(v *view, charts map[widget]chart.Chart, w int) string {
	if v.cursor >= 0 {
		var parts []string
		var span string
		for _, wd := range []widget{widgetCPU, widgetMemory} {
			c, ok := charts[wd]
			if !ok {
				c = m.chartOf(v, wd, max(w, 40), minChartHeight)
				c.PlotCells = m.cells
			}
			cv := c.Cell(v.cursor)
			if span == "" && !cv.From.IsZero() {
				span = cv.From.In(m.loc()).Format("15:04:05") + "–" + cv.To.In(m.loc()).Format("15:04:05")
			}
			name, text := "CPU", "no data"
			if wd == widgetMemory {
				name = "Memory"
			}
			if cv.OK {
				text = c.Format(cv.V)
			}
			parts = append(parts, name+" "+text)
		}
		return fit(focusStyle.Render("▏"+span)+"  "+strings.Join(parts, "  ·  ")+dimStyle.Render("  (max in the cell)"), w)
	}
	a := v.now
	switch {
	case a.Loading:
		return fit(dimStyle.Render("Loading the cluster's objects…"), w)
	case a.Err != "":
		return fit("Metrics API: "+a.Err, w)
	case !a.Found && a.Note == "":
		return fit(v.target.Title()+" is not in the lists kc reads; it may be gone", w)
	}
	now := "now"
	if v.paused {
		now = "at pause"
	}
	line := fmt.Sprintf("%s CPU %s · Memory %s", now, amountText(a.CPU, formatCores), amountText(a.Memory, formatBytes))
	if a.Note != "" {
		line = now + " " + a.Note + " · req CPU " + formatCores(a.CPU.Requests) + ", Memory " + formatBytes(a.Memory.Requests)
	} else if a.Running > 0 {
		line += fmt.Sprintf(" · %d/%d pods measured", a.Measured, a.Running)
	}
	return fit(line, w)
}

// amountText is "U req R lim L" of one resource; a limit sum with a pod
// without a limit says so instead of a number.
func amountText(a models.ResourceAmount, format func(float64) string) string {
	use := "unmeasured"
	if a.UsedKnown {
		use = format(a.Used)
	}
	lim := format(a.Limits)
	switch {
	case a.LimitsPartial:
		lim = "partial"
	case a.Limits == 0:
		lim = "none"
	}
	return fmt.Sprintf("%s req %s lim %s", use, format(a.Requests), lim)
}
