// Package chart draws time series as text: braille dots or block
// eighths, one or two samples per cell, with reference lines and a cursor
// column. It knows nothing about Kubernetes or where the points came from.
package chart

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// Point is one sample.
type Point struct {
	At time.Time
	V  float64
}

// Mode is how the plot draws values.
type Mode int

const (
	// Braille draws two samples per cell with four dots of height each.
	Braille Mode = iota
	// Blocks draws one sample per cell with eight steps of height. It is
	// the fallback for fonts without braille.
	Blocks
)

// Ref is a reference line, such as a request or a limit: at V, or, with
// Points, at the value it had at each time.
type Ref struct {
	V float64
	// Points make the line follow a history: a plot cell shows the newest
	// point before its end (at or before it for the last cell), while that
	// point is within Hold of the cell's start. A cell without one has no
	// line, so a missing sample never reads as an unchanged value.
	Points []Point
	Hold   time.Duration
	Style  lipgloss.Style
}

// at is the value of r in the cell [from, to); last closes the cell.
func (r Ref) at(from, to time.Time, last bool) (float64, bool) {
	if len(r.Points) == 0 {
		return r.V, r.V > 0
	}
	for _, p := range slices.Backward(r.Points) {
		if p.At.After(to) || (!last && p.At.Equal(to)) {
			continue
		}
		if from.Sub(p.At) > r.Hold {
			return 0, false
		}
		return p.V, true
	}
	return 0, false
}

// Chart is one time series over [Start, End] with its scale and axes.
type Chart struct {
	Start, End time.Time
	Points     []Point
	// Hold is how long a sample stands for the columns after it that have
	// no sample of their own. A column further from the last sample is a
	// gap. Zero holds nothing: every column without a sample is a gap.
	Hold time.Duration
	Refs []Ref
	// Width and Height are the whole chart: the value labels on the left,
	// the plot, and the time axis as the last line.
	Width, Height int
	Mode          Mode
	// Format writes a value of the axis; nil uses %g.
	Format func(float64) string
	// Scale rounds the largest value up to the top of the axis; nil
	// rounds to 1, 2, 2.5 or 5 times a power of ten (NiceTop).
	Scale func(float64) float64
	Style lipgloss.Style
	// Cursor is the plot cell drawn reversed; -1 draws none.
	Cursor int
	// Loc is the time zone of the axis labels; nil is UTC.
	Loc *time.Location
	// PlotCells fixes the plot width when it fits, widening the labels:
	// charts that share a cursor need the same cells. Zero fits the plot
	// to the labels.
	PlotCells int
	// GapStyle draws plot columns without a sample; a run of them wide
	// enough says "no data". Its zero value leaves them blank.
	GapStyle lipgloss.Style
}

// RefFit is how far above the usage a reference line may be and still
// set the scale. A line further above is off the scale: the plot draws it
// as arrows on its top row, so a small usage is not flattened by a large
// request.
const RefFit = 3

// usageHeadroom is how much room the scale leaves above the usage.
const usageHeadroom = 1.1

const (
	axisLine = '─'
	refLine  = '┄'
	refAbove = '↑'
	gapLabel = "no data"
)

// PlotWidth is how many cells the plot of a chart width wide has, after
// the value labels; label is the widest value label.
func PlotWidth(width, label int) int { return max(0, width-label-1) }

// Samples is how many samples a plot of cells cells shows in mode m: the
// resolution to ask a backend for.
func Samples(cells int, m Mode) int {
	if m == Braille {
		return 2 * cells
	}
	return cells
}

// column is one sample column after pooling: the largest value in it.
type column struct {
	v  float64
	ok bool
}

// layout is a chart cut into cells.
type layout struct {
	label, plot, rows int
	cols              []column
	top               float64
	// refRows is the plot row of each reference line per plot cell, -1
	// where it is not drawn; refOff marks the cells where it is above the
	// scale.
	refRows [][]int
	refOff  [][]bool
}

func (c Chart) format(v float64) string {
	if c.Format != nil {
		return c.Format(v)
	}
	return trimFloat(v)
}

