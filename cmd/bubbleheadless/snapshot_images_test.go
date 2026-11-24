package main

import (
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/taigrr/bubbleterm/emulator"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// at is the color of pixel (x, y) of cell (col, row).
func at(img image.Image, col, row, x, y int) color.Color {
	return img.At(col*colorCellWidth+x, row*colorCellHeight+y)
}

// onlyBackground reports whether every pixel of cell (col, row) is bg.
func onlyBackground(img image.Image, col, row int, bg color.Color) bool {
	for y := range colorCellHeight {
		for x := range colorCellWidth {
			if !sameColor(at(img, col, row, x, y), bg) {
				return false
			}
		}
	}
	return true
}

// checkQuadrantTiles checks that the cells at (col0, row0) and the three
// after them show the tiles of a 2x2-cell quadrants image.
func checkQuadrantTiles(t *testing.T, img image.Image, col0, row0 int) {
	t.Helper()
	want := [2][2]color.RGBA{{red, green}, {blue, white}}
	for r := range 2 {
		for c := range 2 {
			for _, p := range []image.Point{{0, 0}, {colorCellWidth - 1, colorCellHeight - 1}, {4, 9}} {
				if got := at(img, col0+c, row0+r, p.X, p.Y); !sameColor(got, want[r][c]) {
					t.Fatalf("tile (%d,%d) pixel %v = %v, want %v", r, c, p, got, want[r][c])
				}
			}
		}
	}
}

func TestRenderCellsDrawsImageTiles(t *testing.T) {
	t.Parallel()
	g := newGraphics(colorCellWidth, colorCellHeight)
	src := quadrants(2*colorCellWidth, 2*colorCellHeight)
	runCommands(g, "a=T,U=1,f=100,q=2,i=240,c=2,r=2;"+base64.StdEncoding.EncodeToString(encodePNG(t, src)))

	ph := func(id uint8, row, col int) uv.Cell {
		return uv.Cell{Content: cell(t, row, col), Width: 1, Style: uv.Style{Fg: ansi.IndexedColor(id)}}
	}
	bare := func(id uint8) uv.Cell {
		return uv.Cell{Content: cell(t), Width: 1, Style: uv.Style{Fg: ansi.IndexedColor(id)}}
	}
	text := func(s string) uv.Cell { return uv.Cell{Content: s, Width: 1} }
	cells := [][]uv.Cell{
		{ph(240, 0, 0), ph(240, 0, 1), text("x"), ph(241, 0, 0)},
		{ph(240, 1, 0), ph(240, 1, 1), text(" "), ph(240, 5, 5)},
		// Text drawn over an image replaces the tile of its cell only.
		{ph(240, 0, 0), text("x"), ph(240, 0, 1), text(" ")},
		// A placeholder without diacritics continues only the cell just
		// before it: after text it is cell (0,0) again.
		{ph(240, 0, 0), text("x"), bare(240), text(" ")},
	}
	img, err := renderCells(cells, g)
	if err != nil {
		t.Fatal(err)
	}
	checkQuadrantTiles(t, img, 0, 0)
	if onlyBackground(img, 2, 0, defaultBg) {
		t.Error("the text cell over the image shows no glyph")
	}
	// An image the store does not have and a cell outside the placement
	// show the cell's background, not a missing-glyph box.
	for _, c := range []image.Point{{3, 0}, {3, 1}} {
		if !onlyBackground(img, c.X, c.Y, defaultBg) {
			t.Errorf("cell %v is not blank", c)
		}
	}
	if got := at(img, 0, 2, 4, 9); !sameColor(got, red) {
		t.Errorf("tile (0,0) left of the text = %v, want red", got)
	}
	if got := at(img, 2, 2, 4, 9); !sameColor(got, green) {
		t.Errorf("tile (0,1) right of the text = %v, want green", got)
	}
	if got := at(img, 2, 3, 4, 9); !sameColor(got, red) {
		t.Errorf("a bare placeholder after text = %v, want tile (0,0), red", got)
	}

	// Without a store the placeholders are blank too.
	none, err := renderCells(cells, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !onlyBackground(none, 0, 0, defaultBg) {
		t.Error("a placeholder without images is not blank")
	}
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// waitForCells polls the emulator until cell (0, 0) holds a placeholder.
func waitForCells(t *testing.T, emu *emulator.Emulator) [][]uv.Cell {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		cells := emu.GetCells()
		if len(cells) > 0 && len(cells[0]) > 0 {
			if _, ok := readPlaceholder(cells[0][0].Content, cells[0][0].Style.Fg, nil); ok {
				return cells
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no placeholder on the screen: %q", emu.GetScreen().Rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// kcOutput is what kc writes for an image of 2x2 cells shown at the top
// left: the transmit, then the placeholder rows in the image's color.
func kcOutput(t *testing.T) string {
	t.Helper()
	png := encodePNG(t, quadrants(2*colorCellWidth, 2*colorCellHeight))
	out := kittygfx.Encoder{}.Transmit(240, png, 2, 2)
	for i, row := range kittygfx.Rows(2, 2) {
		out += fmt.Sprintf("\x1b[%d;1H", i+1) + kittygfx.Color(240) + row + "\x1b[39m"
	}
	return out
}

// kc's own encoder, through the filter and the terminal emulator, to the
// snapshot: the cells hold what the emulator made of kc's placeholders.
func TestKittyImagesThroughTheEmulator(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	g := newGraphics(colorCellWidth, colorCellHeight)
	emu, err := emulator.NewFromPipes(10, 3, newOutputFilter(pr, io.Discard, g, func() (int, int) { return 10, 3 }), nopWriteCloser{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pw.Close()
		_ = emu.Close()
	})
	go func() { _, _ = io.WriteString(pw, kcOutput(t)) }()
	img, err := renderCells(waitForCells(t, emu), g)
	if err != nil {
		t.Fatal(err)
	}
	checkQuadrantTiles(t, img, 0, 0)
}

const helperChildEnv = "BUBBLEHEADLESS_TEST_CHILD"

// TestHelperChild is the child of TestSessionShowsKittyImages: it writes
// kc's output for an image and waits until the session ends.
func TestHelperChild(t *testing.T) {
	if os.Getenv(helperChildEnv) != "kitty" {
		t.Skip("runs as the child of TestSessionShowsKittyImages")
	}
	if _, err := io.WriteString(os.Stdout, kcOutput(t)); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// A child on the session's PTY: its images reach the color snapshot.
func TestSessionShowsKittyImages(t *testing.T) {
	t.Setenv(helperChildEnv, "kitty")
	s, err := newSession(10, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	if err := s.startCommand([]string{os.Args[0], "-test.run=^TestHelperChild$"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.terminate()
		select {
		case <-s.exitCh:
		case <-time.After(5 * time.Second):
			t.Error("the child did not exit")
		}
	})
	img, err := renderCells(waitForCells(t, s.emu), s.gfx)
	if err != nil {
		t.Fatal(err)
	}
	checkQuadrantTiles(t, img, 0, 0)
	if got := s.gfx.describe(shownImages(s.emu.GetCells())); len(got) != 1 || got[0] != "image 240: 18x36 px, placement 2x2 cells, shown" {
		t.Errorf("images = %q", got)
	}
}
