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
	press := func() string {
		cmd, _ := m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
		return gfxRun(t, m, cmd)
	}
	hasPlaceholders := func() bool { return strings.ContainsRune(m.View(), kittygfx.Placeholder) }
	if !hasPlaceholders() {
		t.Fatal("images are not drawn first")
	}
	if out := press(); !strings.Contains(out, "a=d,d=I") || hasPlaceholders() {
		t.Errorf("b from images: deletes %v, placeholders %v; want braille without images", strings.Contains(out, "a=d"), hasPlaceholders())
	}
	if !strings.Contains(m.View(), "b blocks") && !strings.Contains(ansi.Strip(m.View()), "b blocks") {
		t.Errorf("the key line does not offer blocks next")
	}
	press() // blocks
	if !strings.Contains(ansi.Strip(m.View()), "b images") {
		t.Errorf("the key line does not offer images after blocks")
	}
	if out := press(); !strings.Contains(out, "a=T") || !hasPlaceholders() {
		t.Error("b from blocks does not bring the images back")
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
	key(t, m, "b")
	if v := ansi.Strip(m.View()); strings.Contains(v, "b images") || strings.ContainsRune(v, kittygfx.Placeholder) {
		t.Error("b offers images the terminal cannot show")
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
