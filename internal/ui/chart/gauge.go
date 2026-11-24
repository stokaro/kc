package chart

import (
	"image"
	"image/color"
	"math"
)

// The parts of a bar gauge picture, as Grafana's bar gauge draws them in
// the dark theme.
var (
	// TrackColor is the unfilled part of a gauge and the panel behind a
	// sparkline: Grafana's panel color.
	TrackColor = Background
	// trackEdge outlines the track faintly, so it stands out from a dark
	// panel color too.
	trackEdge = blend(Background, color.RGBA{0xcc, 0xcc, 0xdc, 0xff}, 0.12)
)

const (
	// gaugeTrack is the height of the track, as a share of the picture.
	gaugeTrack = 0.5
	// gaugeRadius is the corner radius of the track, in pixels at a
	// device pixel ratio of 1 (Grafana rounds its bars by 2 to 3 px).
	gaugeRadius = 3.0
	// The fill fades from fillFrom at the start of the track to fillTo at
	// its end, and ends in a solid edge of capWidth: Grafana's basic bar
	// with a gradient.
	fillFrom = 0.62
	fillTo   = 0.95
	capWidth = 2.0
	// markWidth is a request or limit mark, markReach how much of the
	// picture's height it spans: it sticks out of the track, so it reads
	// over a fill of its own color too.
	markWidth = 2.0
	markReach = 0.8
)

// Gauge is a horizontal bar gauge: a dark rounded track filled to
// Percent in Color, with Marks as thin ticks across it. It knows nothing
// about what the percentage measures.
type Gauge struct {
	// Percent fills the track: 0 to 100, more fills it.
	Percent float64
	// Color fills the track; the caller picks it by level, as Grafana's
	// thresholds do.
	Color color.RGBA
	// Marks are drawn over the track, such as a request and a limit.
	Marks []Mark
}

// Mark is a tick on a gauge at Percent of its scale. One above 100 is a
// small arrow at the end of the track: it is off the scale.
type Mark struct {
	Percent float64
	Color   color.RGBA
}

// gaugeGeom is a gauge picture in pixels: the track, the end of the fill
// and the x of each mark, all as whole or half pixels, so two gauges with
// the same geometry draw the same pixels.
type gaugeGeom struct {
	w, h         int
	ratio        float64
	top, bottom  float64
	radius       float64
	fill         float64 // the x where the fill ends; 0 for none
	marks        []float64
	above        []bool
	reachTop     float64
	reachBottom  float64
	markW, capW  float64
	colors       []color.RGBA
	fillColor    color.RGBA
	cols, rows   int
	cellW, cellH int
}

func (g Gauge) geom(cols, rows, cellW, cellH int) gaugeGeom {
	w, h := cols*cellW, rows*cellH
	ratio := max(1, float64(cellH)/cssCell)
	cell := float64(cellH)
	// The track sits in the middle of the last row, so a gauge of two
	// rows still lines up with the text after it.
	mid := float64(h) - cell/2
	half := math.Round(cell * gaugeTrack / 2)
	gg := gaugeGeom{
		w: w, h: h, ratio: ratio, top: mid - half, bottom: mid + half,
		markW: math.Max(1, math.Round(markWidth*ratio)), capW: math.Max(1, math.Round(capWidth*ratio)),
		fillColor: g.Color, cols: cols, rows: rows, cellW: cellW, cellH: cellH,
	}
	gg.radius = math.Min(half, math.Round(gaugeRadius*ratio))
	reach := math.Round(cell * markReach / 2)
	gg.reachTop, gg.reachBottom = mid-reach, mid+reach
	pct := math.Max(0, math.Min(g.Percent, 100))
	if !math.IsNaN(pct) {
		gg.fill = math.Round(pct / 100 * float64(w))
	}
	for _, m := range g.Marks {
		if math.IsNaN(m.Percent) || m.Percent < 0 {
			continue
		}
		above := m.Percent > 100
		// A mark at 0 or 100 stays whole inside the picture.
		x := math.Round(math.Min(m.Percent, 100) / 100 * float64(w))
		x = math.Max(gg.markW/2, math.Min(float64(w)-gg.markW/2, x))
		gg.marks = append(gg.marks, x)
		gg.above = append(gg.above, above)
		gg.colors = append(gg.colors, m.Color)
	}
	return gg
}

// GaugeFill is where the fill of a gauge cols cells wide ends, in pixels
// from the left of its picture.
func (g Gauge) GaugeFill(cols, cellW, cellH int) int {
	return int(g.geom(cols, 1, cellW, cellH).fill)
}

// Raster draws the gauge cols by rows cells at cellW by cellH pixels
// each. Outside the track the picture is transparent: the terminal shows
// the cell background there. It is nil for an empty size.
func (g Gauge) Raster(cols, rows, cellW, cellH int) *image.RGBA {
	if cols < 1 || rows < 1 || cellW < 1 || cellH < 1 {
		return nil
	}
	gg := g.geom(cols, rows, cellW, cellH)
	p := newPainter(gg.w, gg.h)
	w := float64(gg.w)
	// The edge first, then the track inside it.
	edge := math.Max(1, math.Round(gg.ratio/2))
	p.roundRect(0, gg.top-edge, w, gg.bottom+edge, gg.radius+edge, func(float64) (color.RGBA, float64) { return trackEdge, 1 })
	p.roundRect(edge, gg.top, w-edge, gg.bottom, gg.radius, func(float64) (color.RGBA, float64) { return TrackColor, 1 })
	if gg.fill > 0 {
		fill := gg.fill
		col := gg.fillColor
		p.roundRectClip(edge, gg.top, w-edge, gg.bottom, gg.radius, fill, func(x float64) (color.RGBA, float64) {
			t := x / w
			a := fillFrom + (fillTo-fillFrom)*t
			if x >= fill-gg.capW {
				a = 1
			}
			return col, a
		})
	}
	for i, x := range gg.marks {
		gg.drawMark(p, x, gg.above[i], gg.colors[i])
	}
	return p.img
}

