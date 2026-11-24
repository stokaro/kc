package chart_test

import (
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/stokaro/kc/internal/ui/chart"
)

// dumpDir makes TestRasterLooks write each picture, with the chart's text
// labels around it as a terminal shows them, to a PNG file:
//
//	go test ./internal/ui/chart -run TestRasterLooks -args -dump /tmp/charts
var dumpDir = flag.String("dump", "", "write the pictures of TestRasterLooks to this directory as PNG files")

// Cell size of the dumps: a Retina terminal at about 14 points.
const dumpCellW, dumpCellH = 16, 34

type look struct {
	name string
	c    chart.Chart
	// cellW and cellH override the cell size of the dump.
	cellW, cellH int
}

func looks() []look {
	start := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return start.Add(time.Duration(sec) * time.Second) }
	base := func(pts []chart.Point) chart.Chart {
		return chart.Chart{
			Start: start, End: start.Add(15 * time.Minute), Points: pts, Hold: 75 * time.Second,
			Width: 110, Height: 14, Mode: chart.Blocks, Cursor: -1, Centered: true,
			Style:    lipgloss.NewStyle().Foreground(lipgloss.BrightGreen),
			GapStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("#8e8e8e")),
		}
	}
	req := func(v float64) chart.Ref {
		return chart.Ref{V: v, Style: lipgloss.NewStyle().Foreground(lipgloss.BrightYellow)}
	}
	lim := func(v float64) chart.Ref {
		return chart.Ref{V: v, Band: true, Style: lipgloss.NewStyle().Foreground(lipgloss.BrightRed)}
	}
	every := func(n, stepSec int, f func(i int) float64) []chart.Point {
		out := make([]chart.Point, n)
		for i := range out {
			out[i] = chart.Point{At: at(900 - (n-1-i)*stepSec), V: f(i)}
		}
		return out
	}
	burst := func(i int) float64 {
		v := 0.08 + 0.02*math.Sin(float64(i)/2)
		if i%9 == 4 || i%9 == 5 {
			v += 0.22
		}
		return v
	}

	// The first two samples of a session: the request and the limit are
	// known only where the samples are, as the session records them.
	two := base(every(2, 30, func(i int) float64 { return []float64{0.12, 0.18}[i] }))
	two.Refs = []chart.Ref{lim(0.5), req(0.25)}
	for i := range two.Refs {
		two.Refs[i].Points = every(2, 30, func(int) float64 { return two.Refs[i].V })
		two.Refs[i].Hold = two.Hold
	}

	bursts := base(every(30, 30, burst))
	bursts.Refs = []chart.Ref{lim(0.5), req(0.25)}

	zero := base(every(30, 30, func(int) float64 { return 0 }))

	over := base(every(30, 30, func(i int) float64 { return 0.3 + 0.25*math.Max(0, math.Sin(float64(i)/4)) }))
	over.Refs = []chart.Ref{lim(0.4), req(0.2)}

	paused := base(every(30, 30, burst))
	paused.Refs = []chart.Ref{lim(0.5), req(0.25)}
	paused.Cursor = 70
	paused.CursorLine = lipgloss.NewStyle().Foreground(lipgloss.Color("#b4bac4"))

	dense := base(every(1800, 1, func(i int) float64 {
		return 0.2 + 0.1*math.Sin(float64(i)/60) + 0.05*math.Sin(float64(i)/7)
	}))
	dense.Refs = []chart.Ref{req(0.25)}

	// Four pods of a workload: the chart's usage is their sum.
	podColors := []string{"#8AB8FF", "#FF9830", "#B877D9", "#5794F2"}
	var pods []chart.Series
	sum := make([]float64, 30)
	for p, col := range podColors {
		pts := every(30, 30, func(i int) float64 {
			v := 0.04 + 0.015*float64(p) + 0.01*math.Sin(float64(i+3*p)/3)
			if p == 1 && i >= 12 && i < 20 {
				v += 0.18 // one pod takes a burst
			}
			sum[i] += v
			return v
		})
		pods = append(pods, chart.Series{Points: pts, Style: lipgloss.NewStyle().Foreground(lipgloss.Color(col))})
	}
	many := base(every(30, 30, func(i int) float64 { return sum[i] }))
	many.Series = pods
	many.Refs = []chart.Ref{lim(0.6), req(0.4)}

	lowDPI := base(every(30, 30, burst))
	lowDPI.Refs = []chart.Ref{lim(0.5), req(0.25)}

	// A live chart as the Metrics API fills it: the newest measurement is
	// 25 seconds old, and the request and the limit are known at the
	// samples only, as the session records them. The samples hold to the
	// end of the plot.
	ago := func(pts []chart.Point, sec int) []chart.Point {
		out := make([]chart.Point, len(pts))
		for i, p := range pts {
			out[i] = chart.Point{At: p.At.Add(-time.Duration(sec) * time.Second), V: p.V}
		}
		return out
	}
	sessionRefs := func(c *chart.Chart, refs ...chart.Ref) {
		c.Refs = refs
		for i := range c.Refs {
			c.Refs[i].Points = make([]chart.Point, len(c.Points))
			for j, p := range c.Points {
				c.Refs[i].Points[j] = chart.Point{At: p.At, V: c.Refs[i].V}
			}
			c.Refs[i].Hold = c.Hold
		}
	}
	live := base(ago(every(30, 30, burst), 25))
	sessionRefs(&live, lim(0.5), req(0.25))

	// The pods above, measured 25 seconds ago: each pod's line holds to
	// the end of the plot with the sum.
	livePods := many
	livePods.Points = ago(many.Points, 25)
	livePods.Series = nil
	for _, sr := range many.Series {
		livePods.Series = append(livePods.Series, chart.Series{Points: ago(sr.Points, 25), Style: sr.Style})
	}

	// Measurements that stopped four minutes ago: the newest sample holds
	// for Hold, then the plot is empty.
	stale := base(ago(every(22, 30, burst), 240))
	sessionRefs(&stale, lim(0.5), req(0.25))

	return []look{
		{name: "two-samples", c: two},
		{name: "bursts", c: bursts},
		{name: "flat-zero", c: zero},
		{name: "over-limit", c: over},
		{name: "cursor", c: paused},
		{name: "dense", c: dense},
		{name: "pods", c: many},
		{name: "low-dpi", c: lowDPI, cellW: 8, cellH: 17},
		{name: "live-end", c: live},
		{name: "live-pods", c: livePods},
		{name: "stale-end", c: stale},
	}
}

