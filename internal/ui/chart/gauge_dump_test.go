package chart_test

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/stokaro/kc/internal/ui/chart"
)

// panelBlue is kc's panel color in the dumps: xterm color 18, as the
// chart dumps use.
var panelBlue = color.RGBA{0x00, 0x00, 0x87, 0xff}

type gaugeLook struct {
	name  string
	g     chart.Gauge
	label string
	tail  string
}

func gaugeLooks() []gaugeLook {
	req := func(p float64) chart.Mark { return chart.Mark{Percent: p, Color: chart.Yellow} }
	lim := func(p float64) chart.Mark { return chart.Mark{Percent: p, Color: chart.Red} }
	return []gaugeLook{
		{name: "5", g: chart.Gauge{Percent: 5, Color: chart.Green, Marks: []chart.Mark{req(12), lim(11)}}, label: " CPU     ", tail: "   5%"},
		{name: "57", g: chart.Gauge{Percent: 57, Color: chart.Green}, label: " Disk    ", tail: "  57%"},
		{name: "85-warn", g: chart.Gauge{Percent: 85, Color: chart.Orange, Marks: []chart.Mark{req(40), lim(116)}}, label: " Memory  ", tail: "  85% high ! lim"},
		{name: "over-limit", g: chart.Gauge{Percent: 130, Color: chart.Red, Marks: []chart.Mark{req(50), lim(100)}}, label: " Memory  ", tail: " 130% near-limit"},
		{name: "zero", g: chart.Gauge{Percent: 0, Color: chart.Green}, label: " Pods    ", tail: "   0%"},
	}
}

// TestGaugeLooks draws the gauge datasets and checks the fill ends where
// the percentage says; with -dump it writes each, between its label and
// its percentage on a kc panel, to a PNG file.
func TestGaugeLooks(t *testing.T) {
	t.Parallel()
	for _, lk := range gaugeLooks() {
		for _, cols := range []int{10, 30} {
			img := lk.g.Raster(cols, 1, dumpCellW, dumpCellH)
			if img == nil || img.Bounds().Dx() != cols*dumpCellW || img.Bounds().Dy() != dumpCellH {
				t.Fatalf("%s: no picture of %d cells", lk.name, cols)
			}
			if *dumpDir != "" {
				writeLineDump(t, filepath.Join(*dumpDir, "gauge-"+lk.name+"-"+strconv.Itoa(cols)+".png"), lk.label, img, lk.tail)
			}
		}
	}
}

func TestSparkLooks(t *testing.T) {
	t.Parallel()
	end := time.Date(2026, 11, 24, 10, 15, 0, 0, time.UTC)
	every := func(n int, step time.Duration, f func(i int) float64) []chart.Point {
		out := make([]chart.Point, n)
		for i := range out {
			out[i] = chart.Point{At: end.Add(-time.Duration(n-1-i) * step), V: f(i)}
		}
		return out
	}
	looks := []struct {
		name string
		s    chart.Spark
	}{
		{"two", chart.Spark{Start: end.Add(-45 * time.Second), End: end, Points: every(2, 15*time.Second, func(i int) float64 { return []float64{4, 6}[i] }), Top: 10, Gap: 38 * time.Second, Color: chart.Green}},
		{"bursty", chart.Spark{Start: end.Add(-15 * time.Minute), End: end, Points: every(88, 10*time.Second, func(i int) float64 {
			v := 3 + 1.5*math.Sin(float64(i)/5)
			if i%17 == 8 || i%17 == 9 {
				v += 5
			}
			return v
		}), Top: 10, Gap: 38 * time.Second, Color: chart.Green}},
		{"flat", chart.Spark{Start: end.Add(-15 * time.Minute), End: end, Points: every(60, 15*time.Second, func(int) float64 { return 12 }), Top: 25, Gap: 38 * time.Second, Color: chart.Orange}},
	}
	for _, lk := range looks {
		for _, rows := range []int{1, 2} {
			cols := 30
			img := lk.s.Raster(cols, rows, dumpCellW, dumpCellH)
			if img == nil {
				t.Fatalf("%s: no picture", lk.name)
			}
			if *dumpDir != "" {
				writeLineDump(t, filepath.Join(*dumpDir, "spark-"+lk.name+"-"+strconv.Itoa(rows)+".png"), "        ", img, " 15m, top 10%")
			}
		}
	}
}

// writeLineDump writes label, the picture and tail as one panel line of
// text cells on kc's blue.
func writeLineDump(t *testing.T, path, label string, img *image.RGBA, tail string) {
	t.Helper()
	f, err := opentype.Parse(gomono.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(dumpCellH) * 0.7, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	cw, ch := dumpCellW, dumpCellH
	labelCols := len([]rune(label))
	cols := labelCols + img.Bounds().Dx()/cw + len([]rune(tail)) + 1
	rows := img.Bounds().Dy()/ch + 2
	out := image.NewRGBA(image.Rect(0, 0, cols*cw, rows*ch))
	draw.Draw(out, out.Bounds(), image.NewUniform(panelBlue), image.Point{}, draw.Src)
	top := ch
	at := image.Rect(labelCols*cw, top, labelCols*cw+img.Bounds().Dx(), top+img.Bounds().Dy())
	draw.Draw(out, at, img, image.Point{}, draw.Over)
	d := &font.Drawer{Dst: out, Src: image.NewUniform(color.RGBA{0xd0, 0xd0, 0xd0, 0xff}), Face: face}
	baseline := top + img.Bounds().Dy() - ch/4
	d.Dot = fixed.P(1, baseline)
	d.DrawString(label)
	d.Dot = fixed.P(at.Max.X+1, top+ch*3/4)
	d.DrawString(tail)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := png.Encode(w, out); err != nil {
		t.Fatal(err)
	}
}
