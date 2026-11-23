package dashboard

import (
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/promhistory"
	"github.com/stokaro/kc/internal/ui/chart"
	"github.com/stokaro/kc/internal/ui/menubar"
)

// rect is where a widget's frame is on the screen; plotX is the screen
// column of the first plot cell of a chart, and of the Events lane.
type rect struct{ x, y, w, h, plotX int }

func (r rect) contains(x, y int) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

const (
	// wideWidth puts the target list left of the charts and Events.
	wideWidth = 120
	// sideWidth puts the two charts side by side below wideWidth.
	sideWidth = 80
	// minChartHeight is a chart's frame: two borders, the legend, two plot
	// rows and the time axis.
	minChartHeight = 6
	// minEventsHeight is the Events frame with its lane and one Event.
	minEventsHeight = 6
	// minListHeight is the target list with a header and two rows.
	minListHeight = 5
	// labelWidth is the value label width a range read assumes.
	labelWidth = 6
)

var (
	useStyle    = plotStyle.Foreground(lipgloss.BrightGreen)
	reqStyle    = plotStyle.Foreground(lipgloss.BrightYellow)
	limStyle    = plotStyle.Foreground(lipgloss.BrightRed)
	allocStyle  = plotStyle.Foreground(lipgloss.BrightBlue)
	pausedStyle = lipgloss.NewStyle().Background(lipgloss.Yellow).Foreground(lipgloss.Black).Bold(true)
	liveStyle   = lipgloss.NewStyle().Background(lipgloss.Black).Foreground(lipgloss.BrightGreen).Bold(true)
	crumbStyle  = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black)
)

// layout places the widgets of v on a w x h screen below the menu bar.
// From wideWidth columns the target list is on the left and the charts
// and Events on the right; below that the charts sit side by side (one
// above the other below sideWidth), with Events and the target list
// under them. When the body is too low, only the focused widget shows and
// Tab pages through them.
func layout(v *view, w, h int) map[widget]rect {
	out := make(map[widget]rect)
	y0, body := 1, h-1
	if body < 3 || w < 20 {
		return out
	}
	full := rect{x: 0, y: y0, w: w, h: body}
	if v.expanded {
		out[v.focus] = full
		return out
	}
	switch {
	case w >= wideWidth:
		lw := listWidth(v, w)
		rw := w - lw
		evH := min(14, max(minEventsHeight+1, body/3))
		ch := (body - evH) / 2
		if ch < minChartHeight {
			break
		}
		out[widgetTargets] = rect{x: 0, y: y0, w: lw, h: body}
		out[widgetCPU] = rect{x: lw, y: y0, w: rw, h: ch}
		out[widgetMemory] = rect{x: lw, y: y0 + ch, w: rw, h: ch}
		out[widgetEvents] = rect{x: lw, y: y0 + 2*ch, w: rw, h: body - 2*ch}
		return out
	case w >= sideWidth:
		listH := min(10, max(minListHeight, body/4))
		evH := min(10, max(minEventsHeight, body/4))
		ch := body - listH - evH
		if ch < minChartHeight {
			break
		}
		half := w / 2
		out[widgetCPU] = rect{x: 0, y: y0, w: half, h: ch}
		out[widgetMemory] = rect{x: half, y: y0, w: w - half, h: ch}
		out[widgetEvents] = rect{x: 0, y: y0 + ch, w: w, h: evH}
		out[widgetTargets] = rect{x: 0, y: y0 + ch + evH, w: w, h: listH}
		return out
	default:
		listH, evH := minListHeight, minEventsHeight
		ch := (body - listH - evH) / 2
		if ch < minChartHeight {
			break
		}
		out[widgetCPU] = rect{x: 0, y: y0, w: w, h: ch}
		out[widgetMemory] = rect{x: 0, y: y0 + ch, w: w, h: ch}
		out[widgetEvents] = rect{x: 0, y: y0 + 2*ch, w: w, h: evH}
		out[widgetTargets] = rect{x: 0, y: y0 + 2*ch + evH, w: w, h: body - 2*ch - evH}
		return out
	}
	out[v.focus] = full
	return out
}

// View renders the dashboard at its size: the menu bar row and the
// widgets. The App draws the F-key bar under it.
func (m *Model) View() string {
	w, h := m.w, m.h
	if w < 20 || h < 6 {
		return fit("Dashboard: the terminal is too small", w)
	}
	v := m.top()
	rects := layout(v, w, h)
	charts := m.placeCharts(v, rects)
	screen := make([]string, h)
	m.drawScreen(v, screen, rects, charts)
	return strings.Join(screen, "\n")
}