// TestRasterLooks draws the dump datasets and checks each picture has the
// background, the grid and the usage line in it.
func TestRasterLooks(t *testing.T) {
	t.Parallel()
	use := color.RGBA{0x73, 0xbf, 0x69, 0xff}
	for _, lk := range looks() {
		t.Run(lk.name, func(t *testing.T) {
			t.Parallel()
			cw, ch := dumpCellW, dumpCellH
			if lk.cellW > 0 {
				cw, ch = lk.cellW, lk.cellH
			}
			img := lk.c.Raster(cw, ch)
			cols, rows := lk.c.PlotSize()
			if img == nil || img.Bounds() != image.Rect(0, 0, cols*cw, rows*ch) {
				t.Fatalf("no picture of %dx%d cells", cols, rows)
			}
			// Under the zero line is the panel's margin.
			if got := img.RGBAAt(1, img.Bounds().Dy()-2); got != chart.Background {
				t.Errorf("the bottom corner is %v, want the background", got)
			}
			if !hasColor(img, use) {
				t.Error("the usage line is not drawn")
			}
			if *dumpDir != "" {
				writeDump(t, filepath.Join(*dumpDir, lk.name+".png"), lk.c, img, cw, ch)
			}
		})
	}
}

func hasColor(img *image.RGBA, c color.RGBA) bool {
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			if img.RGBAAt(x, y) == c {
				return true
			}
		}
	}
	return false
}

// writeDump writes the picture inside the chart's text lines, drawn on a
// kc panel background.
func writeDump(t *testing.T, path string, c chart.Chart, img *image.RGBA, cw, ch int) {
	t.Helper()
	f, err := opentype.Parse(gomono.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(ch) * 0.7, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	lines := c.Render()
	cols, rows := c.PlotSize()
	out := image.NewRGBA(image.Rect(0, 0, c.Width*cw, c.Height*ch))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.RGBA{0x00, 0x00, 0x87, 0xff}), image.Point{}, draw.Src)
	plotX := (c.Width - cols) * cw
	draw.Draw(out, image.Rect(plotX, 0, plotX+img.Bounds().Dx(), img.Bounds().Dy()), img, image.Point{}, draw.Src)
	text := c.TextCells()
	d := &font.Drawer{Dst: out, Src: image.NewUniform(color.RGBA{0xd0, 0xd0, 0xd0, 0xff}), Face: face}
	for row, line := range lines {
		for x, r := range []rune(ansi.Strip(line)) {
			px := x - (c.Width - cols)
			inPlot := row < rows && px >= 0
			if r == ' ' || (inPlot && !text[row][px]) {
				continue
			}
			cell := image.Rect(x*cw, row*ch, (x+1)*cw, (row+1)*ch)
			if inPlot {
				// A text cell over the image is the terminal's own cell.
				draw.Draw(out, cell, image.NewUniform(chart.Background), image.Point{}, draw.Src)
			}
			if r == '│' || r == '└' {
				// Terminals draw box lines across the whole cell.
				draw.Draw(out, image.Rect(cell.Min.X+cw/2-1, cell.Min.Y, cell.Min.X+cw/2+1, cell.Max.Y), d.Src, image.Point{}, draw.Src)
				if r == '│' {
					continue
				}
			}
			d.Dot = fixed.P(x*cw+1, row*ch+ch*3/4)
			d.DrawString(string(r))
		}
	}
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

// BenchmarkRasterPicture is the cost of one picture of a 1600x612 plot:
// the raster and its PNG, as a cursor drawn into the picture would cost
// per move: the dashboard keeps the cursor as text for that reason.
func BenchmarkRasterPicture(b *testing.B) {
	var c chart.Chart
	for _, lk := range looks() {
		if lk.name == "cursor" {
			c = lk.c
		}
	}
	c.Width, c.Height = 105, 19 // 100 by 18 cells of 16x34
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	b.ReportAllocs()
	var out countWriter
	for b.Loop() {
		img := c.Raster(dumpCellW, dumpCellH)
		if err := enc.Encode(&out, img); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(out)/float64(b.N), "png-bytes/op")
}

type countWriter int

func (w *countWriter) Write(p []byte) (int, error) {
	*w += countWriter(len(p))
	return len(p), nil
}
