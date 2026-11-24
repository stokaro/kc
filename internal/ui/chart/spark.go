package chart

import (
	"image"
	"image/color"
	"math"
	"slices"
	"time"
)

const (
	// sparkRadius rounds the panel behind a sparkline, in pixels at a
	// device pixel ratio of 1.
	sparkRadius = 3.0
	// sparkPadX and sparkPadY keep the line off the panel's edges, in
	// pixels at that ratio; the zero line sits sparkPadY above the bottom.
	sparkPadX = 3.0
	sparkPadY = 2.5
	// sparkLine is the width of the line at that ratio: Grafana's stat
	// panel draws its sparkline 1 px wide; a terminal cell is small, so a
	// little more keeps it readable.
	sparkLine = 1.25
)

// Spark is a sparkline as Grafana's stat panel draws it: a thin line
// from sample to sample over [Start, End], with the area under it fading
// out towards zero, on a dark rounded panel. It has no axes: the caller
// labels the span and the scale.
type Spark struct {
	Start, End time.Time
	Points     []Point
	// Top is the value at the top of the plot; a higher sample is cut.
	Top float64
	// Gap is the longest time between two samples that are joined.
	Gap   time.Duration
	Color color.RGBA
}

// sparkGeom maps the samples to pixels of a sparkline picture; x0 and x1
// are where the line starts and ends, inside the panel's padding.
func (s Spark) sparkGeom(cols, rows, cellW, cellH int) (g geom, x0, x1 float64) {
	w, h := cols*cellW, rows*cellH
	ratio := max(1, float64(cellH)/cssCell)
	padX := math.Round(sparkPadX * ratio)
	padY := math.Round(sparkPadY * ratio)
	g = geom{
		w: w, h: h, cellW: cellW, ratio: ratio, top: s.Top,
		yTop: padY + ratio, yZero: float64(h) - padY,
		start: s.Start, span: s.End.Sub(s.Start),
	}
	return g, padX, float64(w) - padX
}

// Raster draws the sparkline cols by rows cells at cellW by cellH pixels
// each. Outside the rounded panel the picture is transparent. It is nil
// for an empty size or range.
func (s Spark) Raster(cols, rows, cellW, cellH int) *image.RGBA {
	if cols < 1 || rows < 1 || cellW < 1 || cellH < 1 || !s.End.After(s.Start) || s.Top <= 0 {
		return nil
	}
	g, x0, x1 := s.sparkGeom(cols, rows, cellW, cellH)
	p := newPainter(g.w, g.h)
	w, h := float64(g.w), float64(g.h)
	r := math.Round(sparkRadius * g.ratio)
	edge := math.Max(1, math.Round(g.ratio/2))
	p.roundRect(0, 0, w, h, r+edge, func(float64) (color.RGBA, float64) { return trackEdge, 1 })
	p.roundRect(edge, edge, w-edge, h-edge, r, func(float64) (color.RGBA, float64) { return TrackColor, 1 })
	runs := s.runs(g, x0, x1)
	for _, run := range runs {
		fillUnder(p, g, run, s.Color)
	}
	strokeRuns(p, g, runs, s.Color, sparkLine)
	return p.img
}

// runs are the samples as pixels between x0 and x1, cut where two are
// further apart than Gap. The newest sample before Start continues a line
// into the range; the panel's padding takes the part before x0.
func (s Spark) runs(g geom, x0, x1 float64) [][]vertex {
	pts := slices.Clone(s.Points)
	slices.SortStableFunc(pts, func(a, b Point) int { return a.At.Compare(b.At) })
	span := float64(s.End.Sub(s.Start))
	x := func(t time.Time) float64 { return x0 + float64(t.Sub(s.Start))/span*(x1-x0) }
	var out [][]vertex
	var run []vertex
	var prev *Point
	var last time.Time
	for i := range pts {
		pt := pts[i]
		if math.IsNaN(pt.V) || math.IsInf(pt.V, 0) || pt.At.After(s.End) {
			continue
		}
		if pt.At.Before(s.Start) {
			prev = &pts[i]
			continue
		}
		if prev != nil {
			if pt.At.Sub(prev.At) <= s.Gap && len(run) == 0 {
				// Cut the segment from the sample before the range at
				// the start of the range.
				a, b := vertex{x: x(prev.At), y: g.y(math.Min(prev.V, s.Top))}, vertex{x: x(pt.At), y: g.y(math.Min(pt.V, s.Top))}
				f := (x0 - a.x) / (b.x - a.x)
				run = append(run, vertex{x: x0, y: a.y + (b.y-a.y)*f})
				last = prev.At
			}
			prev = nil
		}
		if len(run) > 0 && pt.At.Sub(last) > s.Gap {
			out, run = append(out, run), nil
		}
		run = append(run, vertex{x: x(pt.At), y: g.y(math.Max(0, math.Min(pt.V, s.Top)))})
		last = pt.At
	}
	if len(run) > 0 {
		out = append(out, run)
	}
	return out
}

// RasterKey identifies the picture Raster draws: two sparklines with the
// same key draw the same pixels. Of the samples before Start only the
// newest counts: the others are not drawn.
func (s Spark) RasterKey(cols, rows, cellW, cellH int) uint64 {
	k := newKeyHash()
	k.ints(cols, rows, cellW, cellH)
	k.int(s.Start.UnixNano())
	k.int(s.End.UnixNano())
	k.float(s.Top)
	k.int(int64(s.Gap))
	k.color(s.Color)
	first := 0
	for i, pt := range s.Points {
		if pt.At.Before(s.Start) {
			first = i
		}
	}
	pts := s.Points[first:]
	k.ints(len(pts))
	for _, pt := range pts {
		k.int(pt.At.UnixNano())
		k.float(pt.V)
	}
	return k.sum()
}