// placeCharts builds the charts of rects on one cell grid and records
// where each widget and its plot are, for the mouse.
func (m *Model) placeCharts(v *view, rects map[widget]rect) map[widget]chart.Chart {
	charts := make(map[widget]chart.Chart)
	m.rects = make(map[widget]rect, len(rects))
	cells := 0
	for wd, r := range rects {
		m.rects[wd] = r
		if wd != widgetCPU && wd != widgetMemory {
			continue
		}
		c := m.chartOf(v, wd, r.w-2, r.h-3)
		charts[wd] = c
		// Labels up to labelWidth keep the plot width the reads assume.
		if n := min(c.Cells(), chart.PlotWidth(r.w-2, labelWidth)); cells == 0 || n < cells {
			cells = n
		}
	}
	// One cell grid for the charts and the events lane, so the cursor is
	// the same time everywhere.
	if cells > 0 {
		m.cells = cells
	} else if r, ok := rects[widgetEvents]; ok && m.cells == 0 {
		m.cells = chart.PlotWidth(r.w-2, labelWidth)
	}
	for wd, r := range m.rects {
		if c, ok := charts[wd]; ok {
			c.PlotCells = m.cells
			charts[wd] = c
		}
		// Plots and the lane end at the right border.
		r.plotX = r.x + r.w - 1 - m.cells
		m.rects[wd] = r
	}
	// Under charts side by side, the lane is on the time axis of the left
	// one: it is read from the left.
	if cpu, ok := m.rects[widgetCPU]; ok {
		if ev, ok := m.rects[widgetEvents]; ok && ev.x == cpu.x && ev.w > cpu.w {
			ev.plotX = cpu.plotX
			m.rects[widgetEvents] = ev
		}
	}
	if v.cursor >= m.cells {
		v.cursor = m.cells - 1
	}
	// A legend that needs a second line takes it from the plot.
	for wd, c := range charts {
		if n := len(legendLines(m.legend(v, wd, c), m.rects[wd].w-2)); n > 1 {
			c.Height -= n - 1
			charts[wd] = c
		}
	}
	return charts
}

