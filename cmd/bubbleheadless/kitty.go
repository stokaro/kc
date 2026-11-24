package main

// The part of the Kitty graphics protocol that kc uses, so that a color
// snapshot shows the chart images a real terminal shows:
//
//   - transmit (a=t, a=T) of PNG data (f=100) sent directly (t=d), in one
//     command or in chunks (m=1 on every chunk but the last);
//   - virtual placements for Unicode placeholders (U=1), with their size in
//     cells (c, r), made by a=T or a=p;
//   - delete by image ID (a=d with d=i or d=I).
//
// The emulator sends no replies: kc sends every command with q=2. Placement
// IDs are not modeled; an image has at most one virtual placement, the last
// one made. See https://sw.kovidgoyal.net/kitty/graphics-protocol/.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// maxImageData bounds the decoded data of one transmit, all chunks
// together. kc sends at most 1.5 million pixels per image.
const maxImageData = 48 << 20

// graphics holds the images the child sent and their virtual placements.
// The output filter runs commands on the PTY reader goroutine while a
// snapshot draws tiles, so every method locks.
type graphics struct {
	cellW, cellH int

	mu     sync.Mutex
	images map[uint32]*kittyImage
	upload *upload
}

type kittyImage struct {
	img image.Image
	// cols and rows are the size of the virtual placement in cells; 0
	// when the image has none.
	cols, rows int
	// box is img fit into the placement, cols*cellW by rows*cellH pixels,
	// made when a tile is first drawn.
	box *image.RGBA
}

// upload is a transmit that arrives in chunks: the keys of its first chunk
// and the data decoded so far.
type upload struct {
	keys gfxKeys
	data []byte
	// tooBig is set when the data passed maxImageData; the rest of the
	// chunks are read and dropped.
	tooBig bool
}

// newGraphics returns an empty store whose placements are cellW by cellH
// pixels per cell.
func newGraphics(cellW, cellH int) *graphics {
	return &graphics{cellW: cellW, cellH: cellH, images: make(map[uint32]*kittyImage)}
}

// gfxKeys are the control keys of a command: "a=T,i=7" gives a: T, i: 7.
type gfxKeys map[string]string

func parseKeys(s string) gfxKeys {
	keys := make(gfxKeys)
	for kv := range strings.SplitSeq(s, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			keys[k] = v
		}
	}
	return keys
}

// num is the value of an integer key, 0 when it is missing or not a
// number.
func (k gfxKeys) num(key string) int {
	n, err := strconv.Atoi(k[key])
	if err != nil {
		return 0
	}
	return n
}

