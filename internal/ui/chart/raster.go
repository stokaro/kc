package chart

import (
	"encoding/binary"
	"hash/fnv"
	"image"
	"image/color"
	"math"
	"slices"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The raster draws in the colors of a Grafana time series panel in the
// dark theme.
var (
	// Background is the plot color of a raster.
	Background = color.RGBA{0x18, 0x1b, 0x1f, 0xff}
	// gridColor is Grafana's grid, white at 9% over the background.
	gridColor = blend(Background, color.RGBA{0xf0, 0xfa, 0xff, 0xff}, 0.09)
)

// Grafana's classic palette: the threshold colors of a bar gauge, and the
// lines of a time series panel.
var (
	Green  = color.RGBA{0x73, 0xbf, 0x69, 0xff}
	Yellow = color.RGBA{0xfa, 0xde, 0x2a, 0xff}
	Orange = color.RGBA{0xff, 0x98, 0x30, 0xff}
	Red    = color.RGBA{0xf2, 0x49, 0x5c, 0xff}
)

// palette maps the basic terminal colors of the chart styles to
// Grafana's classic palette.
var palette = map[ansi.BasicColor]color.RGBA{
	ansi.Green:   Green,
	ansi.Yellow:  Yellow,
	ansi.Blue:    {0x57, 0x94, 0xf2, 0xff},
	ansi.Red:     Red,
	ansi.Magenta: {0xb8, 0x77, 0xd9, 0xff},
	ansi.Cyan:    {0x8a, 0xb8, 0xff, 0xff},
}

const (
	// fillOpacity is the opacity of the area under the usage at the top
	// of the scale; it fades to nothing at zero (Grafana's opacity
	// gradient).
	fillOpacity = 0.5
	// bandOpacity shades the plot above a reference line with Band.
	bandOpacity = 0.06
	// cssCell is the cell height, in pixels, of a terminal at a device
	// pixel ratio of 1: line widths are in those pixels.
	cssCell = 18.0
	// lineWidth, refWidth and gridWidth are in pixels at that ratio.
	lineWidth   = 1.5
	seriesWidth = 1.0
	refWidth    = 1.0
	// pointSpacing is the spacing of samples, at that ratio, from which
	// each sample is drawn as a point too.
	pointSpacing = 40.0
	pointRadius  = 2.5
	// fewPoints is the most samples of a line drawn as points too.
	fewPoints = 3
	dashLen   = 6.0
	dashGap   = 4.0
)

// PlotSize is the plot of the chart in cells: the columns after the
// value labels and the rows above the time axis. Render draws the plot at
// columns [Width-cols, Width) of lines [0, rows).
func (c Chart) PlotSize() (cols, rows int) {
	if c.Height < 2 || c.Width < 1 {
		return 0, 0
	}
	l := c.layout()
	if l.plot < 2 || l.rows < 1 {
		return 0, 0
	}
	return l.plot, l.rows
}

// geom maps values and times to pixels of a raster.
type geom struct {
	w, h        int
	cellW       int
	ratio       float64 // device pixels per pixel at ratio 1
	top         float64
	yTop, yZero float64
	start       time.Time
	span        time.Duration
}

func (c Chart) geom(l layout, cellW, cellH int) geom {
	g := geom{
		w: l.plot * cellW, h: l.rows * cellH, cellW: cellW,
		ratio: max(1, float64(cellH)/cssCell), top: l.top,
		start: c.Start, span: c.End.Sub(c.Start),
	}
	// The grid lines go through the centers of the label rows: zero on
	// the last, the top on its row (see Centered).
	g.yZero = float64(g.h) - float64(cellH)/2
	g.yTop = (float64(l.topRow) + 0.5) * float64(cellH)
	if l.rows < 2 {
		g.yTop, g.yZero = 2*g.ratio, float64(g.h)-2*g.ratio
	}
	return g
}

func (g geom) y(v float64) float64 { return g.yZero - v/g.top*(g.yZero-g.yTop) }

func (g geom) x(t time.Time) float64 {
	return float64(t.Sub(g.start)) / float64(g.span) * float64(g.w)
}

// Raster draws the plot of the chart, PlotSize cells at cellW by cellH
// pixels each, as a Grafana time series panel: grid lines through the
// value labels and the middle time, the usage as an antialiased line
// through the samples with an area under it that fades out towards zero,
// and the reference lines dashed. Samples further apart than Hold are
// not joined, so a gap shows the empty grid; a sample without a
// neighbor is a point. The scale and the reference line values are those
// of Render. The cursor is not drawn: it is a text cell over the image
// (TextCells), so moving it changes no picture. It is nil when the chart
// has no plot.
func (c Chart) Raster(cellW, cellH int) *image.RGBA {
	if cols, _ := c.PlotSize(); cols == 0 || cellW < 1 || cellH < 1 {
		return nil
	}
	l := c.layout()
	g := c.geom(l, cellW, cellH)
	p := newPainter(g.w, g.h)
	p.fill(Background)
	c.drawGrid(p, l, g)
	use := rasterColor(c.Style)
	runs := c.runs(g)
	for _, run := range runs {
		fillUnder(p, g, run, use)
	}
	refs := c.refRuns(l, g)
	for i, ref := range refs {
		if c.Refs[i].Band {
			ref.shade(p)
		}
	}
	for _, ref := range slices.Backward(refs) {
		ref.draw(p, g)
	}
	for _, sr := range c.Series {
		other := c
		other.Points = sr.Points
		strokeRuns(p, g, other.runs(g), rasterColor(sr.Style), seriesWidth)
	}
	strokeRuns(p, g, runs, use, lineWidth)
	return p.img
}

func (c Chart) drawGrid(p *painter, l layout, g geom) {
	width := math.Max(1, math.Round(g.ratio))
	line := func(y float64) {
		y = snap(y, width)
		p.rect(0, y-width/2, float64(g.w), y+width/2, gridColor, 1)
	}
	for _, t := range l.ticks {
		line(g.y(t.v))
	}
	vline := func(f float64) {
		x := snap(f*float64(g.w), width)
		p.rect(x-width/2, 0, x+width/2, float64(g.h), gridColor, 1)
	}
	if ticks := c.timeTicks(l); len(ticks) > 0 {
		for _, t := range ticks {
			vline(t.f)
		}
	} else if showsMidTime(l, len(c.Start.Format(c.timeFormat()))) {
		vline(0.5)
	}
}

// snap moves a line center so a line width pixels wide covers whole
// pixels.
func snap(v, width float64) float64 {
	if int(width)%2 == 1 {
		return math.Floor(v) + 0.5
	}
	return math.Round(v)
}

// vertex is a sample in pixels.
type vertex struct{ x, y float64 }

// runs are the samples as pixels, cut where two are further apart than
// Hold. With more samples than half the pixel columns, they are pooled
// per column first, as Render pools them per cell.
func (c Chart) runs(g geom) [][]vertex {
	in, before, after := c.inRange()
	if len(in) > g.w/2 {
		return c.pooledRuns(g)
	}
	slices.SortStableFunc(in, func(a, b Point) int { return a.At.Compare(b.At) })
	// The samples just outside the range continue a line into it.
	if before != nil && len(in) > 0 && in[0].At.Sub(before.At) <= c.Hold {
		in = slices.Insert(in, 0, *before)
	}
	if after != nil && len(in) > 0 && after.At.Sub(in[len(in)-1].At) <= c.Hold {
		in = append(in, *after)
	}
	var out [][]vertex
	var run []vertex
	for i, pt := range in {
		if i > 0 && pt.At.Sub(in[i-1].At) > c.Hold {
			out, run = append(out, run), nil
		}
		run = append(run, vertex{g.x(pt.At), g.y(pt.V)})
	}
	if len(run) > 0 {
		out = append(out, run)
	}
	return out
}

// inRange are the finite samples in the range, and the newest before it
// and the oldest after it.
func (c Chart) inRange() (in []Point, before, after *Point) {
	for i := range c.Points {
		pt := &c.Points[i]
		if math.IsNaN(pt.V) || math.IsInf(pt.V, 0) {
			continue
		}
		switch {
		case pt.At.Before(c.Start):
			if before == nil || pt.At.After(before.At) {
				before = pt
			}
		case pt.At.After(c.End):
			if after == nil || pt.At.Before(after.At) {
				after = pt
			}
		default:
			in = append(in, *pt)
		}
	}
	return in, before, after
}

func (c Chart) pooledRuns(g geom) [][]vertex {
	var out [][]vertex
	var run []vertex
	for x, col := range c.pool(g.w) {
		if !col.ok {
			if len(run) > 0 {
				out, run = append(out, run), nil
			}
			continue
		}
		run = append(run, vertex{float64(x) + 0.5, g.y(col.v)})
	}
	if len(run) > 0 {
		out = append(out, run)
	}
	return out
}

// fillUnder fills the area between a run and zero, more opaque towards
// the top of the scale.
func fillUnder(p *painter, g geom, run []vertex, col color.RGBA) {
	if len(run) < 2 {
		return
	}
	from := max(0, int(math.Floor(run[0].x)))
	to := min(g.w, int(math.Ceil(run[len(run)-1].x)))
	span := g.yZero - g.yTop
	seg := 1
	for x := from; x < to; x++ {
		xc := float64(x) + 0.5
		if xc < run[0].x || xc > run[len(run)-1].x {
			continue
		}
		for seg < len(run)-1 && run[seg].x < xc {
			seg++
		}
		a, b := run[seg-1], run[seg]
		y := a.y
		if b.x > a.x {
			y = a.y + (b.y-a.y)*(xc-a.x)/(b.x-a.x)
		}
		for py := max(0, int(math.Floor(y))); float64(py) < g.yZero && py < g.h; py++ {
			cover := math.Min(float64(py+1), g.yZero) - math.Max(float64(py), y)
			if cover <= 0 {
				continue
			}
			f := math.Min(1, math.Max(0, (g.yZero-float64(py)-0.5)/span))
			p.blendAt(x, py, col, cover*fillOpacity*f)
		}
	}
}

// strokeRuns draws the line of each run, width pixels wide at ratio 1,
// and points where the samples are few or far apart.
func strokeRuns(p *painter, g geom, runs [][]vertex, col color.RGBA, width float64) {
	hw := width * g.ratio / 2
	for _, run := range runs {
		for i := 1; i < len(run); i++ {
			p.segment(run[i-1], run[i], hw)
		}
		sparse := len(run) <= fewPoints ||
			(run[len(run)-1].x-run[0].x)/float64(len(run)-1) >= pointSpacing*g.ratio
		if sparse {
			for _, v := range run {
				p.disk(v.x, v.y, pointRadius*width/lineWidth*g.ratio)
			}
		}
	}
	p.flush(col, 1)
}

// refRun is a reference line at one value over pixel columns [x0, x1).
type refRun struct {
	x0, x1 int
	y      float64
	col    color.RGBA
}

type refLines []refRun

// refRuns are the reference lines, each as runs of one value, from the
// same values Render shows per cell, taken per pixel column. A value
// above the scale is left out: the text plot draws its arrow.
func (c Chart) refRuns(l layout, g geom) []refLines {
	out := make([]refLines, len(c.Refs))
	for i, ref := range c.Refs {
		col := rasterColor(ref.Style)
		var runs refLines
		for x := range g.w {
			from := c.Start.Add(time.Duration(float64(g.span) * float64(x) / float64(g.w)))
			to := c.Start.Add(time.Duration(float64(g.span) * float64(x+1) / float64(g.w)))
			v, ok := ref.at(from, to, x == g.w-1)
			if !ok || v <= 0 || v > l.top*(1+1e-9) {
				continue
			}
			y := g.y(v)
			if n := len(runs); n > 0 && runs[n-1].x1 == x && runs[n-1].y == y {
				runs[n-1].x1 = x + 1
				continue
			}
			runs = append(runs, refRun{x0: x, x1: x + 1, y: y, col: col})
		}
		out[i] = runs
	}
	return out
}

// shade shades the plot above the runs.
func (rl refLines) shade(p *painter) {
	for _, r := range rl {
		p.rect(float64(r.x0), 0, float64(r.x1), r.y, r.col, bandOpacity)
	}
}

// draw draws the runs dashed, in phase across runs, with a step where
// the value changes from one column to the next.
func (rl refLines) draw(p *painter, g geom) {
	width := math.Max(1, math.Round(refWidth*g.ratio))
	dash := math.Round(dashLen * g.ratio)
	period := dash + math.Round(dashGap*g.ratio)
	for i, r := range rl {
		y := snap(r.y, width)
		for x := math.Floor(float64(r.x0)/period) * period; x < float64(r.x1); x += period {
			x0, x1 := math.Max(x, float64(r.x0)), math.Min(x+dash, float64(r.x1))
			if x1 > x0 {
				p.rect(x0, y-width/2, x1, y+width/2, r.col, 1)
			}
		}
		if i > 0 && rl[i-1].x1 == r.x0 {
			prev := snap(rl[i-1].y, width)
			p.rect(float64(r.x0)-width/2, math.Min(prev, y)-width/2, float64(r.x0)+width/2, math.Max(prev, y)+width/2, r.col, 1)
		}
	}
}

// RasterKey identifies the picture Raster draws at cellW by cellH: two
// charts with the same key draw the same pixels. It reads the inputs,
// which costs far less than drawing them, so a caller can skip Raster
// when only the cursor moved.
func (c Chart) RasterKey(cellW, cellH int) uint64 {
	cols, rows := c.PlotSize()
	h := fnv.New64a()
	var buf [8]byte
	put := func(v uint64) {
		binary.LittleEndian.PutUint64(buf[:], v)
		_, _ = h.Write(buf[:])
	}
	putF := func(f float64) { put(math.Float64bits(f)) }
	putPoints := func(ps []Point) {
		putF(float64(len(ps)))
		for _, p := range ps {
			putF(float64(p.At.UnixNano()))
			putF(p.V)
		}
	}
	putColor := func(st lipgloss.Style) {
		col := rasterColor(st)
		put(uint64(col.R)<<16 | uint64(col.G)<<8 | uint64(col.B))
	}
	putI := func(v int64) { putF(float64(v)) }
	centered := 0
	if c.Centered {
		centered = 1
	}
	for _, v := range []int{cols, rows, cellW, cellH, int(c.Mode), centered} {
		putI(int64(v))
	}
	putI(c.Start.UnixNano())
	putI(c.End.UnixNano())
	putI(int64(c.Hold))
	if cols > 0 {
		putF(c.layout().top)
	}
	putColor(c.Style)
	putPoints(c.Points)
	putI(int64(len(c.Refs)))
	for _, r := range c.Refs {
		putI(int64(r.Hold))
		putF(r.V)
		putColor(r.Style)
		if r.Band {
			putI(1)
		}
		putPoints(r.Points)
	}
	putI(int64(len(c.Series)))
	for _, sr := range c.Series {
		putColor(sr.Style)
		putPoints(sr.Points)
	}
	return h.Sum64()
}

// TextCells marks the plot cells, by row and column, that an image of
// the plot leaves to the text chart: the words "no data" over a gap, the
// arrows of reference lines above the scale, and the cursor column.
func (c Chart) TextCells() [][]bool {
	cols, rows := c.PlotSize()
	if cols == 0 {
		return nil
	}
	l := c.layout()
	out := make([][]bool, rows)
	gaps := c.gapCells(l)
	for row := range out {
		out[row] = make([]bool, cols)
		for x := range cols {
			out[row][x] = (row == l.gapRow && gaps[x] > 0) || c.arrowAt(l, row, x) || x == c.Cursor
		}
	}
	return out
}

// arrowAt reports whether the text plot draws an above-the-scale arrow
// at row, x.
func (c Chart) arrowAt(l layout, row, x int) bool {
	r, ref := c.cellRune(l, row, x)
	return ref != nil && r == refAbove
}

// blend mixes b over a with weight f.
func blend(a, b color.RGBA, f float64) color.RGBA {
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*(1-f) + float64(y)*f)) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff}
}

