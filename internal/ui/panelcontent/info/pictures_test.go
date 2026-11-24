package info

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/kittygfx"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// Cell size the fake terminal reports: a Retina terminal at about 14
// points, as the chart dumps use.
const termCellW, termCellH = 16, 34

// fakeTerm is the part of a terminal the pictures need: it keeps the
// images the Kitty commands transmit and delete, and answers the cell
// size query.
type fakeTerm struct {
	images map[uint32]image.Image
	cur    uint32
	data   strings.Builder
	sent   int // transmits
	gone   int // deletes
}

func newFakeTerm() *fakeTerm { return &fakeTerm{images: make(map[uint32]image.Image)} }

var apcRe = regexp.MustCompile("\x1b_G([^;\x1b]*)(?:;([^\x1b]*))?\x1b\\\\")

func (ft *fakeTerm) write(t *testing.T, s string) {
	t.Helper()
	for _, m := range apcRe.FindAllStringSubmatch(s, -1) {
		keys := make(map[string]string)
		for kv := range strings.SplitSeq(m[1], ",") {
			k, v, _ := strings.Cut(kv, "=")
			keys[k] = v
		}
		switch keys["a"] {
		case "d":
			id, _ := strconv.ParseUint(keys["i"], 10, 32)
			delete(ft.images, uint32(id))
			ft.gone++
			continue
		case "T":
			id, _ := strconv.ParseUint(keys["i"], 10, 32)
			ft.cur = uint32(id)
			ft.data.Reset()
		}
		ft.data.WriteString(m[2])
		if keys["m"] == "0" {
			raw, err := base64.StdEncoding.DecodeString(ft.data.String())
			if err != nil {
				t.Fatalf("image %d: %v", ft.cur, err)
			}
			img, err := png.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("image %d: %v", ft.cur, err)
			}
			ft.images[ft.cur] = img
			ft.sent++
		}
	}
}

// drive runs cmd as Bubble Tea would, for the messages of the pictures:
// raw output goes to the terminal and comes back to the widget, a cell
// size query is answered, and pictures and settle timers go to the
// widget. Other messages (the refresh tick) are not run.
func drive(t *testing.T, w *Widget, ft *fakeTerm, cmd tea.Cmd) {
	t.Helper()
	ctx := t.Context()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 1000 {
			t.Fatal("the pictures never settle: a loop of commands")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.RawMsg:
			out := fmt.Sprint(msg.Msg)
			ft.write(t, out)
			if strings.Contains(out, "\x1b[16t") {
				next, _ := w.Update(ctx, panelcontent.TerminalMsg{Msg: uv.CellSizeEvent{Width: termCellW, Height: termCellH}})
				queue = append(queue, next)
			}
			next, _ := w.Update(ctx, panelcontent.TerminalMsg{Msg: msg})
			queue = append(queue, next)
		case pictureMsg, settledMsg:
			next, _ := w.Update(ctx, msg)
			queue = append(queue, next)
		}
	}
}

// startNoTick is Init without the refresh tick, which would wait.
func startNoTick(t *testing.T, w *Widget, width, height int) {
	t.Helper()
	ctx := t.Context()
	w.SetFocus(ctx, true)
	_, _ = w.Update(ctx, tea.WindowSizeMsg{Width: width, Height: height})
	w.refresh(ctx)
}

// pictureHarness is a widget with images over a fake terminal.
func pictureHarness(t *testing.T, summary models.InfoSummary, width int) (*harness, *fakeTerm) {
	t.Helper()
	h := &harness{now: harnessNow, summary: summary}
	h.setScope("ctx|ns", "kind-kc-demo: namespace kc-gfx-load")
	h.w = New(panelcontent.WidgetDeps{}, Options{
		Resolve: func(context.Context) Target { return h.target },
		Now:     func() time.Time { return h.now },
		Images:  kittygfx.NewImages(kittygfx.Encoder{}, 0xC0, ImageKeys),
		Tick: func(_ time.Duration, f func(time.Time) tea.Msg) tea.Cmd {
			return func() tea.Msg { return f(time.Time{}) }
		},
	})
	ft := newFakeTerm()
	startNoTick(t, h.w, width, 60)
	drive(t, h.w, ft, h.w.startPictures())
	return h, ft
}

// withHistory records samples of cpu and mem at every step up to now.
func withHistory(h *harness, n int, step time.Duration, cpu, mem func(i int) float64) {
	hist := &history{}
	for i := range n {
		at := h.now.Add(-time.Duration(n-1-i) * step)
		hist.add(at, &usageSample{percent: cpu(i), base: "allocatable"}, &usageSample{percent: mem(i), base: "allocatable"})
	}
	h.w.history[h.w.historyKey()] = hist
}

