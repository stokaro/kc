package main

import (
	"bytes"
	"image/color"
	"path/filepath"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func TestCellColors(t *testing.T) {
	red := ansi.BasicColor(1)
	blue := ansi.BasicColor(4)
	tests := []struct {
		name   string
		style  uv.Style
		wantFg color.Color
		wantBg color.Color
	}{
		{name: "defaults", wantFg: defaultFg, wantBg: defaultBg},
		{name: "explicit", style: uv.Style{Fg: red, Bg: blue}, wantFg: red, wantBg: blue},
		{name: "reverse swaps", style: uv.Style{Fg: red, Bg: blue, Attrs: uv.AttrReverse}, wantFg: blue, wantBg: red},
		{name: "reverse of defaults", style: uv.Style{Attrs: uv.AttrReverse}, wantFg: defaultBg, wantBg: defaultFg},
		{name: "faint blends toward the background", style: uv.Style{Attrs: uv.AttrFaint}, wantFg: blend(defaultFg, defaultBg, 0.5), wantBg: defaultBg},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fg, bg := cellColors(tt.style)
			if !sameColor(fg, tt.wantFg) || !sameColor(bg, tt.wantBg) {
				t.Fatalf("got fg %v bg %v, want fg %v bg %v", fg, bg, tt.wantFg, tt.wantBg)
			}
		})
	}
}

func cellsOf(rows ...string) [][]uv.Cell {
	cells := make([][]uv.Cell, len(rows))
	for y, row := range rows {
		for _, r := range row {
			cells[y] = append(cells[y], uv.Cell{Content: string(r), Width: 1})
		}
	}
	return cells
}

func TestRenderCellsSizeAndColors(t *testing.T) {
	cells := cellsOf("ab ", "   ")
	cells[1][2].Style = uv.Style{Bg: ansi.BasicColor(4)}
	img, err := renderCells(cells)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.Bounds().Dx(), 3*colorCellWidth; got != want {
		t.Fatalf("width = %d, want %d", got, want)
	}
	if got, want := img.Bounds().Dy(), 2*colorCellHeight; got != want {
		t.Fatalf("height = %d, want %d", got, want)
	}
	// The styled cell is filled with its background, the others with the default.
	if got := img.At(2*colorCellWidth+1, colorCellHeight+1); !sameColor(got, ansi.BasicColor(4)) {
		t.Fatalf("styled cell background = %v", got)
	}
	if got := img.At(colorCellWidth*2+1, 1); !sameColor(got, defaultBg) {
		t.Fatalf("plain cell background = %v", got)
	}
	// A glyph leaves foreground pixels in its cell.
	inked := false
	for y := range colorCellHeight {
		for x := range colorCellWidth {
			if !sameColor(img.At(x, y), defaultBg) {
				inked = true
			}
		}
	}
	if !inked {
		t.Fatal("the glyph a drew nothing")
	}
}

// Box-drawing lines must reach the cell edges so that frames join across
// cells; a font glyph usually stops short of them.
func TestRenderCellsBoxLinesJoin(t *testing.T) {
	img, err := renderCells(cellsOf("┌─┐", "│ │", "└─┘"))
	if err != nil {
		t.Fatal(err)
	}
	cx := colorCellWidth / 2
	cy := colorCellHeight / 2
	checks := []struct {
		name string
		x, y int
		ink  bool
	}{
		{name: "horizontal line at the left edge of its cell", x: colorCellWidth, y: cy, ink: true},
		{name: "horizontal line at the right edge of its cell", x: 2*colorCellWidth - 1, y: cy, ink: true},
		{name: "vertical line at the top edge of its cell", x: cx, y: colorCellHeight, ink: true},
		{name: "vertical line at the bottom edge of its cell", x: cx, y: 2*colorCellHeight - 1, ink: true},
		{name: "corner reaches right", x: colorCellWidth - 1, y: cy, ink: true},
		{name: "corner reaches down", x: cx, y: colorCellHeight - 1, ink: true},
		{name: "corner has no arm up", x: cx, y: 0, ink: false},
		{name: "corner has no arm left", x: 0, y: cy, ink: false},
		{name: "blank cell stays blank", x: colorCellWidth + cx, y: colorCellHeight + cy, ink: false},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			got := !sameColor(img.At(c.x, c.y), defaultBg)
			if got != c.ink {
				t.Fatalf("ink at (%d,%d) = %v, want %v", c.x, c.y, got, c.ink)
			}
		})
	}
}

func TestDrawShapeLeavesOtherRunes(t *testing.T) {
	img, err := renderCells(cellsOf("█"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []rune{'a', '/', '📋'} {
		if drawShape(img, img.Bounds(), r, defaultFg, defaultBg) {
			t.Fatalf("drawShape(%q) = true, want false", r)
		}
	}
	if got := img.At(colorCellWidth-1, colorCellHeight-1); !sameColor(got, defaultFg) {
		t.Fatalf("full block corner = %v, want the foreground", got)
	}
}

// A braille cell draws its dots, so a chart shows in a snapshot without a
// braille font: ⠁ is the top-left dot only, ⢀ the bottom-right one.
func TestRenderCellsBrailleDots(t *testing.T) {
	img, err := renderCells(cellsOf("⠁⢀"))
	if err != nil {
		t.Fatal(err)
	}
	inked := func(x0, y0, x1, y1 int) bool {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if !sameColor(img.At(x, y), defaultBg) {
					return true
				}
			}
		}
		return false
	}
	w, h := colorCellWidth, colorCellHeight
	checks := []struct {
		name           string
		x0, y0, x1, y1 int
		ink            bool
	}{
		{"⠁ top left", 0, 0, w / 2, h / 4, true},
		{"⠁ bottom right", w / 2, 3 * h / 4, w, h, false},
		{"⢀ bottom right", w + w/2, 3 * h / 4, 2 * w, h, true},
		{"⢀ top left", w, 0, w + w/2, h / 4, false},
	}
	for _, c := range checks {
		if got := inked(c.x0, c.y0, c.x1, c.y1); got != c.ink {
			t.Errorf("%s: ink = %v, want %v", c.name, got, c.ink)
		}
	}
}