func (c Chart) layout() layout {
	l := layout{rows: c.Height - 1}
	// Value labels depend on the scale, which depends on the data alone.
	scale := c.Scale
	if scale == nil {
		scale = NiceTop
	}
	l.top = scale(c.scaleValue())
	if l.top <= 0 || math.IsNaN(l.top) || math.IsInf(l.top, 0) {
		l.top = 1
	}
	l.label = 0
	for row := range l.rows {
		l.label = max(l.label, len(c.labelText(l, row)))
	}
	l.plot = PlotWidth(c.Width, l.label)
	if c.PlotCells > 0 && c.PlotCells < l.plot {
		l.plot = c.PlotCells
		l.label = c.Width - 1 - l.plot
	}
	l.cols = c.pool(Samples(l.plot, c.Mode))
	l.refRows = make([][]int, len(c.Refs))
	l.refOff = make([][]bool, len(c.Refs))
	for i, ref := range c.Refs {
		rows := make([]int, max(0, l.plot))
		off := make([]bool, max(0, l.plot))
		for x := range rows {
			rows[x] = -1
			from, to := c.cellSpan(l, x)
			if v, ok := ref.at(from, to, x == l.plot-1); ok {
				rows[x] = refRow(l, v)
				off[x] = v > l.top*(1+1e-9)
			}
		}
		l.refRows[i], l.refOff[i] = rows, off
	}
	return l
}

// cellSpan is the time span of plot cell x.
func (c Chart) cellSpan(l layout, x int) (from, to time.Time) {
	span := c.End.Sub(c.Start)
	return c.Start.Add(time.Duration(float64(span) * float64(x) / float64(l.plot))),
		c.Start.Add(time.Duration(float64(span) * float64(x+1) / float64(l.plot)))
}

// RefAt is the value of reference line ref in plot cell x.
func (c Chart) RefAt(ref, x int) (float64, bool) {
	l := c.layout()
	if ref < 0 || ref >= len(c.Refs) || x < 0 || x >= l.plot {
		return 0, false
	}
	from, to := c.cellSpan(l, x)
	return c.Refs[ref].at(from, to, x == l.plot-1)
}

// OffScale reports whether reference line ref is above the scale
// somewhere in the range (see RefFit).
func (c Chart) OffScale(ref int) bool {
	if ref < 0 || ref >= len(c.Refs) {
		return false
	}
	return slices.Contains(c.layout().refOff[ref], true)
}

// OffScaleAt reports whether reference line ref is above the scale in
// cell x.
func (c Chart) OffScaleAt(ref, x int) bool {
	if ref < 0 || ref >= len(c.Refs) {
		return false
	}
	off := c.layout().refOff[ref]
	return x >= 0 && x < len(off) && off[x]
}

// usageMax is the largest sample in the range.
func (c Chart) usageMax() float64 {
	top := 0.0
	for _, p := range c.Points {
		if p.V > top && !p.At.Before(c.Start) && !p.At.After(c.End) {
			top = p.V
		}
	}
	return top
}

// refMax is the largest value of r in the range.
func (c Chart) refMax(r Ref) float64 {
	top := r.V
	for _, p := range r.Points {
		if !p.At.After(c.End) && c.Start.Sub(p.At) <= r.Hold {
			top = max(top, p.V)
		}
	}
	return top
}

// maxValue is the largest value of the samples and the reference lines.
func (c Chart) maxValue() float64 {
	top := c.usageMax()
	for _, r := range c.Refs {
		top = max(top, c.refMax(r))
	}
	return top
}

// scaleValue is what the scale fits: the usage, and the reference lines
// up to RefFit times above it. Without usage, everything.
func (c Chart) scaleValue() float64 {
	use := c.usageMax()
	if use <= 0 {
		return c.maxValue()
	}
	// Headroom keeps a steady usage off the top border.
	top := use * usageHeadroom
	for _, r := range c.Refs {
		if v := c.refMax(r); v <= RefFit*use {
			top = max(top, v)
		}
	}
	return top
}

// pool spreads the points over n columns of equal time, each keeping its
// largest value, and holds a sample over the following columns up to Hold.
func (c Chart) pool(n int) []column {
	cols := make([]column, n)
	span := c.End.Sub(c.Start)
	if n == 0 || span <= 0 {
		return cols
	}
	idx := func(at time.Time) int {
		return min(n-1, int(float64(at.Sub(c.Start))/float64(span)*float64(n)))
	}
	lastAt := make([]time.Time, n) // the newest sample in each column
	for _, p := range c.Points {
		if p.At.Before(c.Start) || p.At.After(c.End) || math.IsNaN(p.V) || math.IsInf(p.V, 0) {
			continue
		}
		i := idx(p.At)
		if !cols[i].ok || p.V > cols[i].v {
			cols[i].v = p.V
		}
		cols[i].ok = true
		if p.At.After(lastAt[i]) {
			lastAt[i] = p.At
		}
	}
	if c.Hold > 0 {
		c.hold(cols, lastAt)
	}
	return cols
}

