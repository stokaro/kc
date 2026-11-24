package chart_test

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/ui/chart"
)

// The cell of the tests: iTerm2's at 2x, as the dumps use.
const (
	gaugeCols = 20
	testCellW = 16
	testCellH = 34
	// gaugeMid is the row of pixels in the middle of the track.
	gaugeMid = testCellH / 2
)

func near(a, b color.RGBA, tol int) bool {
	d := func(x, y uint8) bool { return math.Abs(float64(x)-float64(y)) <= float64(tol) }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && d(a.A, b.A)
}

func pixel(img *image.RGBA, x, y int) color.RGBA { return img.RGBAAt(x, y) }

func TestGaugeFill(t *testing.T) {
	t.Parallel()
	w := gaugeCols * testCellW
	for _, tc := range []struct {
		name    string
		percent float64
		want    int
	}{
		{"empty", 0, 0},
		{"half", 50, w / 2},
		{"full", 100, w},
		{"over the limit fills the track", 130, w},
		{"below zero", -5, 0},
		{"unknown", math.NaN(), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := chart.Gauge{Percent: tc.percent, Color: chart.Green}
			if got := g.GaugeFill(gaugeCols, testCellW, testCellH); got != tc.want {
				t.Errorf("fill ends at %d px, want %d", got, tc.want)
			}
		})
	}
}

// TestGaugeKey: the key changes with what the picture shows, and stays
// for a change of the percentage smaller than a pixel.
func TestGaugeKey(t *testing.T) {
	t.Parallel()
	base := chart.Gauge{Percent: 50, Color: chart.Green, Marks: []chart.Mark{{Percent: 75, Color: chart.Yellow}}}
	key := func(g chart.Gauge) uint64 { return g.RasterKey(gaugeCols, 1, testCellW, testCellH) }
	k := key(base)
	same := base
	same.Percent += 0.05 // a pixel is 100/320 = 0.31%
	if key(same) != k {
		t.Error("a change below a pixel changed the key")
	}
	for name, change := range map[string]func(*chart.Gauge){
		"a pixel more":   func(g *chart.Gauge) { g.Percent++ },
		"another level":  func(g *chart.Gauge) { g.Color = chart.Orange },
		"a moved mark":   func(g *chart.Gauge) { g.Marks = []chart.Mark{{Percent: 80, Color: chart.Yellow}} },
		"a mark above":   func(g *chart.Gauge) { g.Marks = []chart.Mark{{Percent: 120, Color: chart.Yellow}} },
		"another mark":   func(g *chart.Gauge) { g.Marks = append(g.Marks, chart.Mark{Percent: 90, Color: chart.Red}) },
		"no marks":       func(g *chart.Gauge) { g.Marks = nil },
		"a marks' color": func(g *chart.Gauge) { g.Marks = []chart.Mark{{Percent: 75, Color: chart.Red}} },
	} {
		g := base
		g.Marks = append([]chart.Mark(nil), base.Marks...)
		change(&g)
		if key(g) == k {
			t.Errorf("%s kept the key", name)
		}
	}
	if base.RasterKey(gaugeCols+1, 1, testCellW, testCellH) == k || base.RasterKey(gaugeCols, 1, testCellW, testCellH+2) == k {
		t.Error("another size kept the key")
	}
}

// TestGaugePicture: a transparent picture but for the track, a fill in
// the level color that ends in a solid edge, the dark track after it, and
// a mark that sticks out of the track.
func TestGaugePicture(t *testing.T) {
	t.Parallel()
	g := chart.Gauge{Percent: 50, Color: chart.Green, Marks: []chart.Mark{{Percent: 75, Color: chart.Yellow}}}
	img := g.Raster(gaugeCols, 1, testCellW, testCellH)
	w := gaugeCols * testCellW
	if b := img.Bounds(); b.Dx() != w || b.Dy() != testCellH {
		t.Fatalf("picture is %v", b)
	}
	if c := pixel(img, 100, 1); c.A != 0 {
		t.Errorf("above the track %v, want transparent", c)
	}
	if c := pixel(img, 100, testCellH-1); c.A != 0 {
		t.Errorf("below the track %v, want transparent", c)
	}
	if c := pixel(img, w/2-2, gaugeMid); !near(c, chart.Green, 2) {
		t.Errorf("end of the fill %v, want the level color %v", c, chart.Green)
	}
	start := pixel(img, 20, gaugeMid)
	if start.A != 0xff || start.G <= start.R || start.G <= start.B || near(start, chart.Green, 8) {
		t.Errorf("start of the fill %v, want a green fainter than the end", start)
	}
	if c := pixel(img, w/2+20, gaugeMid); !near(c, chart.TrackColor, 1) {
		t.Errorf("past the fill %v, want the track %v", c, chart.TrackColor)
	}
	mark := w * 3 / 4
	if c := pixel(img, mark, gaugeMid); !near(c, chart.Yellow, 2) {
		t.Errorf("mark %v, want %v", c, chart.Yellow)
	}
	if c := pixel(img, mark, 5); !near(c, chart.Yellow, 2) {
		t.Errorf("mark above the track %v, want %v", c, chart.Yellow)
	}
	if c := pixel(img, mark-12, 5); c.A != 0 {
		t.Errorf("beside the mark above the track %v, want transparent", c)
	}
}

