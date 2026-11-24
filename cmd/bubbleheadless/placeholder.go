package main

import (
	"image/color"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// placeholder is where a Unicode placeholder cell points: an image ID and a
// cell of that image's virtual placement.
type placeholder struct {
	id       uint32
	row, col int
}

// diacriticIndex maps each row and column diacritic to the number it
// stands for; it is kittygfx's table read backwards, so kc's encoder and
// this decoder cannot disagree on it.
var diacriticIndex = sync.OnceValue(func() map[rune]int {
	index := make(map[rune]int, kittygfx.MaxCells)
	for n := range kittygfx.MaxCells {
		if r, ok := kittygfx.Diacritic(n); ok {
			index[r] = n
		}
	}
	return index
})

// readPlaceholder reads a cell that holds U+10EEEE: the image ID from the
// foreground color, then the row, the column and the most significant byte
// of the ID from up to three diacritics. It reports false for any other
// cell.
//
// Diacritics may be left out; left, the decoded cell just before this one
// (nil when that is no placeholder), then fills them in as kitty does. With
// none, the cell continues left's row; with the row only, and the same row
// as left, it takes the next column; with row and column following left's,
// it takes left's high byte. Anything still missing is 0.
func readPlaceholder(content string, fg color.Color, left *placeholder) (placeholder, bool) {
	rest, ok := strings.CutPrefix(content, string(kittygfx.Placeholder))
	if !ok {
		return placeholder{}, false
	}
	var p placeholder
	index := diacriticIndex()
	var marks [3]int
	n := 0
	for _, r := range rest {
		v, isMark := index[r]
		if !isMark || n == len(marks) {
			break
		}
		marks[n] = v
		n++
	}
	p.id = colorID(fg)
	same := left != nil && left.id&0xFFFFFF == p.id
	switch {
	case same && n == 0:
		p.row, p.col = left.row, left.col+1
	case same && n == 1 && marks[0] == left.row:
		p.row, p.col = marks[0], left.col+1
	case same && n == 2 && marks[0] == left.row && marks[1] == left.col+1:
		p.row, p.col = marks[0], marks[1]
	default:
		p.row, p.col = marks[0], marks[1]
		if n == 3 {
			p.id |= uint32(marks[2]&0xFF) << 24
		}
		return p, true
	}
	p.id |= left.id &^ 0xFFFFFF
	return p, true
}

// shownImages is the set of image IDs the placeholder cells of a screen
// name.
func shownImages(cells [][]uv.Cell) map[uint32]bool {
	shown := make(map[uint32]bool)
	for _, row := range cells {
		var left *placeholder
		for _, c := range row {
			p, ok := readPlaceholder(c.Content, c.Style.Fg, left)
			if !ok {
				left = nil
				continue
			}
			shown[p.id] = true
			left = &p
		}
	}
	return shown
}

// colorID is the image ID a foreground color carries: the index of a
// 16- or 256-color, or the 24 bits of a true color. The default color
// carries 0, which is no image.
func colorID(c color.Color) uint32 {
	switch c := c.(type) {
	case nil:
		return 0
	case ansi.BasicColor:
		return uint32(c)
	case ansi.IndexedColor:
		return uint32(c)
	}
	r, g, b, _ := c.RGBA()
	return (r>>8)<<16 | (g>>8)<<8 | b>>8
}
