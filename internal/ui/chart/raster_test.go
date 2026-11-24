package chart_test

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/chart"
)

var (
	grafanaGreen = color.RGBA{0x73, 0xbf, 0x69, 0xff}
	grafanaRed   = color.RGBA{0xf2, 0x49, 0x5c, 0xff}
)

// rasterChart is a 10-minute chart scaled to 10, with a sample every
// minute and a hold of 90 seconds.
func rasterChart(points []chart.Point) chart.Chart {
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	for i := range points {
		points[i].At = start.Add(points[i].At.Sub(time.Time{}))
	}
	return chart.Chart{
		Start: start, End: start.Add(10 * time.Minute), Points: points, Hold: 90 * time.Second,
		Width: 24, Height: 6, Cursor: -1, Centered: true,
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

// greenness is how much greener than the background a pixel is.
func greenness(c color.RGBA) int { return int(c.G) - int(chart.Background.G) }

// lineTop is the first row of column x drawn in nearly the full usage
// color, -1 for none.
func lineTop(img *image.RGBA, x int) int {
	for y := range img.Bounds().Dy() {
		if greenness(img.RGBAAt(x, y)) > greenness(grafanaGreen)*3/4 {
			return y
		}
	}
	return -1
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

// TestRasterJoinsSamplesWithLines: between two samples the line runs
// straight, not in a step, through the value labels' grid: zero on the
// center of the last row, the top on the center of the first.
func TestRasterJoinsSamplesWithLines(t *testing.T) {
	t.Parallel()
	c := rasterChart([]chart.Point{{At: time.Time{}, V: 0}, {At: time.Time{}.Add(10 * time.Minute), V: 10}})
	c.Hold = 11 * time.Minute
	img := c.Raster(6, 10)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	yZero, yTop := float64(h)-5, 5.0
	for _, f := range []float64{0.25, 0.5, 0.75} {
		x := int(f * float64(w))
		want := yZero - (float64(x)+0.5)/float64(w)*(yZero-yTop)
		if got := lineTop(img, x); math.Abs(float64(got)-want) > 1.5 {
			t.Errorf("x=%d: the line is at row %d, want about %.1f", x, got, want)
		}
	}
}

// TestRasterFadesTheArea: the area under the line is green, more so
// near the line than near zero, and nothing is drawn above the line.
func TestRasterFadesTheArea(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(8, 0, 10))
	img := c.Raster(6, 10)
	x := img.Bounds().Dx()/2 + 1
	top := lineTop(img, x)
	if top < 0 {
		t.Fatal("no line")
	}
	high, low := greenness(img.RGBAAt(x, top+4)), greenness(img.RGBAAt(x, img.Bounds().Dy()-8))
	if high <= low || low <= 0 {
		t.Errorf("area greenness %d under the line and %d near zero, want fading but above the background", high, low)
	}
	if got := img.RGBAAt(x, top-4); greenness(got) > 0 && got.G > got.R+20 {
		t.Errorf("above the line is %v", got)
	}
	if got := img.RGBAAt(x, img.Bounds().Dy()-1); got != chart.Background {
		t.Errorf("under zero is %v, want the background", got)
	}
}

func TestRasterLeavesGapsEmpty(t *testing.T) {
	t.Parallel()
	// Samples for the first four minutes only: after them, a gap.
	c := rasterChart(steady(8, 0, 4))
	img := c.Raster(6, 10)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if lineTop(img, w/10) < 0 {
		t.Error("the first minutes are not drawn")
	}
	for _, x := range []int{w * 6 / 10, w - 1} {
		for y := range h {
			if got := img.RGBAAt(x, y); got.G > got.R+8 {
				t.Fatalf("gap pixel (%d,%d) is %v", x, y, got)
			}
		}
	}
	// A zero sample is a line on the zero grid line.
	z := rasterChart(steady(0, 0, 10)).Raster(6, 10)
	if lineTop(z, 0) != h-6 && lineTop(z, 0) != h-5 {
		t.Errorf("a zero sample is drawn at row %d", lineTop(z, 0))
	}
}

func TestRasterShowsALonelySample(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(5, 5, 5))
	img := c.Raster(6, 10)
	x := img.Bounds().Dx() / 2
	if lineTop(img, x) < 0 {
		t.Error("a sample without neighbors is not drawn")
	}
}

func TestRasterDashesReferenceLines(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(2, 0, 10))
	c.Refs = []chart.Ref{{V: 10, Style: lipgloss.NewStyle().Foreground(lipgloss.Red)}}
	img := c.Raster(6, 10)
	y := 5 // the top of the scale: the center of the first row
	on, off := 0, 0
	for x := range img.Bounds().Dx() {
		if img.RGBAAt(x, y) == grafanaRed {
			on++
		} else {
			off++
		}
	}
	if on == 0 || off == 0 {
		t.Errorf("the line has %d pixels in red and %d not, want dashes", on, off)
	}
	if got := img.RGBAAt(0, y+2); got == grafanaRed {
		t.Errorf("the line is thicker than one pixel at a ratio of 1")
	}
}

