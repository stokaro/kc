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
// dashed, and a gap is left empty. The cursor is not drawn: it is a text
// cell over the image (TextCells), so moving it changes no picture. It is
// nil when the chart has no plot.
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
	return img
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
	for _, v := range []int{cols, rows, cellW, cellH, int(c.Mode)} {
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
	for _, r := range c.Refs {
		putI(int64(r.Hold))
		putF(r.V)
		putColor(r.Style)
		putPoints(r.Points)
	}
	return h.Sum64()
}

// TextCells marks the plot cells, by row and column, that an image of
// the plot leaves to the text chart: the columns without a sample, which
// draw the "no data" band, the arrows of reference lines above the scale,
// and the cursor column.
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
			out[row][x] = gaps[x] != 0 || c.arrowAt(l, row, x) || x == c.Cursor
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