// ImageColor is the color Raster draws a line of style st in, for a
// legend that matches the picture.
func ImageColor(st lipgloss.Style) color.RGBA { return rasterColor(st) }

// rasterColor is the foreground of st as pixels: a basic terminal color
// is its Grafana palette neighbor, a 256 or true color is itself. No
// foreground draws white.
func rasterColor(st lipgloss.Style) color.RGBA {
	fg := st.GetForeground()
	if bc, ok := fg.(ansi.BasicColor); ok {
		if c, ok := palette[bc%8]; ok {
			return c
		}
		if bc < 8 {
			fg = bc + 8
		}
	}
	if fg == nil {
		return color.RGBA{0xff, 0xff, 0xff, 0xff}
	}
	if _, isNo := fg.(lipgloss.NoColor); isNo {
		return color.RGBA{0xff, 0xff, 0xff, 0xff}
	}
	c, _ := color.RGBAModel.Convert(fg).(color.RGBA)
	c.A = 0xff
	return c
}

// painter draws antialiased shapes into an RGBA image. Strokes collect
// their coverage first, the largest per pixel, so where two segments of
// one line meet the pixels are not drawn twice; flush paints them.
type painter struct {
	img   *image.RGBA
	w, h  int
	cov   []float32
	dirty image.Rectangle
}