// placeholderCells counts the placeholder cells of each image ID in lines.
func placeholderCells(t *testing.T, lines []string, width int) map[uint8]int {
	t.Helper()
	out := make(map[uint8]int)
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("line %d is %d cells wide in %d", i, w, width)
		}
		buf := uv.NewScreenBuffer(width, 1)
		uv.NewStyledString(l).Draw(buf, buf.Bounds())
		for x := range width {
			c := buf.CellAt(x, 0)
			if c == nil || !strings.HasPrefix(c.Content, string(kittygfx.Placeholder)) {
				continue
			}
			id, ok := c.Style.Fg.(ansi.IndexedColor)
			if !ok {
				t.Fatalf("line %d: a placeholder in %T, want a 256-color ID", i, c.Style.Fg)
			}
			out[uint8(id)]++
		}
	}
	return out
}

func capacitySummary() models.InfoSummary {
	b := bars(5, 9)
	b[0].Marks = []models.InfoMark{{Kind: models.InfoMarkRequest, Percent: 12}, {Kind: models.InfoMarkLimit, Percent: 11}}
	b[1].Marks = []models.InfoMark{{Kind: models.InfoMarkRequest, Percent: 10}, {Kind: models.InfoMarkLimit, Percent: 64}}
	b[3] = models.InfoBar{Label: "Disk", Known: true, Percent: 57, Detail: "341/589 Gi", Flag: "ok", Health: models.RowHealthOK}
	return models.InfoSummary{UsageAt: harnessNow, Bars: b, BarsTitle: "Cluster capacity (all nodes, all namespaces)"}
}

// TestPicturesReplaceBarsAndSparklines: with images, each bar is a gauge
// of the text bar's cells and each sparkline a picture of two rows over
// its frame; every other cell stays text, and no line is wider than the
// panel.
func TestPicturesReplaceBarsAndSparklines(t *testing.T) {
	t.Parallel()
	for _, width := range []int{35, 58} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Parallel()
			h, ft := pictureHarness(t, capacitySummary(), width)
			withHistory(h, 30, 15*time.Second, func(i int) float64 { return 3 + float64(i%5) }, func(int) float64 { return 9 })
			drive(t, h.w, ft, h.w.syncPictures())
			lines := h.w.render(width, 60, true)
			cells := placeholderCells(t, lines, width)
			cellsOf := barCells(capacitySummary().Bars, width)
			if len(cells) != ImageKeys {
				t.Fatalf("%d pictures on screen, want %d: %v", len(cells), ImageKeys, cells)
			}
			for id, n := range cells {
				if id < 0xC0 || id >= 0xC0+2*ImageKeys {
					t.Errorf("image ID %d is outside the widget's block", id)
				}
				if n != cellsOf && n%sparkRows != 0 {
					t.Errorf("image %d covers %d cells: neither a bar of %d nor a sparkline of %d rows", id, n, cellsOf, sparkRows)
				}
			}
			if ft.sent != ImageKeys {
				t.Errorf("%d transmits, want one per picture", ft.sent)
			}
			// The text around the pictures is the text Info shows anyway.
			plain := ansi.Strip(strings.Join(lines, "\n"))
			for _, want := range []string{"CPU", "5%", "Disk", "57%", "top 10%", "of 15m"} {
				if !strings.Contains(plain, want) {
					t.Errorf("the panel lost %q:\n%s", want, plain)
				}
			}
			if strings.ContainsAny(plain, "█░▁▂▃▄▅▆▇") {
				t.Errorf("a text bar or sparkline is left under the pictures:\n%s", plain)
			}
		})
	}
}

