package info

import (
	"flag"
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

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/kittygfx"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// dumpDir makes TestInfoPanelLooks write the panel, its text and its
// pictures as a terminal shows them, to PNG files:
//
//	go test ./internal/ui/panelcontent/info -run TestInfoPanelLooks -args -dump /tmp/info
var dumpDir = flag.String("dump", "", "write the panels of TestInfoPanelLooks to this directory as PNG files")

// panelLook is a summary and a history to draw.
type panelLook struct {
	name    string
	summary models.InfoSummary
	history func(h *harness)
}

func mark(kind models.InfoMarkKind, p float64) models.InfoMark {
	return models.InfoMark{Kind: kind, Percent: p}
}

func panelLooks() []panelLook {
	bursty := func(i int) float64 {
		v := 3 + 1.5*math.Sin(float64(i)/5)
		if i%17 == 8 || i%17 == 9 {
			v += 5
		}
		return v
	}
	top := []models.InfoConsumer{
		{Namespace: "kc-gfx-load", Name: "burner-7d9c5b8f6-x2kqp", CPU: "512m", Memory: "64Mi"},
		{Namespace: "kc-gfx-load", Name: "steady-5f4d8b7c9-lm4vn", CPU: "120m", Memory: "48Mi"},
		{Namespace: "kc-gfx-load", Name: "spiky-6c8f9d7b5-qw7rt", CPU: "35m", Memory: "21Mi"},
	}
	cluster := models.InfoSummary{
		UsageAt: harnessNow.Add(-12 * time.Second), UsageWindow: 15 * time.Second, UsageReadAt: harnessNow.Add(-2 * time.Second),
		BarsTitle: "Cluster capacity (all nodes, all namespaces)",
		Bars: []models.InfoBar{
			{
				Label: "CPU", Known: true, Usage: true, Base: "allocatable", Percent: 5, Flag: "ok", Health: models.RowHealthOK,
				Detail: "pods 0.7/12.0 cores allocatable, 5%, 19 of 21 pods measured", More: []string{"node 1.9/12.0 cores capacity, 15%", "req 12%, lim 11%"},
				MoreHealth: []models.RowHealth{models.RowHealthOK, models.RowHealthOK},
				Marks:      []models.InfoMark{mark(models.InfoMarkRequest, 12), mark(models.InfoMarkLimit, 11)},
			},
			{
				Label: "Memory", Known: true, Usage: true, Base: "allocatable", Percent: 9, Flag: "ok", Health: models.RowHealthOK,
				Detail: "pods 1.4/15.6 Gi allocatable, 9%", More: []string{"node 4.1/15.6 Gi capacity, 26%", "req 6%, lim 14%"},
				MoreHealth: []models.RowHealth{models.RowHealthOK, models.RowHealthOK},
				Marks:      []models.InfoMark{mark(models.InfoMarkRequest, 6), mark(models.InfoMarkLimit, 14)},
			},
			{Label: "Pods", Known: true, Percent: 9, Detail: "21/220", Flag: "ok", Health: models.RowHealthOK},
			{Label: "Disk", Known: true, Percent: 57, Detail: "341/589 Gi", Flag: "ok", Health: models.RowHealthOK},
		},
		TopTitle: "Top pods by memory in namespace kc-gfx-load", TopNamespace: "kc-gfx-load", Top: top,
	}
	levels := cluster
	levels.Bars = []models.InfoBar{
		{
			Label: "CPU", Known: true, Usage: true, Base: "allocatable", Percent: 85, Flag: "high", Health: models.RowHealthNotReady,
			Detail: "pods 10.2/12.0 cores allocatable, 85% (high)", More: []string{"node 11.0/12.0 cores capacity, 91% (high)", "req 40%, lim 116% (overcommitted)"},
			MoreHealth: []models.RowHealth{models.RowHealthNotReady, models.RowHealthNotReady},
			Attention:  models.RowHealthNotReady, AttentionText: "! lim",
			Marks: []models.InfoMark{mark(models.InfoMarkRequest, 40), mark(models.InfoMarkLimit, 116)},
		},
		{
			Label: "Memory", Known: true, Usage: true, Base: "allocatable", Percent: 97, Flag: "full", Health: models.RowHealthFailed,
			Detail: "pods 15.1/15.6 Gi allocatable, 97% (full)", More: []string{"node 15.4/15.6 Gi capacity, 98% (full)", "req 90% (high), lim 140% (overcommitted)"},
			MoreHealth: []models.RowHealth{models.RowHealthFailed, models.RowHealthNotReady},
			Attention:  models.RowHealthFailed,
			Marks:      []models.InfoMark{mark(models.InfoMarkRequest, 90), mark(models.InfoMarkLimit, 140)},
		},
		{Label: "Pods", Known: true, Percent: 57, Detail: "126/220", Flag: "ok", Health: models.RowHealthOK},
		{Label: "Disk", Known: true, Percent: 92, Detail: "542/589 Gi", Flag: "full", Health: models.RowHealthFailed},
	}
	workload := cluster
	workload.BarsTitle = ""
	workload.Workload = &models.InfoWorkload{Namespace: "kc-gfx-load", Name: "burner"}
	workload.Bars = []models.InfoBar{
		{
			Label: "CPU", Known: true, Usage: true, Base: "limits", Percent: 130, Flag: "near-limit", Health: models.RowHealthNotReady,
			Detail: "3 pods use 1.3 of lim 1.0, req 500m",
			Marks:  []models.InfoMark{mark(models.InfoMarkRequest, 50), mark(models.InfoMarkLimit, 100)},
		},
		{
			Label: "Memory", Known: true, Usage: true, Base: "limits", Percent: 5, Flag: "ok", Health: models.RowHealthOK,
			Detail: "3 pods use 25Mi of lim 512Mi, req 96Mi",
			Marks:  []models.InfoMark{mark(models.InfoMarkRequest, 19), mark(models.InfoMarkLimit, 100)},
		},
		{Label: "Pods", Known: true, Percent: 100, Detail: "3/3 ready, 3 pods", Flag: "ok", Health: models.RowHealthOK},
	}
	return []panelLook{
		{name: "cluster", summary: cluster, history: func(h *harness) {
			withHistory(h, 88, 10*time.Second, bursty, func(i int) float64 { return 8.6 + 0.3*math.Sin(float64(i)/9) })
		}},
		{name: "levels", summary: levels, history: func(h *harness) {
			withHistory(h, 2, 15*time.Second, func(i int) float64 { return []float64{82, 85}[i] }, func(int) float64 { return 97 })
		}},
		{name: "workload", summary: workload, history: func(h *harness) {
			withHistory(h, 40, 15*time.Second, func(i int) float64 { return 90 + 40*math.Abs(math.Sin(float64(i)/6)) }, func(int) float64 { return 5 })
		}},
	}
}

// TestInfoPanelLooks draws each look at the width of a 25% and of a 50%
// panel through the fake terminal: every picture the screen shows is one
// the terminal holds. With -dump it writes each as a PNG file.
func TestInfoPanelLooks(t *testing.T) {
	t.Parallel()
	for _, lk := range panelLooks() {
		for _, width := range []int{35, 73} {
			t.Run(lk.name+"-"+strconv.Itoa(width), func(t *testing.T) {
				t.Parallel()
				h, ft := pictureHarness(t, lk.summary, width)
				lk.history(h)
				drive(t, h.w, ft, h.w.syncPictures())
				const height = 40
				lines := h.w.render(width, height, true)
				for id := range placeholderCells(t, lines, width) {
					if _, ok := ft.images[uint32(id)]; !ok {
						t.Errorf("image %d is on the screen but not on the terminal", id)
					}
				}
				if *dumpDir != "" {
					writePanel(t, filepath.Join(*dumpDir, lk.name+"-"+strconv.Itoa(width)+".png"), ft, h.w.FrameInfo(t.Context(), panelcontent.FrameInfoRequest{Width: width}).Breadcrumb, lines, width)
				}
			})
		}
	}
}

// The colors of the dumps: kc's panel blue as the chart dumps draw it,
// and iTerm2's default palette for the 16 colors.
var (
	dumpBlue  = color.RGBA{0x00, 0x00, 0x87, 0xff}
	dumpText  = color.RGBA{0xc7, 0xc7, 0xc7, 0xff}
	dumpBasic = [16]color.RGBA{
		{0x00, 0x00, 0x00, 0xff}, {0xc9, 0x1b, 0x00, 0xff}, {0x00, 0xc2, 0x00, 0xff}, {0xc7, 0xc4, 0x00, 0xff},
		{0x02, 0x25, 0xc7, 0xff}, {0xc9, 0x30, 0xc7, 0xff}, {0x00, 0xc5, 0xc7, 0xff}, {0xc7, 0xc7, 0xc7, 0xff},
		{0x67, 0x67, 0x67, 0xff}, {0xff, 0x6e, 0x67, 0xff}, {0x5f, 0xfa, 0x68, 0xff}, {0xff, 0xfc, 0x67, 0xff},
		{0x68, 0x71, 0xff, 0xff}, {0xff, 0x77, 0xff, 0xff}, {0x60, 0xfd, 0xff, 0xff}, {0xff, 0xff, 0xff, 0xff},
	}
)

func dumpColor(c color.Color, def color.RGBA) color.RGBA {
	switch c := c.(type) {
	case nil:
		return def
	case ansi.BasicColor:
		return dumpBasic[c%16]
	case ansi.IndexedColor:
		if c < 16 {
			return dumpBasic[c]
		}
		// xterm's 6x6x6 cube and gray ramp, in uint8 arithmetic.
		if c >= 232 {
			v := 8 + 10*(uint8(c)-232)
			return color.RGBA{v, v, v, 0xff}
		}
		n := uint8(c) - 16
		step := func(i uint8) uint8 {
			if i == 0 {
				return 0
			}
			return 55 + 40*i
		}
		return color.RGBA{step(n / 36), step(n / 6 % 6), step(n % 6), 0xff}
	}
	rgba, _ := color.RGBAModel.Convert(c).(color.RGBA)
	rgba.A = 0xff
	return rgba
}

// writePanel draws lines in a panel frame: text cells in their colors on
// kc's blue, placeholder cells as the tile of their image the terminal
// holds.
func writePanel(t *testing.T, path string, ft *fakeTerm, title string, lines []string, width int) {
	t.Helper()
	f, err := opentype.Parse(gomono.TTF)
	if err != nil {
		t.Fatal(err)
	}
	cw, ch := termCellW, termCellH
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(ch) * 0.62, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	cols, rows := width+2, len(lines)+2
	out := image.NewRGBA(image.Rect(0, 0, cols*cw, rows*ch))
	draw.Draw(out, out.Bounds(), image.NewUniform(dumpBlue), image.Point{}, draw.Src)
	// The frame: mc's single lines, the title in the top one.
	frame := image.NewUniform(dumpText)
	lw := 2
	draw.Draw(out, image.Rect(cw/2, ch/2-lw/2, cols*cw-cw/2, ch/2+lw/2), frame, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(cw/2, rows*ch-ch/2-lw/2, cols*cw-cw/2, rows*ch-ch/2+lw/2), frame, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(cw/2-lw/2, ch/2, cw/2+lw/2, rows*ch-ch/2), frame, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(cols*cw-cw/2-lw/2, ch/2, cols*cw-cw/2+lw/2, rows*ch-ch/2), frame, image.Point{}, draw.Src)
	text := func(x, y int, s string, fg color.RGBA, bg *color.RGBA) {
		if bg != nil {
			draw.Draw(out, image.Rect(x*cw, y*ch, (x+ansi.StringWidth(s))*cw, (y+1)*ch), image.NewUniform(*bg), image.Point{}, draw.Src)
		}
		d := &font.Drawer{Dst: out, Src: image.NewUniform(fg), Face: face, Dot: fixed.P(x*cw, y*ch+ch*3/4)}
		d.DrawString(s)
	}
	title = " " + ansi.Truncate(title, width-2, "") + " "
	text(1, 0, title, dumpText, &dumpBlue)
	diacritic := make(map[rune]int)
	for n := range kittygfx.MaxCells {
		d, _ := kittygfx.Diacritic(n)
		diacritic[d] = n
	}
	for y, l := range lines {
		buf := uv.NewScreenBuffer(width, 1)
		uv.NewStyledString(l).Draw(buf, buf.Bounds())
		for x := range width {
			c := buf.CellAt(x, 0)
			if c == nil || c.Content == "" || c.Content == " " {
				continue
			}
			px, py := (x+1)*cw, (y+1)*ch
			runes := []rune(c.Content)
			if runes[0] == kittygfx.Placeholder && len(runes) == 3 {
				id, _ := c.Style.Fg.(ansi.IndexedColor)
				img := ft.images[uint32(id)]
				if img == nil {
					t.Fatalf("no image %d for a placeholder", id)
				}
				row, col := diacritic[runes[1]], diacritic[runes[2]]
				src := image.Pt(img.Bounds().Min.X+col*cw, img.Bounds().Min.Y+row*ch)
				draw.Draw(out, image.Rect(px, py, px+cw, py+ch), img, src, draw.Over)
				continue
			}
			fg := dumpColor(c.Style.Fg, dumpText)
			var bg *color.RGBA
			// The panel's own ANSI blue is the blue of the dump.
			if c.Style.Bg != nil && c.Style.Bg != ansi.Blue {
				b := dumpColor(c.Style.Bg, dumpBlue)
				bg = &b
			}
			text(x+1, y+1, c.Content, fg, bg)
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
