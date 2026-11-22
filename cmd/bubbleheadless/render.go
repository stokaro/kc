package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Cell size of a color snapshot. Go Mono advances 0.6 em, so 15 px gives a
// 9 px cell.
const (
	colorFontSize   = 15
	colorCellWidth  = 9
	colorCellHeight = 18
)

var (
	defaultFg = color.RGBA{R: 0xd0, G: 0xd0, B: 0xd0, A: 0xff}
	defaultBg = color.RGBA{R: 0x10, G: 0x10, B: 0x10, A: 0xff}
)

type colorFaces struct {
	regular font.Face
	bold    font.Face
}

var loadFaces = sync.OnceValues(func() (colorFaces, error) {
	regular, err := newFace(gomono.TTF)
	if err != nil {
		return colorFaces{}, err
	}
	bold, err := newFace(gomonobold.TTF)
	if err != nil {
		return colorFaces{}, err
	}
	return colorFaces{regular: regular, bold: bold}, nil
})

func newFace(ttf []byte) (font.Face, error) {
	f, err := opentype.Parse(ttf)
	if err != nil {
		return nil, fmt.Errorf("parse font: %w", err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: colorFontSize, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, fmt.Errorf("create font face: %w", err)
	}
	return face, nil
}

// cellColors returns the foreground and background color of a cell, with the
// reverse and faint attributes applied.
func cellColors(style uv.Style) (fg, bg color.Color) {
	fg, bg = defaultFg, defaultBg
	if style.Fg != nil {
		fg = style.Fg
	}
	if style.Bg != nil {
		bg = style.Bg
	}
	if style.Attrs&uv.AttrReverse != 0 {
		fg, bg = bg, fg
	}
	if style.Attrs&uv.AttrFaint != 0 {
		fg = blend(fg, bg, 0.5)
	}
	return fg, bg
}

// blend mixes a and b; t is the share of b.
func blend(a, b color.Color, t float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	mix := func(x, y uint32) uint8 {
		return uint8((float64(x)*(1-t) + float64(y)*t) / 257)
	}
	return color.RGBA{R: mix(ar, br), G: mix(ag, bg), B: mix(ab, bb), A: 0xff}
}

// renderCells draws a screen of cells with their colors. Box-drawing and
// block characters are drawn as shapes so that lines join across cells.
func renderCells(cells [][]uv.Cell) (*image.RGBA, error) {
	faces, err := loadFaces()
	if err != nil {
		return nil, err
	}
	cols := 1
	for _, row := range cells {
		cols = max(cols, len(row))
	}
	rows := max(len(cells), 1)
	img := image.NewRGBA(image.Rect(0, 0, cols*colorCellWidth, rows*colorCellHeight))
	draw.Draw(img, img.Bounds(), image.NewUniform(defaultBg), image.Point{}, draw.Src)

	metrics := faces.regular.Metrics()
	glyphHeight := (metrics.Ascent + metrics.Descent).Ceil()
	baseline := (colorCellHeight-glyphHeight)/2 + metrics.Ascent.Ceil()

	for y, row := range cells {
		for x := 0; x < len(row); x++ {
			cell := row[x]
			width := max(cell.Width, 1)
			fg, bg := cellColors(cell.Style)
			rect := image.Rect(x*colorCellWidth, y*colorCellHeight, (x+width)*colorCellWidth, (y+1)*colorCellHeight)
			draw.Draw(img, rect, image.NewUniform(bg), image.Point{}, draw.Src)
			content := cell.Content
			if content == "" || content == " " {
				x += width - 1
				continue
			}
			r := []rune(content)[0]
			if drawShape(img, rect, r, fg, bg) {
				x += width - 1
				continue
			}
			face := faces.regular
			if cell.Style.Attrs&uv.AttrBold != 0 {
				face = faces.bold
			}
			// Go Mono has no emoji; leave such cells blank rather than draw
			// the missing-glyph box.
			if _, ok := face.GlyphAdvance(r); ok {
				d := &font.Drawer{
					Dst:  img,
					Src:  image.NewUniform(fg),
					Face: face,
					Dot:  fixed.P(rect.Min.X, rect.Min.Y+baseline),
				}
				d.DrawString(content)
			}
			if cell.Style.Underline != uv.UnderlineNone {
				line := image.Rect(rect.Min.X, rect.Min.Y+baseline+2, rect.Max.X, rect.Min.Y+baseline+3)
				draw.Draw(img, line, image.NewUniform(fg), image.Point{}, draw.Src)
			}
			x += width - 1
		}
	}
	return img, nil
}

// Line weights of one arm of a box-drawing character.
const (
	armNone = iota
	armLight
	armHeavy
	armDouble
)

// boxArms lists the up, right, down and left arms of the box-drawing
// characters kc and common TUIs use.
var boxArms = map[rune][4]int{
	'─': {0, 1, 0, 1}, '━': {0, 2, 0, 2}, '│': {1, 0, 1, 0}, '┃': {2, 0, 2, 0},
	'┌': {0, 1, 1, 0}, '┐': {0, 0, 1, 1}, '└': {1, 1, 0, 0}, '┘': {1, 0, 0, 1},
	'╭': {0, 1, 1, 0}, '╮': {0, 0, 1, 1}, '╰': {1, 1, 0, 0}, '╯': {1, 0, 0, 1},
	'┏': {0, 2, 2, 0}, '┓': {0, 0, 2, 2}, '┗': {2, 2, 0, 0}, '┛': {2, 0, 0, 2},
	'├': {1, 1, 1, 0}, '┤': {1, 0, 1, 1}, '┬': {0, 1, 1, 1}, '┴': {1, 1, 0, 1}, '┼': {1, 1, 1, 1},
	'═': {0, 3, 0, 3}, '║': {3, 0, 3, 0},
	'╔': {0, 3, 3, 0}, '╗': {0, 0, 3, 3}, '╚': {3, 3, 0, 0}, '╝': {3, 0, 0, 3},
	'╠': {3, 3, 3, 0}, '╣': {3, 0, 3, 3}, '╦': {0, 3, 3, 3}, '╩': {3, 3, 0, 3}, '╬': {3, 3, 3, 3},
	'╒': {0, 3, 1, 0}, '╕': {0, 0, 1, 3}, '╘': {1, 3, 0, 0}, '╛': {1, 0, 0, 3},
	'╓': {0, 1, 3, 0}, '╖': {0, 0, 3, 1}, '╙': {3, 1, 0, 0}, '╜': {3, 0, 0, 1},
	'╞': {1, 3, 1, 0}, '╡': {1, 0, 1, 3}, '╤': {0, 3, 1, 3}, '╧': {1, 3, 0, 3},
	'╟': {3, 1, 3, 0}, '╢': {3, 0, 3, 1}, '╥': {0, 1, 3, 1}, '╨': {3, 1, 0, 1},
	'╪': {1, 3, 1, 3}, '╫': {3, 1, 3, 1},
}

// drawShape draws box-drawing and block characters. It reports false for
// any other rune.
func drawShape(img *image.RGBA, rect image.Rectangle, r rune, fg, bg color.Color) bool {
	if arms, ok := boxArms[r]; ok {
		drawBox(img, rect, arms, fg)
		return true
	}
	w, h := rect.Dx(), rect.Dy()
	fill := func(x0, y0, x1, y1 int, c color.Color) {
		draw.Draw(img, image.Rect(rect.Min.X+x0, rect.Min.Y+y0, rect.Min.X+x1, rect.Min.Y+y1), image.NewUniform(c), image.Point{}, draw.Src)
	}
	switch {
	case r == '█':
		fill(0, 0, w, h, fg)
	case r == '▀':
		fill(0, 0, w, h/2, fg)
	case r == '▐':
		fill(w/2, 0, w, h, fg)
	case r >= '▁' && r <= '▇': // lower one to seven eighths
		n := int(r-'▁') + 1
		fill(0, h-h*n/8, w, h, fg)
	case r >= '▉' && r <= '▏': // left seven to one eighths
		n := 8 - int(r-'▉') - 1
		fill(0, 0, (w*n+7)/8, h, fg)
	case r == '░':
		fill(0, 0, w, h, blend(bg, fg, 0.25))
	case r == '▒':
		fill(0, 0, w, h, blend(bg, fg, 0.5))
	case r == '▓':
		fill(0, 0, w, h, blend(bg, fg, 0.75))
	default:
		return false
	}
	return true
}

// drawBox draws the arms of a box-drawing character from the cell center.
// Double arms are two thin lines; at a corner or junction they are joined
// at the outer lines, which reads well at terminal sizes.
func drawBox(img *image.RGBA, rect image.Rectangle, arms [4]int, fg color.Color) {
	cx := rect.Min.X + rect.Dx()/2
	cy := rect.Min.Y + rect.Dy()/2
	src := image.NewUniform(fg)
	line := func(x0, y0, x1, y1 int) {
		draw.Draw(img, image.Rect(min(x0, x1), min(y0, y1), max(x0, x1), max(y0, y1)), src, image.Point{}, draw.Src)
	}
	const gap = 2
	up, right, down, left := arms[0], arms[1], arms[2], arms[3]
	vertical := func(weight, y0, y1 int) {
		switch weight {
		case armLight:
			line(cx, y0, cx+1, y1)
		case armHeavy:
			line(cx-1, y0, cx+1, y1)
		case armDouble:
			line(cx-gap, y0, cx-gap+1, y1)
			line(cx+gap, y0, cx+gap+1, y1)
		}
	}
	horizontal := func(weight, x0, x1 int) {
		switch weight {
		case armLight:
			line(x0, cy, x1, cy+1)
		case armHeavy:
			line(x0, cy-1, x1, cy+1)
		case armDouble:
			line(x0, cy-gap, x1, cy-gap+1)
			line(x0, cy+gap, x1, cy+gap+1)
		}
	}
	// Arms run from the edge to the far side of the center so that corners
	// close; a double arm reaches past the other arm's outer line.
	reach := func(other int) int {
		if other == armDouble {
			return gap + 1
		}
		return 1
	}
	vertical(up, rect.Min.Y, cy+reach(max(left, right)))
	vertical(down, cy-reach(max(left, right))+1, rect.Max.Y)
	horizontal(left, rect.Min.X, cx+reach(max(up, down)))
	horizontal(right, cx-reach(max(up, down))+1, rect.Max.X)
}

// colorSnapshotToPNG writes the screen with colors and real glyphs.
func colorSnapshotToPNG(cells [][]uv.Cell, path string) error {
	img, err := renderCells(cells)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	// Write next to the target and rename, so a script that waits for the
	// file never reads half of it.
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode png: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}
	return nil
}