func TestRasterShadesABand(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(2, 0, 10))
	c.Refs = []chart.Ref{{V: 5, Band: true, Style: lipgloss.NewStyle().Foreground(lipgloss.Red)}}
	img := c.Raster(6, 10)
	x := 2
	if got := img.RGBAAt(x, 10); got == chart.Background || got.R <= got.G {
		t.Errorf("above the line is %v, want a red tint", got)
	}
	plain := c
	plain.Refs = []chart.Ref{{V: 5, Style: lipgloss.NewStyle().Foreground(lipgloss.Red)}}
	if got := plain.Raster(6, 10).RGBAAt(x, 10); got.R > got.G+10 {
		t.Errorf("a line without a band tints %v", got)
	}
	if plain.RasterKey(6, 10) == c.RasterKey(6, 10) {
		t.Error("the band does not change the key")
	}
}

func TestRasterDrawsSeries(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(8, 0, 10))
	key := c.RasterKey(6, 10)
	purple := color.RGBA{0xb8, 0x77, 0xd9, 0xff}
	c.Series = []chart.Series{{Points: rasterChart(steady(3, 0, 10)).Points, Style: lipgloss.NewStyle().Foreground(lipgloss.Magenta)}}
	// At a pixel ratio of 2 the series line is two pixels wide, so some
	// pixels are its color alone.
	img := c.Raster(12, 36)
	found := false
	for y := range img.Bounds().Dy() {
		if got := img.RGBAAt(img.Bounds().Dx()/2, y); got == purple {
			found = true
		}
	}
	if !found {
		t.Error("the series is not drawn in its palette color")
	}
	if c.RasterKey(6, 10) == key {
		t.Error("the series does not change the key")
	}
	if got := chart.ImageColor(lipgloss.NewStyle().Foreground(lipgloss.BrightGreen)); got != grafanaGreen {
		t.Errorf("green draws as %v, want Grafana's", got)
	}
}

// TestCursorIsTextOverTheImage: the cursor changes neither the picture
// nor its key, so moving it sends no image; its column is a text cell,
// a thin line with CursorLine.
func TestCursorIsTextOverTheImage(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(5, 0, 10))
	plain, key := c.Raster(6, 10), c.RasterKey(6, 10)
	c.Cursor = 2
	if !bytes.Equal(c.Raster(6, 10).Pix, plain.Pix) || c.RasterKey(6, 10) != key {
		t.Error("the cursor changed the picture")
	}
	for row, cells := range c.TextCells() {
		if !cells[2] || cells[3] {
			t.Errorf("row %d: text cells %v, want the cursor column only", row, cells)
		}
	}
	c.CursorLine = lipgloss.NewStyle().Foreground(lipgloss.White)
	cols, _ := c.PlotSize()
	for _, l := range c.Render()[:c.Height-1] {
		if r := []rune(stripANSI(l)); r[len(r)-cols+2] != '│' {
			t.Errorf("the cursor cell is %q, want a thin line", r[len(r)-cols+2])
		}
	}
	// The data and the size do change the key.
	if c.RasterKey(7, 10) == key {
		t.Error("another cell size has the same key")
	}
	c.Points = c.Points[:len(c.Points)-1]
	if c.RasterKey(6, 10) == key {
		t.Error("other points have the same key")
	}
}