// TestTextFallbackIsTheTextMode: until a picture arrives, the bar is the
// text bar Info draws without images, in the same cells.
func TestTextFallbackIsTheTextMode(t *testing.T) {
	t.Parallel()
	text := startedHarness(t, capacitySummary(), 58, 60)
	gfx := &harness{now: harnessNow, summary: capacitySummary()}
	gfx.setScope("ctx|ns", "ctx: namespace ns")
	gfx.w = New(panelcontent.WidgetDeps{}, Options{
		Resolve: func(context.Context) Target { return gfx.target },
		Now:     func() time.Time { return gfx.now },
		Images:  kittygfx.NewImages(kittygfx.Encoder{}, 0xC0, ImageKeys),
	})
	startNoTick(t, gfx.w, 58, 60)
	want := text.w.render(58, 60, true)
	got := gfx.w.render(58, 60, true)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("before its pictures, Info with images differs from text Info:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestUnchangedPictureIsNotDrawnAgain: a tick with the same numbers, or a
// change smaller than a pixel of the gauge, starts no raster; a change of
// a pixel starts one, for that bar only.
func TestUnchangedPictureIsNotDrawnAgain(t *testing.T) {
	t.Parallel()
	h, ft := pictureHarness(t, capacitySummary(), 58)
	if got := pictureJobs(h.w.syncPictures()); got != 0 {
		t.Fatalf("an unchanged panel started %d pictures", got)
	}
	cells := barCells(capacitySummary().Bars, 58)
	pixel := 100.0 / float64(cells*termCellW)
	h.summary.Bars[0].Percent += pixel / 4
	h.last().summary = h.summary
	h.w.refresh(t.Context())
	if got := pictureJobs(h.w.syncPictures()); got != 0 {
		t.Fatalf("a change under a pixel started %d pictures", got)
	}
	h.summary.Bars[0].Percent += 3 * pixel
	h.last().summary = h.summary
	h.w.refresh(t.Context())
	pics := pictureMsgs(h.w.syncPictures())
	if len(pics) != 1 || pics[0].pic.Job().Key() != keyGaugeCPU {
		t.Fatalf("a change of three pixels started %d pictures, want the CPU gauge", len(pics))
	}
	sent := ft.sent
	drive(t, h.w, ft, func() tea.Msg { return pics[0] })
	if ft.sent != sent+1 {
		t.Errorf("%d transmits for the new picture, want 1", ft.sent-sent)
	}
}

// pictureJobs counts the pictures cmd starts. It runs them: the slot
// stays busy, so call it only where nothing should start.
func pictureJobs(cmd tea.Cmd) int { return len(pictureMsgs(cmd)) }

// pictureMsgs runs cmd and its batches and returns the pictures it made,
// without handing them to the widget.
func pictureMsgs(cmd tea.Cmd) []pictureMsg {
	var out []pictureMsg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case pictureMsg:
			out = append(out, msg)
		}
	}
	return out
}

// TestResizeDeletesAndWaits: a new panel width deletes the pictures and
// draws text until the width settles; then the pictures of the new width
// go out.
func TestResizeDeletesAndWaits(t *testing.T) {
	t.Parallel()
	h, ft := pictureHarness(t, capacitySummary(), 58)
	if len(ft.images) == 0 {
		t.Fatal("no pictures to start with")
	}
	ctx := t.Context()
	// A size that does not change the width keeps the pictures.
	cmd, _ := h.w.Update(ctx, tea.WindowSizeMsg{Width: 58, Height: 40})
	if strings.Contains(rawOutput(cmd), "a=d") {
		t.Fatal("a height change deleted the pictures")
	}
	cmd, _ = h.w.Update(ctx, tea.WindowSizeMsg{Width: 40, Height: 40})
	if h.w.pics.settling != true {
		t.Fatal("a width change does not settle")
	}
	if n := len(placeholderCells(t, h.w.render(40, 40, true), 40)); n != 0 {
		t.Errorf("%d pictures shown while the width settles", n)
	}
	drive(t, h.w, ft, cmd)
	if h.w.pics.settling {
		t.Fatal("still settling after the timer")
	}
	cells := placeholderCells(t, h.w.render(40, 40, true), 40)
	if len(cells) != 4 {
		t.Errorf("%d pictures after settling, want the 4 gauges: %v", len(cells), cells)
	}
	if ft.gone == 0 {
		t.Error("the resize deleted no image")
	}
	for id := range cells {
		if _, ok := ft.images[uint32(id)]; !ok {
			t.Errorf("image %d is shown but the terminal does not have it", id)
		}
	}
}

// rawOutput is what cmd and its batches write to the terminal.
func rawOutput(cmd tea.Cmd) string {
	var b strings.Builder
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.RawMsg:
			fmt.Fprint(&b, msg.Msg)
		}
	}
	return b.String()
}

// TestReleaseImagesLeavesText: leaving Info mode deletes every picture,
// and the panel draws text until it starts again.
func TestReleaseImagesLeavesText(t *testing.T) {
	t.Parallel()
	h, ft := pictureHarness(t, capacitySummary(), 58)
	ft.write(t, h.w.ReleaseImages())
	if len(ft.images) != 0 {
		t.Errorf("%d images left on the terminal", len(ft.images))
	}
	if n := len(placeholderCells(t, h.w.render(58, 60, true), 58)); n != 0 {
		t.Errorf("%d pictures shown after the release", n)
	}
	if h.w.ReleaseImages() != "" {
		t.Error("a second release deletes again")
	}
	// Back in Info mode: no settling, the pictures go out again.
	h.w.SetFocus(t.Context(), false)
	h.w.SetFocus(t.Context(), true)
	h.w.refresh(t.Context())
	drive(t, h.w, ft, h.w.startPictures())
	if n := len(placeholderCells(t, h.w.render(58, 60, true), 58)); n != 4 {
		t.Errorf("%d pictures after Info started again, want 4", n)
	}
}