func newPainter(w, h int) *painter {
	return &painter{img: image.NewRGBA(image.Rect(0, 0, w, h)), w: w, h: h}
}

func (p *painter) fill(c color.RGBA) {
	for i := 0; i < len(p.img.Pix); i += 4 {
		p.img.Pix[i], p.img.Pix[i+1], p.img.Pix[i+2], p.img.Pix[i+3] = c.R, c.G, c.B, 0xff
	}
}

// blendAt paints c over the pixel at x, y with opacity a. The image holds
// premultiplied colors, so the same mix applied to the alpha channel as
// well paints over a transparent pixel correctly; an opaque one stays
// opaque.
func (p *painter) blendAt(x, y int, c color.RGBA, a float64) {
	if a <= 0 || x < 0 || y < 0 || x >= p.w || y >= p.h {
		return
	}
	a = math.Min(1, a)
	i := y*p.img.Stride + x*4
	px := p.img.Pix[i : i+4 : i+4]
	px[0] = uint8(float64(px[0]) + (float64(c.R)-float64(px[0]))*a + 0.5)
	px[1] = uint8(float64(px[1]) + (float64(c.G)-float64(px[1]))*a + 0.5)
	px[2] = uint8(float64(px[2]) + (float64(c.B)-float64(px[2]))*a + 0.5)
	px[3] = uint8(float64(px[3]) + (255-float64(px[3]))*a + 0.5)
}

