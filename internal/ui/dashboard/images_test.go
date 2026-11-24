package dashboard_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// gfxRun is run that also collects the raw output, as Bubble Tea writes it.
func gfxRun(t *testing.T, m *dashboard.Model, cmd tea.Cmd) string {
	t.Helper()
	var out strings.Builder
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			if raw, ok := msg.(tea.RawMsg); ok {
				fmt.Fprint(&out, raw.Msg)
			}
			next, _ := m.Update(msg)
			queue = append(queue, next)
		}
	}
	return out.String()
}

func openWithImages(t *testing.T, w, h int) (*dashboard.Model, *kittygfx.Images, string) {
	t.Helper()
	f := &fakes{now: t0, amounts: measured()}
	src := f.sources(true)
	im := kittygfx.NewImages(kittygfx.Encoder{}, 240, dashboard.ImageKeys)
	src.Images = im
	m := dashboard.New(src, web)
	out := gfxRun(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} })
	out += gfxRun(t, m, m.Start())
	return m, im, out
}

func TestImagesReplaceThePlotCells(t *testing.T) {
	t.Parallel()
	const w, h = 140, 40
	m, _, out := openWithImages(t, w, h)
	// Each chart's pixels change as the samples arrive; each change is sent.
	if n := strings.Count(out, "\x1b_Ga=T,U=1,C=1,"); n < 2 {
		t.Fatalf("%d images sent, want at least one per chart", n)
	}
	if !strings.Contains(out, "\x1b[16t") {
		t.Error("the cell size is not asked")
	}
	view := m.View()
	lines := strings.Split(view, "\n")
	ids := make(map[string]int)
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != w {
			t.Fatalf("line %d is %d cells, want %d", i, lw, w)
		}
		for _, id := range []string{"240", "241", "242", "243"} {
			if strings.Contains(l, "\x1b[38;5;"+id+"m"+string(kittygfx.Placeholder)) {
				ids[id]++
			}
		}
	}
	if len(ids) != 2 {
		t.Fatalf("placeholders of images %v, want two charts", ids)
	}
	// The axis labels and the time axis stay text.
	plain := strings.Split(ansi.Strip(view), "\n")
	requireText(t, plain, "CPU", "Memory", "└")
	// Every placeholder row sits right after the value axis.
	for _, l := range plain {
		if i := strings.IndexRune(l, kittygfx.Placeholder); i > 0 && !strings.HasSuffix(l[:i], "│") {
			t.Fatalf("a placeholder row does not start after the axis: %q", l[:i])
		}
	}

	// Nothing changes: nothing is sent.
	if again := gfxRun(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} }); strings.Contains(again, "a=T") {
		t.Error("the same pictures are sent again")
	}
}

func TestDrawKeyCyclesThroughImages(t *testing.T) {
	t.Parallel()
	m, _, _ := openWithImages(t, 140, 40)
	press := func(k string) string {
		cmd, _ := m.Key(k)
		return gfxRun(t, m, cmd)
	}
	hasPlaceholders := func() bool { return strings.ContainsRune(m.View(), kittygfx.Placeholder) }
	next := func() string { return m.FunctionKeys()[5].Label }
	if !hasPlaceholders() || next() != "Blocks" {
		t.Fatalf("images are not drawn first (F6 offers %q)", next())
	}
	if out := press("b"); !strings.Contains(out, "a=d,d=I") || hasPlaceholders() {
		t.Errorf("b from images: deletes %v, placeholders %v; want blocks without images", strings.Contains(out, "a=d"), hasPlaceholders())
	}
	if next() != "Braille" {
		t.Errorf("F6 offers %q after images, want Braille", next())
	}
	press("f6")
	if next() != "Images" {
		t.Errorf("F6 offers %q after braille, want Images", next())
	}
	if out := press("f6"); !strings.Contains(out, "a=T") || !hasPlaceholders() {
		t.Error("F6 from braille does not bring the images back")
	}
}

// TestViewMenuPicksTheDrawing: the View menu checks the drawing in use
// and switches to the one picked.
func TestViewMenuPicksTheDrawing(t *testing.T) {
	t.Parallel()
	m, _, _ := openWithImages(t, 140, 40)
	var ran []string
	checked := func() []string {
		var out []string
		for _, it := range m.Menus(func(k string) tea.Cmd { ran = append(ran, k); return nil })[2].Items {
			if it.Checked {
				out = append(out, it.Label)
			}
		}
		return out
	}
	if c := checked(); len(c) != 1 || !strings.Contains(c[0], "images") {
		t.Fatalf("checked %q, want the images", c)
	}
	out := gfxRun(t, m, func() tea.Msg { cmd, _ := m.Key("draw:blocks"); return cmd() })
	if !strings.Contains(out, "a=d,d=I") || strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Error("picking blocks keeps the images")
	}
	if c := checked(); len(c) != 1 || !strings.Contains(c[0], "blocks") {
		t.Errorf("checked %q, want the blocks", c)
	}
}