// hold lets a sample stand for later empty columns while they start
// within Hold of it.
func (c Chart) hold(cols []column, lastAt []time.Time) {
	n, span := len(cols), c.End.Sub(c.Start)
	// A sample stands for later empty columns while they start within
	// Hold of it; the first column may continue a sample before Start.
	var last time.Time
	var lastV float64
	for _, p := range c.Points {
		if p.At.Before(c.Start) && p.At.After(last) && !math.IsNaN(p.V) && !math.IsInf(p.V, 0) {
			last, lastV = p.At, p.V
		}
	}
	for i := range cols {
		colStart := c.Start.Add(time.Duration(float64(span) * float64(i) / float64(n)))
		if cols[i].ok {
			last, lastV = lastAt[i], cols[i].v
			continue
		}
		if !last.IsZero() && colStart.Sub(last) < c.Hold {
			cols[i] = column{v: lastV, ok: true}
		}
	}
}

// CellValue is what one plot cell covers and shows.
type CellValue struct {
	From, To time.Time
	// V is the largest value drawn in the cell; OK is false for a gap.
	V  float64
	OK bool
}

// Cell reports the time span of plot cell i and the largest value drawn in
// it. Outside the plot it is the zero CellValue.
func (c Chart) Cell(i int) CellValue {
	l := c.layout()
	if i < 0 || i >= l.plot {
		return CellValue{}
	}
	per := len(l.cols) / l.plot
	cv := CellValue{}
	cv.From, cv.To = c.cellSpan(l, i)
	for _, col := range l.cols[i*per : (i+1)*per] {
		if col.ok && (!cv.OK || col.v > cv.V) {
			cv.V, cv.OK = col.v, true
		}
	}
	return cv
}

// Cells is the plot width of the chart in cells.
func (c Chart) Cells() int { return c.layout().plot }

// Render returns Height lines of Width cells.
func (c Chart) Render() []string {
	if c.Height < 2 || c.Width < 1 {
		return nil
	}
	l := c.layout()
	if l.plot < 2 || l.rows < 1 {
		out := make([]string, c.Height)
		for i := range out {
			out[i] = strings.Repeat(" ", c.Width)
		}
		return out
	}
	lines := make([]string, 0, c.Height)
	cursor := lipgloss.NewStyle().Reverse(true)
	gaps := c.gapCells(l)
	labelRow := l.rows / 2
	for row := range l.rows {
		var b strings.Builder
		b.WriteString(c.axisLabel(l, row))
		b.WriteRune('│')
		for x := range l.plot {
			r, ref := c.cellRune(l, row, x)
			st := c.Style
			if ref != nil {
				st = ref.Style
			}
			if gaps[x] != 0 {
				st = c.GapStyle
				if row == labelRow && gaps[x] > 0 {
					r = gaps[x]
				}
			}
			if x == c.Cursor {
				st = cursor
			}
			b.WriteString(st.Render(string(r)))
		}
		lines = append(lines, b.String())
	}
	return append(lines, c.timeAxis(l))
}

// gapCells marks the plot cells without a sample: -1 for a plain gap
// cell, a rune of "no data" where a run is wide enough to say so.
func (c Chart) gapCells(l layout) []rune {
	out := make([]rune, l.plot)
	per := len(l.cols) / l.plot
	for x := range out {
		out[x] = -1
		for _, col := range l.cols[x*per : (x+1)*per] {
			if col.ok {
				out[x] = 0
			}
		}
	}
	label := []rune(gapLabel)
	for x := 0; x < len(out); {
		if out[x] == 0 {
			x++
			continue
		}
		end := x
		for end < len(out) && out[end] != 0 {
			end++
		}
		if end-x >= len(label)+2 {
			copy(out[x+(end-x-len(label))/2:], label)
		}
		x = end
	}
	return out
}

// axisLabel is the value label of a plot row: the top, the middle and
// zero, right-aligned.
func (c Chart) axisLabel(l layout, row int) string {
	text := c.labelText(l, row)
	return strings.Repeat(" ", max(0, l.label-len(text))) + text
}

