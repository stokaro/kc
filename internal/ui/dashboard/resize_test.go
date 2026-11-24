package dashboard_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// gfxSteps is gfxRun that stops after max messages and also reports
// whether a screen clear was asked for.
func gfxSteps(t *testing.T, m *dashboard.Model, cmd tea.Cmd, maxMsgs int) (out string, msgs int, cleared bool) {
	t.Helper()
	var b strings.Builder
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 && msgs < maxMsgs {
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
			msgs++
			if msg == tea.ClearScreen() {
				cleared = true
				continue
			}
			if raw, ok := msg.(tea.RawMsg); ok {
				fmt.Fprint(&b, raw.Msg)
			}
			next, _ := m.Update(msg)
			queue = append(queue, next)
		}
	}
	return b.String(), msgs, cleared
}

// TestResizeDeletesImagesAndClears: a new size deletes the images and
// clears the screen; the plots are text until the size settles, then
// images of the new size go out.
func TestResizeDeletesImagesAndClears(t *testing.T) {
	t.Parallel()
	m, _, _ := openWithImages(t, 140, 40)
	if !strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Fatal("no images before the resize")
	}
	out, _, cleared := gfxSteps(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 110, Height: 34} }, 100)
	if !cleared {
		t.Error("a resize does not clear the screen")
	}
	if !strings.Contains(out, "\x1b_Ga=d,d=I") {
		t.Error("a resize does not delete the images")
	}
	if strings.Contains(out, "a=T") || strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Error("images are sent or placed before the size settles")
	}
	// A tick while settling sends nothing either.
	if out, _, _ := gfxSteps(t, m, func() tea.Msg { return dashboard.TickMsgForTest(m) }, 100); strings.Contains(out, "a=T") {
		t.Error("a tick sends images before the size settles")
	}

	// A second step of the drag: the first settle is stale.
	stale := dashboard.SettledMsgForTest(m)
	gfxSteps(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 100, Height: 30} }, 100)
	if out, _, _ := gfxSteps(t, m, func() tea.Msg { return stale }, 100); strings.Contains(out, "a=T") {
		t.Error("the settle of an earlier size sends images")
	}

	out, _, _ = gfxSteps(t, m, func() tea.Msg { return dashboard.SettledMsgForTest(m) }, 100)
	if !strings.Contains(out, "\x1b[16t") {
		t.Error("the cell size is not asked again after the size settles")
	}
	if n := strings.Count(out, "a=T"); n != 2 {
		t.Errorf("%d images sent after the size settles, want one per chart", n)
	}
	if !strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Error("no images after the size settles")
	}
}

// TestSameSizeKeepsImages: a size message without a change (the App
// sends one when the dashboard opens or reopens) keeps the images.
func TestSameSizeKeepsImages(t *testing.T) {
	t.Parallel()
	m, _, _ := openWithImages(t, 140, 40)
	out, _, cleared := gfxSteps(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 140, Height: 40} }, 100)
	if cleared || strings.Contains(out, "a=d") || !strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Errorf("the same size clears %v, deletes %v, or drops the placeholders", cleared, strings.Contains(out, "a=d"))
	}
}

// TestTransmitDoesNotStartAnother: with a clock that moves on every
// read, as the real one does, a transmit coming back does not raster and
// send the charts again. Before, each return sent a new picture, whose
// return sent the next: a loop that flooded the terminal and kept the
// event loop seconds behind a resize.
func TestTransmitDoesNotStartAnother(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	src := f.sources(true)
	var mu sync.Mutex
	now := t0
	src.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(7 * time.Second)
		return now
	}
	src.Images = kittygfx.NewImages(kittygfx.Encoder{}, 240, dashboard.ImageKeys)
	m := dashboard.New(src, web)
	gfxSteps(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 140, Height: 40} }, 100)
	out, msgs, _ := gfxSteps(t, m, m.Start(), 200)
	if msgs >= 200 {
		t.Fatalf("opening does not settle: %d messages, %d images", msgs, strings.Count(out, "a=T"))
	}
	out, msgs, _ = gfxSteps(t, m, func() tea.Msg { return dashboard.TickMsgForTest(m) }, 200)
	// Each message the dashboard gets (the tick, the read it starts)
	// may send one image per chart; the returns of those send none.
	if n := strings.Count(out, "a=T"); msgs >= 200 || n > 2*(msgs-n) {
		t.Errorf("one tick: %d messages, %d of them images; want at most one image per chart per other message", msgs, n)
	}
}
