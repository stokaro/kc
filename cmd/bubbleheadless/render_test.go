package main

import (
	"image/color"
	"path/filepath"
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

func TestColorSnapshotToPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "frame.png")
	if err := colorSnapshotToPNG(cellsOf("ok"), path); err != nil {
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
