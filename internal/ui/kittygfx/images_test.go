package kittygfx_test

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

func picture(c uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i := range img.Pix {
		img.Pix[i] = c
	}
	img.SetRGBA(0, 0, color.RGBA{c, 0, 0, 0xff})
	return img
}

// deliver runs a Show command as Bubble Tea would: the RawMsg's text goes
// to the output, then the message comes back to Update.
func deliver(t *testing.T, im *kittygfx.Images, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	msg, ok := cmd().(tea.RawMsg)
	if !ok || !kittygfx.IsMsg(msg) {
		t.Fatalf("command returned %T, want a kittygfx tea.RawMsg", msg)
	}
	out := fmt.Sprint(msg.Msg)
	im.Handle(msg)
	return out
}

func shownID(t *testing.T, im *kittygfx.Images, key, cols, rows int) string {
	t.Helper()
	lines, ok := im.Placeholders(key, cols, rows)
	if !ok {
		return ""
	}
	if len(lines) != rows {
		t.Fatalf("%d placeholder rows, want %d", len(lines), rows)
	}
	sgr, _, _ := strings.Cut(lines[0], string(kittygfx.Placeholder))
	return sgr
}

func TestImagesSwitchOnlyAfterTheTransmit(t *testing.T) {
	t.Parallel()
	im := kittygfx.NewImages(kittygfx.Encoder{}, 240, 2)
	a, b := picture(1), picture(2)

	cmd := im.Show(0, a, 4, 2)
	if shownID(t, im, 0, 4, 2) != "" {
		t.Fatal("placeholders before the transmit reached the output")
	}
	if out := deliver(t, im, cmd); !strings.Contains(out, "i=240,c=4,r=2") {
		t.Fatalf("first transmit %q, want image 240", out[:min(len(out), 60)])
	}
	if got := shownID(t, im, 0, 4, 2); got != kittygfx.Color(240) {
		t.Fatalf("shown %q, want image 240", got)
	}
	if im.Show(0, a, 4, 2) != nil {
		t.Error("the same pixels are sent again")
	}
	if _, ok := im.Placeholders(0, 5, 2); ok {
		t.Error("placeholders for another size than the image's")
	}

	cmd = im.Show(0, b, 4, 2)
	if im.Show(0, b, 4, 2) != nil {
		t.Error("a picture on its way is sent twice")
	}
	if got := shownID(t, im, 0, 4, 2); got != kittygfx.Color(240) {
		t.Errorf("while the new picture is on its way the old one shows; got %q", got)
	}
	if out := deliver(t, im, cmd); !strings.Contains(out, "i=241,") {
		t.Fatalf("second transmit does not go to the hidden image 241")
	}
	if got := shownID(t, im, 0, 4, 2); got != kittygfx.Color(241) {
		t.Fatalf("shown %q, want image 241", got)
	}
	// Back to the first picture: image 240 still holds it.
	if im.Show(0, a, 4, 2) != nil {
		t.Error("a picture the terminal holds is sent again")
	}
	if got := shownID(t, im, 0, 4, 2); got != kittygfx.Color(240) {
		t.Errorf("shown %q, want image 240 again", got)
	}

	// Another key has its own IDs.
	if out := deliver(t, im, im.Show(1, a, 4, 2)); !strings.Contains(out, "i=242,") {
		t.Errorf("key 1 does not start at image 242")
	}
}

func TestImagesRelease(t *testing.T) {
	t.Parallel()
	im := kittygfx.NewImages(kittygfx.Encoder{}, 240, 2)
	if got := im.Release(); got != "" {
		t.Errorf("Release before any transmit = %q", got)
	}
	deliver(t, im, im.Show(0, picture(1), 4, 2))
	late := im.Show(0, picture(2), 4, 2) // still on its way at the release
	got := im.Release()
	enc := kittygfx.Encoder{}
	if want := enc.Delete(240) + enc.Delete(241); got != want {
		t.Errorf("Release = %q, want %q", got, want)
	}
	if _, ok := im.Placeholders(0, 4, 2); ok {
		t.Error("placeholders after the release")
	}
	deliver(t, im, late)
	if _, ok := im.Placeholders(0, 4, 2); ok {
		t.Error("a transmit sent before the release shows after it")
	}
	if got := im.Release(); got != "" {
		t.Errorf("second Release = %q, want nothing", got)
	}
	if im.Show(0, picture(1), 4, 2) == nil {
		t.Error("after a release the picture is not sent again")
	}
}

func TestImagesRefuseWhatCannotShow(t *testing.T) {
	t.Parallel()
	im := kittygfx.NewImages(kittygfx.Encoder{}, 240, 1)
	for _, c := range []struct {
		key, cols, rows int
		img             *image.RGBA
	}{
		{key: 1, cols: 4, rows: 2, img: picture(1)},
		{key: 0, cols: 0, rows: 2, img: picture(1)},
		{key: 0, cols: kittygfx.MaxCells + 1, rows: 2, img: picture(1)},
		{key: 0, cols: 4, rows: 2},
	} {
		if im.Show(c.key, c.img, c.cols, c.rows) != nil {
			t.Errorf("Show(%d, %dx%d) sent an image", c.key, c.cols, c.rows)
		}
	}
}

func TestCellSize(t *testing.T) {
	t.Parallel()
	im := kittygfx.NewImages(kittygfx.Encoder{}, 240, 1)
	if w, h := im.CellSize(10, 5); w != kittygfx.FallbackCellWidth || h != kittygfx.FallbackCellHeight {
		t.Errorf("before a report: %dx%d, want the fallback", w, h)
	}
	if im.Handle(uv.CellSizeEvent{Width: 0, Height: 20}) {
		t.Error("a zero width counts as a report")
	}
	if !im.Handle(uv.CellSizeEvent{Width: 9, Height: 18}) || im.Handle(uv.CellSizeEvent{Width: 9, Height: 18}) {
		t.Error("Handle does not report exactly the change")
	}
	if w, h := im.CellSize(10, 5); w != 9 || h != 18 {
		t.Errorf("CellSize = %dx%d, want 9x18", w, h)
	}
	// A plot this large at 9x18 would be over 2 million pixels.
	w, h := im.CellSize(250, 60)
	if 250*60*w*h > 1_500_000 || w < 4 || h < 8 {
		t.Errorf("large plot: %dx%d per cell", w, h)
	}
}

func TestIsMsg(t *testing.T) {
	t.Parallel()
	if kittygfx.IsMsg(tea.RawMsg{Msg: "x"}) || kittygfx.IsMsg(tea.KeyPressMsg{}) {
		t.Error("foreign messages taken")
	}
	if !kittygfx.IsMsg(uv.CellSizeEvent{Width: 1, Height: 1}) {
		t.Error("cell size report not taken")
	}
	if msg := kittygfx.QueryCellSize()(); fmt.Sprint(msg.(tea.RawMsg).Msg) != "\x1b[16t" {
		t.Errorf("QueryCellSize writes %q", fmt.Sprint(msg.(tea.RawMsg).Msg))
	}
}