// TestNoDataWordsStayText: with images on, a chart without samples is an
// empty plot in the image with the words "no data" as text over it.
func TestNoDataWordsStayText(t *testing.T) {
	t.Parallel()
	a := measured()
	a.CPU.Used, a.CPU.UsedKnown, a.Memory.Used, a.Memory.UsedKnown, a.Top, a.Rows = 0, false, 0, false, nil, nil
	f := &fakes{now: t0, amounts: a}
	src := f.sources(false)
	src.Images = kittygfx.NewImages(kittygfx.Encoder{}, 240, dashboard.ImageKeys)
	m := dashboard.New(src, web)
	gfxRun(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 120, Height: 30} })
	gfxRun(t, m, m.Start())
	view := m.View()
	if !strings.Contains(ansi.Strip(view), "no data") {
		t.Fatalf("no \"no data\" band:\n%s", ansi.Strip(view))
	}
	if !strings.ContainsRune(view, kittygfx.Placeholder) {
		t.Error("no placeholders around the words")
	}
}

func TestNoImagesWithoutTheSource(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 140, 40)
	if strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Fatal("placeholders without images")
	}
	key(t, m, "b")
	if l := m.FunctionKeys()[5].Label; l != "Blocks" || strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Errorf("F6 offers %q from braille without images, want Blocks", l)
	}
	for _, it := range m.Menus(nil)[2].Items {
		if strings.Contains(it.Label, "images") {
			t.Error("the View menu offers images the terminal cannot show")
		}
	}
}

func TestLiveViewSendsNewPixelsOnly(t *testing.T) {
	t.Parallel()
	m, _, _ := openWithImages(t, 140, 40)
	// Pausing freezes the range; the next ticks change no pixel.
	cmd, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	gfxRun(t, m, cmd)
	for range 3 {
		if out := gfxRun(t, m, func() tea.Msg { return dashboard.TickMsgForTest(m) }); strings.Contains(out, "a=T") {
			t.Fatal("a paused view sends images on a tick")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestCursorMovesSendNoImage: the cursor is text over the placeholders,
// so moving it sends no picture; new data does.
func TestCursorMovesSendNoImage(t *testing.T) {
	t.Parallel()
	const w, h = 140, 40
	m, _, _ := openWithImages(t, w, h)
	// The first press pauses, which may redraw once; the moves after it
	// stay inside the range.
	cmd, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	gfxRun(t, m, cmd)
	var out string
	for _, k := range []rune{tea.KeyLeft, tea.KeyLeft, tea.KeyRight, tea.KeyLeft} {
		cmd, _ := m.Update(tea.KeyPressMsg{Code: k})
		out += gfxRun(t, m, cmd)
	}
	if n := strings.Count(out, "\x1b_G"); n != 0 {
		t.Fatalf("cursor moves sent %d images", n)
	}
	// The cursor column is drawn as text inside the image's cells.
	view := ansi.Strip(m.View())
	if !strings.Contains(view, string(kittygfx.Placeholder)) {
		t.Fatal("no placeholders under the cursor")
	}
}

// TestImagesDrawALinePerPod: with images and session samples, a
// workload's charts draw a line per pod under the usage, and the legend
// says so; text charts do not.
func TestImagesDrawALinePerPod(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	src := f.sources(false)
	src.Images = kittygfx.NewImages(kittygfx.Encoder{}, 240, dashboard.ImageKeys)
	m := dashboard.New(src, web)
	gfxRun(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 140, Height: 40} })
	gfxRun(t, m, m.Start())
	if view := ansi.Strip(m.View()); strings.Count(view, "── 2 pods  ┄ request") != 2 {
		t.Errorf("want a \"2 pods\" legend on both charts:\n%s", view)
	}
	text := open(t, &fakes{now: t0, amounts: measured()}, false, web, 140, 40)
	if view := ansi.Strip(text.View()); strings.Contains(view, "2 pods  ┄") {
		t.Errorf("text charts promise lines per pod:\n%s", view)
	}
}