// labelText is the value label of a plot row, empty for most rows: the
// top, zero, and half the top on the row that holds it.
func (c Chart) labelText(l layout, row int) string {
	if row != l.rows-1 && c.maxValue() <= 0 {
		// Nothing to scale: a made-up top would read as data.
		return ""
	}
	switch {
	case row == 0:
		return c.format(l.top)
	case row == l.rows-1:
		return c.format(0)
	case l.rows >= 5 && row == refRow(l, l.top/2):
		return c.format(l.top / 2)
	}
	return ""
}

// cellRune draws plot cell (row, x) and returns the reference line it
// carries, if any. A reference shows as a dashed line where the plot is
// empty, and colors the glyph where the plot reaches it.
func (c Chart) cellRune(l layout, row, x int) (rune, *Ref) {
	r := blockCell(l, row, x)
	if c.Mode == Braille {
		r = brailleCell(l, row, x)
	}
	for i := range c.Refs {
		ref := &c.Refs[i]
		if l.refRows[i][x] != row {
			continue
		}
		if l.refOff[i][x] {
			if (r == ' ' || r == brailleBlank) && x == l.plot-1 {
				return refAbove, ref
			}
			continue
		}
		if r == ' ' || r == brailleBlank {
			return refLine, ref
		}
		return r, ref
	}
	return r, nil
}

// refRow is the plot row a value falls in.
func refRow(l layout, v float64) int {
	if v <= 0 || l.top <= 0 {
		return -1
	}
	return min(l.rows-1, max(0, l.rows-1-int(v/l.top*float64(l.rows)-1e-9)))
}

// height is the filled units of a column out of units, at least one for a
// sample so a zero shows apart from a gap.
func height(col column, top float64, units int) int {
	if !col.ok {
		return 0
	}
	return max(1, min(units, int(math.Round(col.v/top*float64(units)))))
}

const brailleBlank = '⠀'

// brailleDots are the bits of the left and right dot columns, top to
// bottom.
var brailleDots = [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

func brailleCell(l layout, row, x int) rune {
	units := l.rows * 4
	bits := rune(0)
	for side := range 2 {
		h := height(l.cols[2*x+side], l.top, units)
		// Dots of this cell row, from its top: unit index counted from
		// the bottom of the plot.
		for dot := range 4 {
			unit := (l.rows-1-row)*4 + (3 - dot)
			if unit < h {
				bits |= brailleDots[side][dot]
			}
		}
	}
	if bits == 0 {
		return ' '
	}
	return brailleBlank + bits
}

var eighths = []rune(" ▁▂▃▄▅▆▇█")

func blockCell(l layout, row, x int) rune {
	h := height(l.cols[x], l.top, l.rows*8)
	fill := min(8, max(0, h-(l.rows-1-row)*8))
	return eighths[fill]
}

// timeAxis is the last line: a rule with the start time at the left, the
// middle time in the middle and the end time at the right.
func (c Chart) timeAxis(l layout) string {
	cells := make([]rune, l.plot)
	for x := range cells {
		cells[x] = axisLine
	}
	layoutFmt := "15:04"
	if c.End.Sub(c.Start) >= 24*time.Hour {
		layoutFmt = "01-02 15:04"
	}
	loc := c.Loc
	if loc == nil {
		loc = time.UTC
	}
	span := c.End.Sub(c.Start)
	if span < 10*time.Minute {
		layoutFmt = "15:04:05"
	}
	from, to := c.Start.In(loc).Format(layoutFmt), c.End.In(loc).Format(layoutFmt)
	mid := c.Start.Add(span / 2).In(loc).Format(layoutFmt)
	if len(from)+len(to)+2 <= l.plot {
		copy(cells, []rune(from))
		copy(cells[l.plot-len(to):], []rune(to))
	}
	if at := (l.plot - len(mid)) / 2; len(from)+len(mid)+len(to)+4 <= l.plot {
		copy(cells[at:], []rune(mid))
	}
	return strings.Repeat(" ", l.label) + "└" + string(cells)
}

// NiceTop rounds v up to 1, 2, 2.5 or 5 times a power of ten, so the
// labels read well; zero and below scale to 1.
func NiceTop(v float64) float64 {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*exp >= v*(1-1e-12) {
			return m * exp
		}
	}
	return 10 * exp
}

func trimFloat(v float64) string {
	s := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 3, 64), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// BinaryTop is NiceTop in binary units (Ki, Mi, Gi, ...), for bytes: 1Gi
// stays 1Gi, 300Mi rounds to 500Mi.
func BinaryTop(v float64) float64 {
	unit := 1.0
	for v/unit >= 1024 {
		unit *= 1024
	}
	return NiceTop(v/unit) * unit
}
