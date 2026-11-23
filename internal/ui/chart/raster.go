package chart

import (
	"image"
	"image/color"
	"math"
	"slices"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Background is the plot color of a raster.
var Background = color.RGBA{A: 0xff}

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

// Raster draws the plot of the chart, PlotSize cells at cellW by cellH pixels
// each, from the same model as Render: the same pooling and hold, the same
// scale and the same reference line values, at one sample column per
// pixel. Usage is a filled area under a solid edge, a reference line is
// dashed, a gap is left empty, and the cursor cell is lighter. It is nil
// when the chart has no plot.
func (c Chart) Raster(cellW, cellH int) *image.RGBA {
	cols, rows := c.PlotSize()
	if cols == 0 || cellW < 1 || cellH < 1 {
		return nil
	}
	l := c.layout()
	w, h := cols*cellW, rows*cellH
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, img.Bounds(), Background)

	use := rasterColor(c.Style)
	area := blend(Background, use, 0.45)
	edge := max(1, cellH/8)
	// A zero sample is drawn one pixel high, apart from a gap, as the text
	// charts draw one unit.
	samples := c.pool(w)
	for x, col := range samples {
		if !col.ok {
			continue
		}
		top := h - max(1, min(h, int(math.Round(col.v/l.top*float64(h)))))
		fill(img, image.Rect(x, top, x+1, h), area)
		fill(img, image.Rect(x, top, x+1, min(h, top+edge)), use)
	}
	// The first reference wins where two meet, as in the text charts.
	thick := max(1, cellH/10)
	dash, gap := max(3, cellW), max(2, cellW/2)
	span := c.End.Sub(c.Start)
	for _, ref := range slices.Backward(c.Refs) {
		col := rasterColor(ref.Style)
		for x := range w {
			if x%(dash+gap) >= dash {
				continue
			}
			from := c.Start.Add(time.Duration(float64(span) * float64(x) / float64(w)))
			to := c.Start.Add(time.Duration(float64(span) * float64(x+1) / float64(w)))
			v, ok := ref.at(from, to, x == w-1)
			// A line above the scale is the text plot's arrow (TextCells).
			if !ok || v <= 0 || v > l.top*(1+1e-9) {
				continue
			}
			y := h - int(math.Round(v/l.top*float64(h)))
			y = min(h-thick, max(0, y-thick/2))
			fill(img, image.Rect(x, y, x+1, y+thick), col)
		}
	}
	if c.Cursor >= 0 && c.Cursor < cols {
		r := image.Rect(c.Cursor*cellW, 0, (c.Cursor+1)*cellW, h)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				img.SetRGBA(x, y, blend(img.RGBAAt(x, y), color.RGBA{0xff, 0xff, 0xff, 0xff}, 0.3))
			}
		}
	}
	return img
}

// TextCells marks the plot cells, by row and column, that an image of
// the plot leaves to the text chart: the columns without a sample, which
// draw the "no data" band, and the arrows of reference lines above the
// scale.
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
			out[row][x] = gaps[x] != 0 || c.arrowAt(l, row, x)
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

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// blend mixes b over a with weight f.
func blend(a, b color.RGBA, f float64) color.RGBA {
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*(1-f) + float64(y)*f)) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff}
}

// rasterColor is the foreground of st as pixels. The 8 basic colors take
// their bright variant: their xterm values are dim on a black plot. No
// foreground draws white.
func rasterColor(st lipgloss.Style) color.RGBA {
	fg := st.GetForeground()
	if bc, ok := fg.(ansi.BasicColor); ok && bc < 8 {
		fg = bc + 8
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