// rect blends the rectangle [x0, x1) by [y0, y1), with partial cover on
// its edges.
func (p *painter) rect(x0, y0, x1, y1 float64, c color.RGBA, a float64) {
	for y := max(0, int(math.Floor(y0))); y < min(p.h, int(math.Ceil(y1))); y++ {
		cy := math.Min(y1, float64(y+1)) - math.Max(y0, float64(y))
		for x := max(0, int(math.Floor(x0))); x < min(p.w, int(math.Ceil(x1))); x++ {
			cx := math.Min(x1, float64(x+1)) - math.Max(x0, float64(x))
			p.blendAt(x, y, c, a*cx*cy)
		}
	}
}

func (p *painter) cover(x, y int, v float64) {
	if v <= 0 {
		return
	}
	if p.cov == nil {
		p.cov = make([]float32, p.w*p.h)
	}
	if i := y*p.w + x; float32(v) > p.cov[i] {
		p.cov[i] = float32(math.Min(1, v))
	}
}

func (p *painter) bounds(x0, y0, x1, y1 float64) image.Rectangle {
	r := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1))+1, int(math.Ceil(y1))+1).
		Intersect(image.Rect(0, 0, p.w, p.h))
	p.dirty = p.dirty.Union(r)
	return r
}

// segment covers a line from a to b, hw pixels to each side, with round
// ends.
func (p *painter) segment(a, b vertex, hw float64) {
	r := p.bounds(math.Min(a.x, b.x)-hw-1, math.Min(a.y, b.y)-hw-1, math.Max(a.x, b.x)+hw+1, math.Max(a.y, b.y)+hw+1)
	dx, dy := b.x-a.x, b.y-a.y
	ll := dx*dx + dy*dy
	for y := r.Min.Y; y < r.Max.Y; y++ {
		py := float64(y) + 0.5
		for x := r.Min.X; x < r.Max.X; x++ {
			px := float64(x) + 0.5
			t := 0.0
			if ll > 0 {
				t = math.Min(1, math.Max(0, ((px-a.x)*dx+(py-a.y)*dy)/ll))
			}
			d := math.Hypot(px-a.x-t*dx, py-a.y-t*dy)
			p.cover(x, y, hw+0.5-d)
		}
	}
}

// disk covers a filled circle.
func (p *painter) disk(cx, cy, rad float64) {
	r := p.bounds(cx-rad-1, cy-rad-1, cx+rad+1, cy+rad+1)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p.cover(x, y, rad+0.5-math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy))
		}
	}
}

// flush paints the collected coverage in c and clears it.
func (p *painter) flush(c color.RGBA, a float64) {
	r := p.dirty
	p.dirty = image.Rectangle{}
	if p.cov == nil {
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := p.cov[y*p.w : (y+1)*p.w]
		for x := r.Min.X; x < r.Max.X; x++ {
			if v := row[x]; v > 0 {
				p.blendAt(x, y, c, a*float64(v))
				row[x] = 0
			}
		}
	}
}