// drawMark draws a tick across the track with a dark outline, or, for a
// mark above the scale, an arrow at the end of the track pointing out.
func (gg gaugeGeom) drawMark(p *painter, x float64, above bool, col color.RGBA) {
	hw := gg.markW / 2
	if above {
		// A triangle at the right end, as tall as the tick, pointing
		// right: the value is further than the track goes.
		tip := float64(gg.w)
		size := (gg.reachBottom - gg.reachTop) / 2
		mid := (gg.reachTop + gg.reachBottom) / 2
		p.triangle(vertex{x: tip - size*1.1, y: mid - size}, vertex{x: tip - size*1.1, y: mid + size}, vertex{x: tip, y: mid}, TrackColor, 1, 1.5*gg.ratio)
		p.triangle(vertex{x: tip - size*1.1, y: mid - size}, vertex{x: tip - size*1.1, y: mid + size}, vertex{x: tip, y: mid}, col, 1, 0)
		return
	}
	outline := math.Max(1, math.Round(gg.ratio))
	p.rect(x-hw-outline, gg.reachTop-outline, x+hw+outline, gg.reachBottom+outline, TrackColor, 0.9)
	p.rect(x-hw, gg.reachTop, x+hw, gg.reachBottom, col, 1)
}

// RasterKey identifies the picture Raster draws: two gauges with the same
// key draw the same pixels. A change of the percentage smaller than a
// pixel keeps the key, so the caller skips the raster.
func (g Gauge) RasterKey(cols, rows, cellW, cellH int) uint64 {
	gg := g.geom(cols, rows, cellW, cellH)
	k := newKeyHash()
	k.ints(cols, rows, cellW, cellH)
	k.float(gg.fill)
	k.color(gg.fillColor)
	k.ints(len(gg.marks))
	for i, x := range gg.marks {
		k.float(x)
		k.flag(gg.above[i])
		k.color(gg.colors[i])
	}
	return k.sum()
}

// roundRect fills the rectangle [x0, x1) by [y0, y1) with corners of
// radius r, antialiased. paint gives the color and the opacity at each
// pixel column.
func (p *painter) roundRect(x0, y0, x1, y1, r float64, paint func(x float64) (color.RGBA, float64)) {
	p.roundRectClip(x0, y0, x1, y1, r, x1, paint)
}

// roundRectClip is roundRect up to x = clip only, with a soft edge there.
func (p *painter) roundRectClip(x0, y0, x1, y1, r, clip float64, paint func(x float64) (color.RGBA, float64)) {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	bx, by := (x1-x0)/2, (y1-y0)/2
	r = math.Min(r, math.Min(bx, by))
	for y := max(0, int(math.Floor(y0))); y < min(p.h, int(math.Ceil(y1))); y++ {
		py := float64(y) + 0.5
		for x := max(0, int(math.Floor(x0))); x < min(p.w, int(math.Ceil(math.Min(x1, clip)))); x++ {
			px := float64(x) + 0.5
			cover := math.Min(1, math.Max(0, 0.5-roundBoxDistance(px-cx, py-cy, bx, by, r)))
			if c := math.Min(clip, float64(x+1)) - float64(x); c < 1 {
				cover *= math.Max(0, c)
			}
			if cover <= 0 {
				continue
			}
			col, a := paint(px)
			p.blendAt(x, y, col, a*cover)
		}
	}
}

// roundBoxDistance is the signed distance from (x, y) to a box of half
// size bx by by with corners of radius r, centered at the origin:
// negative inside.
func roundBoxDistance(x, y, bx, by, r float64) float64 {
	qx := math.Abs(x) - bx + r
	qy := math.Abs(y) - by + r
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return out + math.Min(math.Max(qx, qy), 0) - r
}

// triangle fills the triangle a, b, c grown by grow pixels, antialiased.
func (p *painter) triangle(a, b, c vertex, col color.RGBA, alpha, grow float64) {
	minX := math.Min(a.x, math.Min(b.x, c.x)) - grow - 1
	maxX := math.Max(a.x, math.Max(b.x, c.x)) + grow + 1
	minY := math.Min(a.y, math.Min(b.y, c.y)) - grow - 1
	maxY := math.Max(a.y, math.Max(b.y, c.y)) + grow + 1
	edges := [3][2]vertex{{a, b}, {b, c}, {c, a}}
	// The winding decides which side of each edge is inside.
	sign := 1.0
	if (b.x-a.x)*(c.y-a.y)-(b.y-a.y)*(c.x-a.x) < 0 {
		sign = -1
	}
	for y := max(0, int(math.Floor(minY))); y < min(p.h, int(math.Ceil(maxY))); y++ {
		py := float64(y) + 0.5
		for x := max(0, int(math.Floor(minX))); x < min(p.w, int(math.Ceil(maxX))); x++ {
			px := float64(x) + 0.5
			d := math.Inf(-1)
			for _, e := range edges {
				ex, ey := e[1].x-e[0].x, e[1].y-e[0].y
				l := math.Hypot(ex, ey)
				if l == 0 {
					continue
				}
				// Distance outside this edge's line: positive outside.
				d = math.Max(d, -sign*(ex*(py-e[0].y)-ey*(px-e[0].x))/l)
			}
			if cover := math.Min(1, math.Max(0, 0.5-(d-grow))); cover > 0 {
				p.blendAt(x, y, col, alpha*cover)
			}
		}
	}
}