// TestRenderCellsDiamond: the rollout mark ◆ is drawn, inked at its center
// and blank in its corners, without the font having the glyph.
func TestRenderCellsDiamond(t *testing.T) {
	img, err := renderCells(cellsOf("◆"))
	if err != nil {
		t.Fatal(err)
	}
	w, h := colorCellWidth, colorCellHeight
	if sameColor(img.At(w/2, h/2), defaultBg) {
		t.Error("the center of ◆ is blank")
	}
	if !sameColor(img.At(0, 0), defaultBg) || !sameColor(img.At(w-1, h-1), defaultBg) {
		t.Error("the corners of ◆ are inked")
	}
}

func TestColorSnapshotToPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "frame.png")
	if err := colorSnapshotToPNG(cellsOf("ok"), "a caption", captionBottom, path); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0] != path {
		t.Fatalf("files written = %v, want only %s", matches, path)
	}
}

func TestDrawCaption(t *testing.T) {
	rows := make([]string, 10)
	for i := range rows {
		rows[i] = strings.Repeat("x", 80)
	}
	plain, err := renderCells(cellsOf(rows...))
	if err != nil {
		t.Fatal(err)
	}
	bounds := plain.Bounds()
	boxHeight := captionRows * colorCellHeight
	centerX := bounds.Dx() / 2

	t.Run("no caption keeps the frame unchanged", func(t *testing.T) {
		img, err := renderCells(cellsOf(rows...))
		if err != nil {
			t.Fatal(err)
		}
		if err := drawCaption(img, "", captionBottom); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(img.Pix, plain.Pix) {
			t.Fatal("an empty caption changed pixels")
		}
	})

	for _, tt := range []struct {
		position string
		boxTop   int
	}{
		{position: captionBottom, boxTop: bounds.Dy() - boxHeight},
		{position: captionTop, boxTop: 0},
	} {
		t.Run(tt.position, func(t *testing.T) {
			img, err := renderCells(cellsOf(rows...))
			if err != nil {
				t.Fatal(err)
			}
			if err := drawCaption(img, "Quick search", tt.position); err != nil {
				t.Fatal(err)
			}
			// The frame of the box is the accent color and its inside is
			// the caption background, apart from the text.
			if got := img.At(centerX, tt.boxTop); !sameColor(got, captionAccent) {
				t.Fatalf("top edge of the box at (%d,%d) = %v, want the accent", centerX, tt.boxTop, got)
			}
			if got := img.At(centerX, tt.boxTop+boxHeight-1); !sameColor(got, captionAccent) {
				t.Fatalf("bottom edge of the box = %v, want the accent", got)
			}
			bright, background := 0, 0
			for y := tt.boxTop + captionBorder; y < tt.boxTop+boxHeight-captionBorder; y++ {
				for x := centerX - 40; x < centerX+40; x++ {
					switch c := img.At(x, y); {
					case sameColor(c, captionFg):
						bright++
					case sameColor(c, captionBg):
						background++
					}
				}
			}
			if bright == 0 || background == 0 {
				t.Fatalf("inside the box: %d text pixels, %d background pixels; want both", bright, background)
			}
			// The box is centered and narrower than the frame, so the
			// sides of the frame stay as they were.
			for _, x := range []int{0, bounds.Dx() - 1} {
				y := tt.boxTop + boxHeight/2
				if got, want := img.At(x, y), plain.At(x, y); !sameColor(got, want) {
					t.Fatalf("pixel (%d,%d) beside the box changed: %v, want %v", x, y, got, want)
				}
			}
			// Rows outside the box are untouched.
			outside := boxHeight
			if tt.position == captionBottom {
				outside = 0
			}
			for x := range bounds.Dx() {
				if got, want := img.At(x, outside), plain.At(x, outside); !sameColor(got, want) {
					t.Fatalf("pixel (%d,%d) outside the box changed", x, outside)
				}
			}
		})
	}
}

func TestCaptionCommands(t *testing.T) {
	s := &session{captionAt: captionBottom}
	s.handleLine("caption   Cluster overview: what needs attention ", nil)
	if s.caption != "Cluster overview: what needs attention" {
		t.Fatalf("caption = %q", s.caption)
	}
	s.handleLine("caption-at top", nil)
	if s.captionAt != captionTop {
		t.Fatalf("captionAt = %q, want top", s.captionAt)
	}
	s.handleLine("caption-at middle", nil)
	if s.captionAt != captionTop {
		t.Fatalf("an invalid position changed captionAt to %q", s.captionAt)
	}
	s.handleLine("caption", nil)
	if s.caption != "" {
		t.Fatalf("a bare caption left %q", s.caption)
	}
}
