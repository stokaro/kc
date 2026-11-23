package dashboard_test

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// leaves runs cmd and the batches it returns, without handing anything to
// Update: what one Update asked for.
func leaves(cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
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
			out = append(out, msg)
		}
	}
	return out
}

// encodes are the pictures made off the event loop among msgs.
func encodes(msgs []tea.Msg) []tea.Msg {
	var out []tea.Msg
	for _, msg := range msgs {
		if fmt.Sprintf("%T", msg) == "dashboard.encodedMsg" {
			out = append(out, msg)
		}
	}
	return out
}

var transmitCols = regexp.MustCompile(`a=T,U=1,C=1,f=100,q=2,i=\d+,c=(\d+),`)

// TestPicturesAreMadeInACommand: Update returns no transmit itself; the
// raster and the PNG come back from a command, and only then go out.
func TestPicturesAreMadeInACommand(t *testing.T) {
	t.Parallel()
	m, _, _ := openWithImages(t, 140, 40)
	cmd, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft}) // the cursor cell is lighter: new pixels
	msgs := leaves(cmd)
	for _, msg := range msgs {
		if raw, ok := msg.(tea.RawMsg); ok && strings.Contains(fmt.Sprint(raw.Msg), "a=T") {
			t.Fatal("Update sent a picture it encoded itself")
		}
	}
	pics := encodes(msgs)
	if len(pics) != 2 {
		t.Fatalf("%d pictures made, want one per chart", len(pics))
	}
	var out strings.Builder
	for _, p := range pics {
		out.WriteString(gfxRun(t, m, func() tea.Msg { return p }))
	}
	if n := strings.Count(out.String(), "a=T"); n != 2 {
		t.Errorf("%d transmits after the pictures came back, want 2", n)
	}
}

// TestOnePictureInFlightPerChart: while a chart's picture is being made,
// another change starts none; its return starts the next.
func TestOnePictureInFlightPerChart(t *testing.T) {
	t.Parallel()
	m, _, _ := openWithImages(t, 140, 40)
	cmd, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	first := encodes(leaves(cmd))
	if len(first) != 2 {
		t.Fatalf("%d pictures made, want 2", len(first))
	}
	cmd, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if n := len(encodes(leaves(cmd))); n != 0 {
		t.Fatalf("%d more pictures started while two are in flight", n)
	}
	// Each return starts the picture of its own chart again, once.
	var next []tea.Msg
	for _, p := range first {
		c, _ := m.Update(p)
		next = append(next, encodes(leaves(c))...)
	}
	if len(next) != 2 {
		t.Errorf("%d pictures restarted, want one per chart", len(next))
	}
}

// TestStalePictureIsDropped: a picture made for a plot size the chart no
// longer has is not sent; the chart gets one of its new size.
func TestStalePictureIsDropped(t *testing.T) {
	t.Parallel()
	m, _, opened := openWithImages(t, 140, 40)
	before := make(map[string]bool)
	for _, s := range transmitCols.FindAllStringSubmatch(opened, -1) {
		before[s[1]] = true
	}
	// Focus the CPU chart, which F4 then expands.
	gfxRun(t, m, func() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyTab} })
	cmd, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	old := encodes(leaves(cmd))
	if len(old) != 2 {
		t.Fatalf("%d pictures made, want 2", len(old))
	}
	// Expanding the focused chart changes its plot size without a resize.
	cmd, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF4})
	if n := len(encodes(leaves(cmd))); n != 0 {
		t.Fatalf("%d pictures started while two are in flight", n)
	}
	var out strings.Builder
	for _, p := range old {
		out.WriteString(gfxRun(t, m, func() tea.Msg { return p }))
	}
	sent := transmitCols.FindAllStringSubmatch(out.String(), -1)
	if len(sent) != 1 {
		t.Fatalf("%d transmits after the expand, want one of the expanded chart", len(sent))
	}
	if before[sent[0][1]] {
		t.Errorf("a picture %s cells wide went out: the width before the expand", sent[0][1])
	}
	if !strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Error("the expanded chart shows no image")
	}
}

// TestPictureAfterAResizeIsDropped: a picture still being made when the
// size changes never goes out.
func TestPictureAfterAResizeIsDropped(t *testing.T) {
	t.Parallel()
	m, _, _ := openWithImages(t, 140, 40)
	cmd, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	old := encodes(leaves(cmd))
	gfxRun(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 110, Height: 34} })
	var out strings.Builder
	for _, p := range old {
		out.WriteString(gfxRun(t, m, func() tea.Msg { return p }))
	}
	if strings.Contains(out.String(), "a=T") || strings.ContainsRune(m.View(), kittygfx.Placeholder) {
		t.Fatal("a picture made before the resize was sent or placed")
	}
	out.Reset()
	out.WriteString(gfxRun(t, m, func() tea.Msg { return dashboard.SettledMsgForTest(m) }))
	if n := strings.Count(out.String(), "a=T"); n != 2 {
		t.Errorf("%d pictures after the size settled, want 2", n)
	}
}

// TestPicturesRunBesideUpdates: pictures made on other goroutines while
// the dashboard takes new samples; run with -race.
func TestPicturesRunBesideUpdates(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	src := f.sources(false)
	src.Images = kittygfx.NewImages(kittygfx.Encoder{}, 240, dashboard.ImageKeys)
	m := dashboard.New(src, web)
	gfxRun(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 140, Height: 40} })
	gfxRun(t, m, m.Start())
	results := make(chan tea.Msg, 64)
	var wg sync.WaitGroup
	for range 20 {
		f.advance(15 * time.Second)
		cmd, _ := m.Update(dashboard.TickMsgForTest(m))
		for _, msg := range leaves(cmd) {
			if fmt.Sprintf("%T", msg) != "dashboard.encodedMsg" {
				next, _ := m.Update(msg)
				for _, l := range leaves(next) {
					if fmt.Sprintf("%T", l) == "dashboard.encodedMsg" {
						results <- l
					}
				}
				continue
			}
			results <- msg
		}
		// Hand the pictures back on the "event loop" while new ones are
		// made concurrently by the commands the returns start.
		for len(results) > 0 {
			p := <-results
			next, _ := m.Update(p)
			wg.Go(func() {
				for _, l := range leaves(next) {
					if fmt.Sprintf("%T", l) == "dashboard.encodedMsg" {
						results <- l
					}
				}
			})
		}
	}
	wg.Wait()
}