// TestBarsThatGoAreDeleted: on a workload (three bars) the Disk gauge is
// deleted; on a loading summary every picture is.
func TestBarsThatGoAreDeleted(t *testing.T) {
	t.Parallel()
	h, ft := pictureHarness(t, capacitySummary(), 58)
	h.summary.Bars = h.summary.Bars[:3]
	h.last().summary = h.summary
	h.w.refresh(t.Context())
	drive(t, h.w, ft, h.w.syncPictures())
	for key := range keyGaugeDisk + 1 {
		held := false
		for _, id := range []uint32{0xC0 + 2*uint32(key), 0xC0 + 2*uint32(key) + 1} {
			_, ok := ft.images[id]
			held = held || ok
		}
		if want := key != keyGaugeDisk; held != want {
			t.Errorf("gauge %d on the terminal: %v, want %v", key, held, want)
		}
	}
	h.last().summary = models.InfoSummary{Loading: true}
	h.w.refresh(t.Context())
	drive(t, h.w, ft, h.w.syncPictures())
	if len(ft.images) != 0 {
		t.Errorf("%d images left while Info loads", len(ft.images))
	}
}

// TestStillPictureIsText: a frozen panel keeps text, not placeholders of
// images the widget goes on replacing.
func TestStillPictureIsText(t *testing.T) {
	t.Parallel()
	h, _ := pictureHarness(t, capacitySummary(), 58)
	v := h.w.TextView(t.Context(), panelcontent.Frame{Size: panelcontent.Size{Width: 58, Height: 60}})
	if strings.ContainsRune(v.Content, kittygfx.Placeholder) {
		t.Error("the text view has placeholders")
	}
	v = h.w.View(t.Context(), panelcontent.Frame{Size: panelcontent.Size{Width: 58, Height: 60}})
	if !strings.ContainsRune(v.Content, kittygfx.Placeholder) {
		t.Error("the view has no placeholders")
	}
}

// TestOtherOwnersTransmitsAreIgnored: a transmit of the other panel or of
// the dashboard coming back is not this widget's.
func TestOtherOwnersTransmitsAreIgnored(t *testing.T) {
	t.Parallel()
	h, _ := pictureHarness(t, capacitySummary(), 58)
	other := kittygfx.NewImages(kittygfx.Encoder{}, 0xD0, ImageKeys)
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	cmd := other.Show(0, img, 1, 1)
	raw, ok := cmd().(tea.RawMsg)
	if !ok {
		t.Fatal("no transmit")
	}
	if _, handled := h.w.Update(t.Context(), panelcontent.TerminalMsg{Msg: raw}); handled {
		t.Error("the widget took another set's transmit")
	}
	if _, handled := h.w.Update(t.Context(), panelcontent.TerminalMsg{Msg: uv.CellSizeEvent{Width: 20, Height: 40}}); !handled {
		t.Error("the widget ignored a cell size report")
	}
}

// TestSparkMarksSplitOverTwoRows: the scale goes next to the top of the
// picture, the span next to its bottom.
func TestSparkMarksSplitOverTwoRows(t *testing.T) {
	t.Parallel()
	scale, span := splitSparkMark(4*time.Minute, 10)
	if scale != " top 10%" || span != " 4m of 15m" {
		t.Errorf("marks %q %q", scale, span)
	}
	scale, span = splitSparkMark(historyWindow, 100)
	if scale != " top 100%" || span != " 15m" {
		t.Errorf("marks %q %q", scale, span)
	}
}

// TestLevelColors: the fill follows the state of what the bar shows.
func TestLevelColors(t *testing.T) {
	t.Parallel()
	for h, want := range map[models.RowHealth]string{
		models.RowHealthUnknown:  "#73bf69",
		models.RowHealthOK:       "#73bf69",
		models.RowHealthNotReady: "#ff9830",
		models.RowHealthFailed:   "#f2495c",
	} {
		c := levelColor(h)
		if got := fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B); got != want {
			t.Errorf("health %d fills %s, want %s", h, got, want)
		}
	}
	g := gaugeOf(models.InfoBar{Percent: 40, Marks: []models.InfoMark{{Kind: models.InfoMarkRequest, Percent: 20}, {Kind: models.InfoMarkLimit, Percent: math.Inf(1)}}})
	if len(g.Marks) != 2 || g.Marks[0].Color != requestColor || g.Marks[1].Color != limitColor {
		t.Errorf("marks %+v", g.Marks)
	}
}