// TestTextCellsKeepTheWordsAndTheArrows: an image of the plot leaves the
// words "no data" and the arrows of a line above the scale to the text,
// draws the rest of a gap as an empty plot, and draws no line above the
// scale itself.
func TestTextCellsKeepTheWordsAndTheArrows(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	var pts []chart.Point
	for i := range 5 {
		pts = append(pts, chart.Point{At: start.Add(time.Duration(i) * time.Minute), V: 1})
	}
	c := chart.Chart{
		Start: start, End: start.Add(30 * time.Minute), Mode: chart.Blocks, Width: 31, Height: 5, Cursor: -1,
		Points: pts, Hold: 90 * time.Second, Format: func(float64) string { return "" },
		Style: lipgloss.NewStyle().Foreground(lipgloss.Green),
		Refs:  []chart.Ref{{V: 50, Style: lipgloss.NewStyle().Foreground(lipgloss.Red)}},
	}
	cols, rows := c.PlotSize()
	text := c.TextCells()
	if len(text) != rows || len(text[0]) != cols {
		t.Fatalf("TextCells is %dx%d, want %dx%d", len(text), len(text[0]), rows, cols)
	}
	if text[rows-1][0] || text[rows-1][cols-2] {
		t.Error("a plain plot cell is left to text")
	}
	label := 0
	for _, on := range text[rows/2] { // not Centered: the middle row
		if on {
			label++
		}
	}
	if label != len("no data") {
		t.Errorf("%d cells of the gap row are text, want the %d of \"no data\"", label, len("no data"))
	}
	if !c.OffScale(0) || !text[0][cols-1] {
		t.Errorf("the arrow of the line above the scale (off %v) is not left to text", c.OffScale(0))
	}
	img := c.Raster(4, 8)
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			if img.RGBAAt(x, y) == grafanaRed {
				t.Fatalf("the line above the scale is drawn at pixel %d,%d", x, y)
			}
		}
	}
}

// TestCenteredLabels: the value labels of a Centered chart are round
// numbers on rows whose centers are their values.
func TestCenteredLabels(t *testing.T) {
	t.Parallel()
	label := func(l string) string {
		s := stripANSI(l)
		return strings.TrimSpace(s[:strings.IndexRune(s, '│')])
	}
	for _, tc := range []struct {
		name   string
		height int
		want   map[int]string
	}{
		{"quarters on 13 rows", 14, map[int]string{0: "10", 3: "7.5", 6: "5", 9: "2.5", 12: "0"}},
		{"halves below the first of 8 rows", 9, map[int]string{0: "", 1: "10", 4: "5", 7: "0"}},
		{"fifths on 11 rows", 12, map[int]string{0: "10", 2: "8", 4: "6", 6: "4", 8: "2", 10: "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := rasterChart(steady(5, 0, 10))
			c.Height = tc.height
			lines := c.Render()
			for row, want := range tc.want {
				if got := label(lines[row]); got != want {
					t.Errorf("row %d: label %q, want %q", row, got, want)
				}
			}
		})
	}
}

// TestCenteredTimeTicks: a Centered chart labels round times, each
// centered on its time, and leaves out the edges.
func TestCenteredTimeTicks(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(5, 0, 10))
	c.Width = 60
	axis := stripANSI(c.Render()[c.Height-1])
	for _, want := range []string{"10:02", "10:04", "10:06", "10:08"} {
		if !strings.Contains(axis, want) {
			t.Errorf("the time axis %q has no %s", axis, want)
		}
	}
	if strings.Contains(axis, "10:00") || strings.Contains(axis, "10:10") {
		t.Errorf("the time axis %q labels an edge it cannot center on", axis)
	}
	cols, _ := c.PlotSize()
	plot := []rune(axis)[len([]rune(axis))-cols:]
	for at := range plot {
		if strings.HasPrefix(string(plot[at:]), "10:04") {
			if mid := float64(at) + 2.5; math.Abs(mid-0.4*float64(cols)) > 1 {
				t.Errorf("10:04 is centered at cell %.1f, want %.1f", mid, 0.4*float64(cols))
			}
		}
	}
}

// TestCenteredGapWordsLeaveTheGrid: in a Centered chart the words "no
// data" move off a row that carries a value label and its grid line.
func TestCenteredGapWordsLeaveTheGrid(t *testing.T) {
	t.Parallel()
	c := rasterChart(steady(5, 8, 10))
	c.Width = 40
	lines := c.Render() // 5 rows: labels on rows 0, 2 and 4
	if got := stripANSI(lines[2]); !strings.Contains(got, "5│") || strings.Contains(got, "no data") {
		t.Errorf("the label row is %q", got)
	}
	if !strings.Contains(stripANSI(lines[1]), "no data") {
		t.Errorf("no words on the row above it: %q", stripANSI(lines[1]))
	}
}