// TestGaugeOverTheScale: a mark above 100% is an arrow at the end of the
// track, and marks at the ends stay whole inside the picture.
func TestGaugeOverTheScale(t *testing.T) {
	t.Parallel()
	w := gaugeCols * testCellW
	over := chart.Gauge{Percent: 130, Color: chart.Red, Marks: []chart.Mark{{Percent: 116, Color: chart.Yellow}}}
	img := over.Raster(gaugeCols, 1, testCellW, testCellH)
	if c := pixel(img, w-6, gaugeMid); !near(c, chart.Yellow, 2) {
		t.Errorf("arrow tip %v, want %v", c, chart.Yellow)
	}
	ends := chart.Gauge{Percent: 0, Color: chart.Green, Marks: []chart.Mark{{Percent: 0, Color: chart.Yellow}, {Percent: 100, Color: chart.Red}}}
	img = ends.Raster(gaugeCols, 1, testCellW, testCellH)
	if c := pixel(img, 0, gaugeMid); !near(c, chart.Yellow, 2) {
		t.Errorf("mark at 0%% %v, want %v at the left edge", c, chart.Yellow)
	}
	if c := pixel(img, w-1, gaugeMid); !near(c, chart.Red, 2) {
		t.Errorf("mark at 100%% %v, want %v at the right edge", c, chart.Red)
	}
}

// TestGaugeOnTwoRows: the track is in the middle of the last row, in line
// with the text after it.
func TestGaugeOnTwoRows(t *testing.T) {
	t.Parallel()
	g := chart.Gauge{Percent: 100, Color: chart.Green}
	img := g.Raster(gaugeCols, 2, testCellW, testCellH)
	if c := pixel(img, 100, gaugeMid); c.A != 0 {
		t.Errorf("first row %v, want transparent", c)
	}
	if c := pixel(img, 100, testCellH+gaugeMid); c.A != 0xff {
		t.Errorf("middle of the last row %v, want the track", c)
	}
	if g.Raster(0, 1, testCellW, testCellH) != nil {
		t.Error("an empty size has a picture")
	}
}

func sparkAt(points ...float64) chart.Spark {
	start := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	s := chart.Spark{Start: start, End: start.Add(15 * time.Minute), Top: 10, Gap: 45 * time.Second, Color: chart.Green}
	for i, v := range points {
		if math.IsNaN(v) {
			continue
		}
		s.Points = append(s.Points, chart.Point{At: start.Add(time.Duration(i) * time.Minute), V: v})
	}
	return s
}

// greenColumn reports whether column x of a sparkline has a pixel of the
// line's color.
func greenColumn(img *image.RGBA, x int) bool {
	for y := range img.Bounds().Dy() {
		if near(pixel(img, x, y), chart.Green, 24) {
			return true
		}
	}
	return false
}

// TestSparkPicture: a rounded dark panel with transparent corners, the
// line where there are samples, nothing in a gap, and a sample above the
// top cut at the top inside the panel.
func TestSparkPicture(t *testing.T) {
	t.Parallel()
	const cols = 30
	nan := math.NaN()
	// Samples a minute apart from 0 to 4 and from 10 to 15; Gap is 45s,
	// so each is a dot.
	pts := []float64{5, 5, 5, 5, 5, nan, nan, nan, nan, nan, 50, 5, 5, 5, 5, 5}
	s := sparkAt(pts...)
	s.Gap = 90 * time.Second
	img := s.Raster(cols, 2, testCellW, testCellH)
	w, h := cols*testCellW, 2*testCellH
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("picture is %v", b)
	}
	if c := pixel(img, 0, 0); c.A != 0 {
		t.Errorf("corner %v, want transparent", c)
	}
	if c := pixel(img, w/2, 4); !near(c, chart.TrackColor, 1) {
		t.Errorf("panel %v, want %v", c, chart.TrackColor)
	}
	minute := float64(w) / 15
	if !greenColumn(img, int(2*minute)) {
		t.Error("no line at minute 2")
	}
	if greenColumn(img, int(7*minute)) {
		t.Error("a line in the gap at minute 7")
	}
	// The sample at 50 is cut at the top: the line reaches the top
	// padding and no further.
	top := -1
	for y := range h {
		if near(pixel(img, int(10*minute), y), chart.Green, 24) {
			top = y
			break
		}
	}
	if top < 2 || top > 10 {
		t.Errorf("a sample above the top is drawn from y %d", top)
	}
	if s.Raster(cols, 2, testCellW, testCellH) == nil || (chart.Spark{}).Raster(cols, 2, testCellW, testCellH) != nil {
		t.Error("an empty range has a picture, or a range has none")
	}
}

func TestSparkKey(t *testing.T) {
	t.Parallel()
	s := sparkAt(1, 2, 3)
	key := func(s chart.Spark) uint64 { return s.RasterKey(20, 2, testCellW, testCellH) }
	k := key(s)
	if key(sparkAt(1, 2, 3)) != k {
		t.Error("the same sparkline has another key")
	}
	more := sparkAt(1, 2, 3, 4)
	later := s
	later.Start, later.End = s.Start.Add(time.Minute), s.End.Add(time.Minute)
	scaled := s
	scaled.Top = 100
	for name, other := range map[string]chart.Spark{"a new sample": more, "a later window": later, "another top": scaled} {
		if key(other) == k {
			t.Errorf("%s kept the key", name)
		}
	}
}