// id is the image ID (i), 0 when it is missing or out of range.
func (k gfxKeys) id() uint32 {
	n, err := strconv.ParseUint(k["i"], 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// action is the a key; transmit (t) when it is missing.
func (k gfxKeys) action() string {
	if a := k["a"]; a != "" {
		return a
	}
	return "t"
}

// continues reports whether a command with these keys is the next chunk
// of a transmit: a chunk after the first carries only m and q.
func (k gfxKeys) continues() bool {
	for key := range k {
		if key != "m" && key != "q" {
			return false
		}
	}
	return true
}

// command runs one graphics command: the bytes between "ESC _ G" and
// "ESC \", control keys and payload separated by ';'.
func (g *graphics) command(body []byte) {
	ctrl, payload, _ := bytes.Cut(body, []byte{';'})
	keys := parseKeys(string(ctrl))
	g.mu.Lock()
	defer g.mu.Unlock()
	if up := g.upload; up != nil && keys.continues() {
		up.add(payload)
		if keys.num("m") != 1 {
			g.upload = nil
			g.transmitted(up)
		}
		return
	}
	// The protocol has the client finish a transmit before any other
	// command; a new command ends one it left unfinished.
	g.upload = nil
	switch keys.action() {
	case "t", "T":
		up := &upload{keys: keys}
		up.add(payload)
		if keys.num("m") == 1 {
			g.upload = up
			return
		}
		g.transmitted(up)
	case "p":
		if img := g.images[keys.id()]; img != nil && keys.num("U") == 1 {
			g.place(img, keys)
		}
	case "d":
		g.remove(keys)
	}
}

// add appends a chunk's data. Each chunk is decoded alone, as kitty does,
// so a client may pad every chunk.
func (up *upload) add(payload []byte) {
	if up.tooBig {
		return
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(string(payload), "="))
	if err != nil || len(up.data)+len(data) > maxImageData {
		up.tooBig = true
		up.data = nil
		return
	}
	up.data = append(up.data, data...)
}

// transmitted stores the image of a finished transmit, and its virtual
// placement for a=T,U=1. An image kc does not send (another format, a file
// or shared memory, compression) or data that does not decode leaves the
// store as it was.
func (g *graphics) transmitted(up *upload) {
	id := up.keys.id()
	if id == 0 || up.tooBig || up.keys["f"] != "100" || up.keys["o"] != "" || (up.keys["t"] != "" && up.keys["t"] != "d") {
		return
	}
	decoded, err := png.Decode(bytes.NewReader(up.data))
	if err != nil {
		return
	}
	img := &kittyImage{img: decoded}
	g.images[id] = img
	if up.keys.action() == "T" && up.keys.num("U") == 1 {
		g.place(img, up.keys)
	}
}

// place makes the virtual placement of img. A size left out follows from
// the image: both left out give the cells the pixels cover, one left out
// keeps the image's aspect ratio. Neither side exceeds the cells a
// placeholder can number.
func (g *graphics) place(img *kittyImage, keys gfxKeys) {
	b := img.img.Bounds()
	w, h := max(1, b.Dx()), max(1, b.Dy())
	cols, rows := keys.num("c"), keys.num("r")
	switch {
	case cols > 0 && rows > 0:
	case cols > 0:
		rows = ceilDiv(cols*g.cellW*h, w*g.cellH)
	case rows > 0:
		cols = ceilDiv(rows*g.cellH*w, h*g.cellW)
	default:
		cols, rows = ceilDiv(w, g.cellW), ceilDiv(h, g.cellH)
	}
	img.cols = min(max(1, cols), kittygfx.MaxCells)
	img.rows = min(max(1, rows), kittygfx.MaxCells)
	img.box = nil
}

// remove runs a delete. Only deletes by ID touch virtual placements: the
// other kinds go by screen position, which a virtual placement does not
// have. d=i keeps the image data for a later a=p; d=I frees it.
func (g *graphics) remove(keys gfxKeys) {
	id := keys.id()
	switch keys["d"] {
	case "i":
		if img := g.images[id]; img != nil {
			img.cols, img.rows, img.box = 0, 0, nil
		}
	case "I":
		delete(g.images, id)
	}
}

// drawTile draws cell (row, col) of the virtual placement of image id over
// rect of dst, which is one cell. It reports false, and draws nothing, when
// there is no such image, placement or cell: a terminal shows an empty cell
// there. A nil store has no images.
func (g *graphics) drawTile(dst *image.RGBA, rect image.Rectangle, id uint32, row, col int) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	img := g.images[id]
	if img == nil || row < 0 || col < 0 || row >= img.rows || col >= img.cols {
		return false
	}
	if img.box == nil {
		img.box = fitBox(img.img, img.cols*g.cellW, img.rows*g.cellH)
	}
	xdraw.Draw(dst, rect, img.box, image.Pt(col*g.cellW, row*g.cellH), xdraw.Over)
	return true
}

// describe lists the images by ID, one line each: the size in pixels, the
// virtual placement in cells, "scaled" when the pixels are not the
// placement at the snapshot's cell size, and "shown" for the IDs in shown.
// A script reads from it that the screen shows the child's images, drawn
// for the cell size the snapshot uses.
func (g *graphics) describe(shown map[uint32]bool) []string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	ids := slices.Sorted(maps.Keys(g.images))
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		img := g.images[id]
		b := img.img.Bounds()
		line := fmt.Sprintf("image %d: %dx%d px, no placement", id, b.Dx(), b.Dy())
		if img.cols > 0 {
			line = fmt.Sprintf("image %d: %dx%d px, placement %dx%d cells", id, b.Dx(), b.Dy(), img.cols, img.rows)
			if b.Dx() != img.cols*g.cellW || b.Dy() != img.rows*g.cellH {
				line += ", scaled"
			}
		}
		if shown[id] {
			line += ", shown"
		}
		lines = append(lines, line)
	}
	return lines
}

// fitBox scales img into a w by h box, keeping its aspect ratio and
// centering it; the rest of the box is transparent. An image of the box's
// size, which kc sends once it knows the cell size, is copied as it is.
func fitBox(img image.Image, w, h int) *image.RGBA {
	box := image.NewRGBA(image.Rect(0, 0, w, h))
	b := img.Bounds()
	if b.Dx() == w && b.Dy() == h {
		xdraw.Draw(box, box.Bounds(), img, b.Min, xdraw.Src)
		return box
	}
	sw, sh := w, h
	if b.Dx()*h > b.Dy()*w {
		sh = max(1, b.Dy()*w/b.Dx())
	} else {
		sw = max(1, b.Dx()*h/b.Dy())
	}
	x0, y0 := (w-sw)/2, (h-sh)/2
	xdraw.CatmullRom.Scale(box, image.Rect(x0, y0, x0+sw, y0+sh), img, b, xdraw.Src, nil)
	return box
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}
