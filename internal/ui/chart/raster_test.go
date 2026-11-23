package chart_test

import (
	"image"
	"image/color"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/chart"
)

func rgba(c color.Color) color.RGBA {
	out, _ := color.RGBAModel.Convert(c).(color.RGBA)
	out.A = 0xff
	return out
}

// rasterChart is a 10-minute chart scaled to 10, with a sample every
// minute and a hold of 90 seconds.
func rasterChart(points []chart.Point) chart.Chart {
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	for i := range points {
		points[i].At = start.Add(points[i].At.Sub(time.Time{}))
	}
	return chart.Chart{
		Start: start, End: start.Add(10 * time.Minute), Points: points, Hold: 90 * time.Second,
		Width: 24, Height: 5, Cursor: -1,
		Style: lipgloss.NewStyle().Foreground(lipgloss.Green),
		Scale: func(float64) float64 { return 10 },
	}
}

func steady(v float64, from, to int) []chart.Point {
	var out []chart.Point
	for m := from; m <= to; m++ {
		out = append(out, chart.Point{At: time.Time{}.Add(time.Duration(m) * time.Minute), V: v})
	}
	return out
}

func TestRasterSizeAndPlot(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(5, 0, 10))
	cols, rows := c.PlotSize()
	if cols != c.Cells() || rows != c.Height-1 {
		t.Fatalf("PlotSize = %dx%d, want %dx%d", cols, rows, c.Cells(), c.Height-1)
	}
	img := c.Raster(6, 10)
	if got, want := img.Bounds(), image.Rect(0, 0, cols*6, rows*10); got != want {
		t.Fatalf("bounds %v, want %v", got, want)
	}
	// The text plot sits where PlotSize says.
	lines := c.Render()
	for _, l := range lines[:rows] {
		if r := []rune(stripANSI(l)); r[len(r)-cols-1] != '│' {
			t.Fatalf("the plot does not start after the axis in %q", stripANSI(l))
		}
	}
	if (chart.Chart{Width: 5, Height: 1}).Raster(6, 10) != nil {
		t.Error("a chart without a plot rasterizes")
	}
}

func TestRasterFillsUsageUpToItsValue(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(5, 0, 10))
	img := c.Raster(6, 10)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	edge := rgba(lipgloss.BrightGreen)
	for _, x := range []int{0, w / 2, w - 1} {
		if got := img.RGBAAt(x, h/2-1); got != chart.Background {
			t.Errorf("x=%d: above half of the scale is %v, want the background", x, got)
		}
		if got := img.RGBAAt(x, h/2); got != edge {
			t.Errorf("x=%d: the top of the area is %v, want the usage color %v", x, got, edge)
		}
		area := img.RGBAAt(x, h-1)
		if area == chart.Background || area == edge || area.G == 0 {
			t.Errorf("x=%d: the area is %v, want a dimmer green", x, area)
		}
	}
}

func TestRasterLeavesGapsEmpty(t *testing.T) {
	t.Parallel()
	// Samples for the first four minutes only: after the hold, a gap.
	c := rasterChart(steady(8, 0, 4))
	img := c.Raster(6, 10)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if got := img.RGBAAt(w/10, h-1); got == chart.Background {
		t.Error("the first minutes are not drawn")
	}
	for _, x := range []int{w * 7 / 10, w - 1} {
		for y := range h {
			if got := img.RGBAAt(x, y); got != chart.Background {
				t.Fatalf("gap pixel (%d,%d) is %v", x, y, got)
			}
		}
	}
	// A zero sample is one pixel high, apart from a gap.
	z := rasterChart(steady(0, 0, 10)).Raster(6, 10)
	if got := z.RGBAAt(0, h-1); got == chart.Background {
		t.Error("a zero sample is not drawn")
	}
	if got := z.RGBAAt(0, h-2); got != chart.Background {
		t.Errorf("a zero sample is %v two pixels up", got)
	}
}

func TestRasterDashesReferenceLines(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(2, 0, 10))
	c.Refs = []chart.Ref{{V: 8, Style: lipgloss.NewStyle().Foreground(lipgloss.Red)}}
	img := c.Raster(6, 10)
	h := img.Bounds().Dy()
	y := h - 8*h/10 // the line's row; it is one pixel thick at 10 px cells
	red := rgba(lipgloss.BrightRed)
	// Dashes of 6 pixels (the cell width) and gaps of 3.
	for x, want := range map[int]bool{0: true, 5: true, 6: false, 8: false, 9: true} {
		if on := img.RGBAAt(x, y) == red; on != want {
			t.Errorf("x=%d on the line: dash %v, want %v (%v)", x, on, want, img.RGBAAt(x, y))
		}
	}
	if got := img.RGBAAt(0, y-2); got != chart.Background {
		t.Errorf("the line is thicker than one pixel: %v above it", got)
	}
}

func TestRasterLightensTheCursorCell(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(5, 0, 10))
	plain := c.Raster(6, 10)
	c.Cursor = 2
	img := c.Raster(6, 10)
	if got := img.RGBAAt(2*6, 0); got.R <= plain.RGBAAt(2*6, 0).R {
		t.Errorf("cursor cell %v is not lighter than %v", got, plain.RGBAAt(2*6, 0))
	}
	if img.RGBAAt(3*6, 0) != plain.RGBAAt(3*6, 0) || img.RGBAAt(2*6-1, 0) != plain.RGBAAt(2*6-1, 0) {
		t.Error("the cursor reaches beyond its cell")
	}
}
