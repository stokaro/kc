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

// at is a sample time for rasterChart: minutes and seconds after its
// start.
func at(m, sec int) time.Time {
	return time.Time{}.Add(time.Duration(m)*time.Minute + time.Duration(sec)*time.Second)
}

// greenRows counts the rows of column x drawn in nearly the full usage
// color: the line, or a point, but not the faded area under them.
func greenRows(img *image.RGBA, x int) int {
	n := 0
	for y := range img.Bounds().Dy() {
		if greenness(img.RGBAAt(x, y)) > greenness(grafanaGreen)*3/4 {
			n++
		}
	}
	return n
}

// plotted reports whether column x has anything green in it: the line,
// a point or the area.
func plotted(img *image.RGBA, x int) bool {
	for y := range img.Bounds().Dy() {
		if got := img.RGBAAt(x, y); got.G > got.R+8 {
			return true
		}
	}
	return false
}

// TestRasterHoldsSamples: as in the text plot, a sample stands for Hold
// after it. The line runs on flat from the newest sample to the end of a
// live chart when that sample is fresh, and stops Hold after a sample
// with nothing after it, so a real gap stays empty.
func TestRasterHoldsSamples(t *testing.T) {
	t.Parallel()
	every := func(from, to time.Time) []chart.Point {
		var out []chart.Point
		for t := from; !t.After(to); t = t.Add(time.Minute) {
			out = append(out, chart.Point{At: t, V: 5})
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		points []chart.Point
		hold   time.Duration
		// line and empty are times whose pixel column has the line, and
		// nothing; end is the last column of the plot.
		line, empty []time.Time
		end         bool
	}{
		{
			name:   "a fresh newest sample runs on to the end",
			points: every(at(0, 40), at(9, 40)),
			hold:   90 * time.Second,
			line:   []time.Time{at(9, 50)},
			end:    true,
		},
		{
			name:   "a stale newest sample stops after its hold",
			points: every(at(0, 0), at(6, 0)),
			hold:   90 * time.Second,
			line:   []time.Time{at(7, 0)},
			empty:  []time.Time{at(8, 0), at(9, 30)},
		},
		{
			name:   "the last sample before a gap holds",
			points: append(every(at(0, 0), at(3, 0)), every(at(8, 0), at(10, 0))...),
			hold:   90 * time.Second,
			line:   []time.Time{at(4, 0), at(9, 0)},
			empty:  []time.Time{at(5, 30)},
			end:    true,
		},
		{
			name:   "a sample before the range holds into it",
			points: append([]chart.Point{{At: at(0, -30), V: 5}}, every(at(5, 0), at(10, 0))...),
			hold:   90 * time.Second,
			line:   []time.Time{at(0, 30)},
			empty:  []time.Time{at(2, 0)},
			end:    true,
		},
		{
			name:   "no hold, no tail",
			points: []chart.Point{{At: at(5, 0), V: 5}},
			line:   []time.Time{at(5, 0)},
			empty:  []time.Time{at(5, 30)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := rasterChart(tc.points)
			c.Hold = tc.hold
			img := c.Raster(6, 10)
			w := img.Bounds().Dx()
			col := func(at time.Time) int {
				return int(float64(c.Start.Add(at.Sub(time.Time{})).Sub(c.Start)) / float64(c.End.Sub(c.Start)) * float64(w))
			}
			for _, at := range tc.line {
				if x := col(at); lineTop(img, x) < 0 {
					t.Errorf("no line at %s (x=%d)", at.Sub(time.Time{}), x)
				}
			}
			for _, at := range tc.empty {
				if x := col(at); plotted(img, x) {
					t.Errorf("the gap at %s (x=%d) is drawn", at.Sub(time.Time{}), x)
				}
			}
			if got := lineTop(img, w-1) >= 0; got != tc.end {
				t.Errorf("line in the last column: %v, want %v", got, tc.end)
			}
		})
	}
}

// TestRasterHoldIsNoSample: the held part of a line is drawn like the
// line, with the area under it, but gets no point: the points stay at
// the real sample times.
func TestRasterHoldIsNoSample(t *testing.T) {
	t.Parallel()
	// Three samples are few, so each is drawn as a point too; the end of
	// the hold does not make them four.
	c := rasterChart([]chart.Point{{At: at(2, 0), V: 5}, {At: at(3, 0), V: 5}, {At: at(4, 0), V: 5}})
	img := c.Raster(6, 10)
	w := img.Bounds().Dx()
	// 4:00, and 4:42 within the hold; off the grid line at 5:00.
	sample, held := 4*w/10, 47*w/100
	// The hold ends at 5:30, without a point there either.
	end := int(0.55*float64(w)) - 1
	for _, x := range []int{held, end} {
		if s, h := greenRows(img, sample), greenRows(img, x); h == 0 || h >= s {
			t.Errorf("x=%d: the hold has %d rows of line, the sample %d: want a thinner line and no point", x, h, s)
		}
	}
	top := lineTop(img, held)
	if top < 0 || greenness(img.RGBAAt(held, top+4)) <= 0 {
		t.Error("no area under the hold")
	}
	// Flat at the value of the sample, as the line between the samples.
	later, between := lineTop(img, 53*w/100), lineTop(img, 35*w/100)
	if later != top || top < between-1 || top > between+1 {
		t.Errorf("the hold is at rows %d and %d, the line between the samples at row %d: want it flat at the same value", top, later, between)
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
	// A series holds its newest sample as the usage does: a pod measured
	// 20 seconds ago reaches the end of the plot.
	var fresh []chart.Point
	for m := range 10 {
		fresh = append(fresh, chart.Point{At: at(m, 40), V: 3})
	}
	c.Series[0].Points = rasterChart(fresh).Points
	img = c.Raster(12, 36)
	last := img.Bounds().Dx() - 1
	found = false
	for y := range img.Bounds().Dy() {
		if img.RGBAAt(last, y) == purple {
			found = true
		}
	}
	if !found {
		t.Error("the series stops before the end of the plot")
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
