// Package chart draws time series as text: braille dots or block
// eighths, one or two samples per cell, with reference lines and a cursor
// column. It knows nothing about Kubernetes or where the points came from.
package chart

import (
	"math"
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

// Ref is a horizontal line at V, such as a request or a limit.
type Ref struct {
	V     float64
	Style lipgloss.Style
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
}

const (
	gapAxis  = '·'
	axisLine = '─'
	refLine  = '┄'
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
	l.top = scale(c.maxValue())
	if l.top <= 0 || math.IsNaN(l.top) || math.IsInf(l.top, 0) {
		l.top = 1
	}
	l.label = 0
	for row := range l.rows {
		l.label = max(l.label, len(c.labelText(l, row)))
	}
	l.plot = PlotWidth(c.Width, l.label)
	l.cols = c.pool(Samples(l.plot, c.Mode))
	return l
}

func (c Chart) maxValue() float64 {
	top := 0.0
	for _, p := range c.Points {
		if p.V > top && !p.At.Before(c.Start) && !p.At.After(c.End) {
			top = p.V
		}
	}
	for _, r := range c.Refs {
		top = max(top, r.V)
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
	span := c.End.Sub(c.Start)
	cv := CellValue{
		From: c.Start.Add(time.Duration(float64(span) * float64(i) / float64(l.plot))),
		To:   c.Start.Add(time.Duration(float64(span) * float64(i+1) / float64(l.plot))),
	}
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
			if x == c.Cursor {
				st = cursor
			}
			b.WriteString(st.Render(string(r)))
		}
		lines = append(lines, b.String())
	}
	return append(lines, c.timeAxis(l))
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
		if refRow(l, ref.V) != row {
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

// timeAxis is the last line: a rule with · under gap cells, the start time
// at the left and the end time at the right.
func (c Chart) timeAxis(l layout) string {
	cells := make([]rune, l.plot)
	per := len(l.cols) / l.plot
	for x := range cells {
		cells[x] = gapAxis
		for _, col := range l.cols[x*per : (x+1)*per] {
			if col.ok {
				cells[x] = axisLine
			}
		}
	}
	layoutFmt := "15:04"
	if c.End.Sub(c.Start) >= 24*time.Hour {
		layoutFmt = "01-02 15:04"
	}
	loc := c.Loc
	if loc == nil {
		loc = time.UTC
	}
	from, to := c.Start.In(loc).Format(layoutFmt), c.End.In(loc).Format(layoutFmt)
	if len(from)+len(to)+2 <= l.plot {
		copy(cells, []rune(from))
		copy(cells[l.plot-len(to):], []rune(to))
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