// drawScreen fills screen with the top bar and the widgets in rects.
func (m *Model) drawScreen(v *view, screen []string, rects map[widget]rect, charts map[widget]chart.Chart) {
	w, h := m.w, len(screen)
	screen[0] = m.topBar(v, w)
	blank := onPanel("", w)
	for i := 1; i < h; i++ {
		screen[i] = blank
	}
	m.itemAt = nil
	for wd := range rects {
		r := m.rects[wd]
		var lines []string
		switch wd {
		case widgetTargets:
			var body []string
			body, m.itemAt = m.targetLines(v, r.w-2, r.h-2)
			lines = frame(listTitle(v), listFooter(v), v.focus == wd, r.w, r.h, body)
		case widgetEvents:
			lines = m.eventsFrame(v, r)
		default:
			lines = m.chartFrame(v, wd, charts[wd], r)
		}
		for i, l := range lines {
			if i >= r.h || r.y+i >= h {
				break
			}
			screen[r.y+i] = place(screen[r.y+i], l, r.x, r.w, w)
		}
	}
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

// chartFrame is a chart in its frame: the title with the unit and the
// range, a legend in words, the plot, and where the line comes from.
func (m *Model) chartFrame(v *view, wd widget, c chart.Chart, r rect) []string {
	title := "CPU, cores"
	if wd == widgetMemory {
		title = "Memory"
	}
	if note := m.rangeNote(v); note != "" {
		title += " · " + note
	}
	legend := legendLines(m.legend(v, wd, c), r.w-2)
	body := slices.Concat(legend, c.Render())
	m.placeImage(wd, c, body[len(legend)-1:])
	return frame(title, m.sourceLabel(v, wd), v.focus == wd, r.w, r.h, body)
}

// topBar is the menu bar row: the menu titles, the breadcrumb of the
// target and the state.
func (m *Model) topBar(v *view, w int) string {
	bar := menubar.RenderTitles(m.Menus(nil), w)
	state := m.stateText(v, w >= wideWidth)
	st := liveStyle
	if v.paused {
		st = pausedStyle
	}
	stateCell := st.Render(" " + state + " ")
	sw := lipgloss.Width(stateCell)
	// The menu titles end where the bar stops finding one.
	menus := m.Menus(nil)
	titles := w
	for titles > 0 && menubar.TitleIndex(menus, titles-1) < 0 {
		titles--
	}
	room := w - sw - titles - 2
	if room < 8 {
		return place(bar, stateCell, w-sw, sw, w)
	}
	crumbs := m.breadcrumb(v)
	text := strings.Join(crumbs, " › ")
	for ansi.StringWidth(text) > room && len(crumbs) > 2 {
		crumbs = append([]string{"…"}, crumbs[2:]...)
		text = strings.Join(crumbs, " › ")
	}
	text = ansi.Truncate(text, room, "…") + " "
	right := crumbStyle.Render(text) + stateCell
	rw := lipgloss.Width(right)
	return place(bar, right, w-rw, rw, w)
}

// breadcrumb is where the view is: the context, the namespace scope, and
// the workload and pod it narrowed to.
func (m *Model) breadcrumb(v *view) []string {
	out := []string{m.src.Context}
	scope := m.scopeTarget()
	out = append(out, scope.Title())
	var path []Target
	for _, x := range m.stack {
		if x.target.Kind != KindScope {
			path = append(path, x.target)
		}
	}
	if len(path) > 0 && path[0].Kind == KindPod {
		if p, ok := m.parent(m.stack[0]); ok && p.Kind == KindWorkload && m.stack[0] == v {
			path = append([]Target{p}, path...)
		}
	}
	for _, t := range path {
		out = append(out, crumb(t))
	}
	return out
}

// crumb names a target in the breadcrumb.
func crumb(t Target) string {
	switch t.Kind {
	case KindNode:
		return "node " + t.Name
	case KindWorkload:
		return workloadKind(t.Workload) + " " + t.Name
	case KindPod:
		return "pod " + t.Name
	}
	return t.Title()
}

// stateText is LIVE or PAUSED with the range, or the window followed.
func (m *Model) stateText(v *view, long bool) string {
	if ws := m.windowState(v); ws != "" {
		return ws
	}
	state := "LIVE " + shortDuration(v.span(m))
	if long {
		state = "LIVE · last " + longDuration(v.span(m))
	}
	if v.paused {
		state = "PAUSED at " + v.end.In(m.loc()).Format("15:04:05") + " · " + shortDuration(v.span(m))
	}
	if !m.lastWin.Off() {
		state += " ≠ window"
	}
	return state
}

// chartOf builds the chart of a CPU or memory widget.
func (m *Model) chartOf(v *view, wd widget, w, h int) chart.Chart {
	end := m.shownEnd(v)
	c := chart.Chart{
		Start: m.shownStart(v), End: end, Width: w, Height: h, Mode: m.mode,
		Style: useStyle, GapStyle: gapStyle, Cursor: -1, Loc: m.src.Loc, Format: formatCores,
	}
	if v.cursor >= 0 {
		c.Cursor = v.cursor
	}
	if wd == widgetMemory {
		c.Format, c.Scale = formatBytes, chart.BinaryTop
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
	c.Refs = m.refsOf(v, wd)
	return c
}

// Reference line order: refsOf returns them so, and the status line reads
// them back by index.
const (
	refCapacity = iota
	refLimits
	refRequests
)

// refsOf are the allocatable, limit and request lines of a widget, each
// as it was at each time: from kube-state-metrics with Prometheus, else
// from the session samples. Today's spec is never drawn over the past.
// A source without that history draws no lines.
func (m *Model) refsOf(v *view, wd widget) []chart.Ref {
	var req, lim, alloc []chart.Point
	var hold time.Duration
	if u := v.prom.usage; m.usesProm(v) {
		res := u.Resources
		if !res.Known() {
			return nil
		}
		h := res.CPU
		if wd == widgetMemory {
			h = res.Memory
		}
		req, lim, alloc = chartPoints(h.Requests), chartPoints(res.FullLimits(h)), chartPoints(h.Capacity)
		hold = u.Range.Step + u.Range.Step/2
	} else {
		req, lim, alloc = m.sess.refPoints(v.target.Key(), wd == widgetMemory)
		hold = sessionHold
	}
	return []chart.Ref{
		refCapacity: {Points: alloc, Hold: hold, Style: allocStyle},
		refLimits:   {Points: lim, Hold: hold, Style: limStyle},
		refRequests: {Points: req, Hold: hold, Style: reqStyle},
	}
}

// usesProm reports whether the charts show a Prometheus read.
func (m *Model) usesProm(v *view) bool {
	return m.src.Usage != nil && v.prom.usage != nil && v.prom.err == nil
}

func chartPoints(ps []promhistory.Point) []chart.Point {
	out := make([]chart.Point, len(ps))
	for i, p := range ps {
		out[i] = chart.Point{At: p.At, V: p.Value}
	}
	return out
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
	// A step stands until the next one; a missing step is a gap.
	return chartPoints(src), u.Range.Step + u.Range.Step/2, true
}

func (m *Model) loc() *time.Location {
	if m.src.Loc != nil {
		return m.src.Loc
	}
	return time.UTC
}
