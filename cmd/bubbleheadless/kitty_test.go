package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// The tests are white-box: bubbleheadless is a main package.

var (
	red   = color.RGBA{R: 0xff, A: 0xff}
	green = color.RGBA{G: 0xff, A: 0xff}
	blue  = color.RGBA{B: 0xff, A: 0xff}
	white = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// quadrants is a w by h image whose four quadrants are red, green, blue
// and white, left to right, top to bottom.
func quadrants(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := red
			switch {
			case x >= w/2 && y < h/2:
				c = green
			case x < w/2 && y >= h/2:
				c = blue
			case x >= w/2 && y >= h/2:
				c = white
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// noise is a w by h image that compresses badly, so its PNG takes several
// chunks.
func noise(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = byte(seed >> 24)
		if i%4 == 3 {
			img.Pix[i] = 0xff
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// commandBodies splits graphics commands into what follows "ESC _ G" up to
// "ESC \" in each.
func commandBodies(t *testing.T, seq string) [][]byte {
	t.Helper()
	var bodies [][]byte
	for part := range strings.SplitSeq(seq, "\x1b\\") {
		if part == "" {
			continue
		}
		body, ok := strings.CutPrefix(part, "\x1b_G")
		if !ok {
			t.Fatalf("not a graphics command: %q", part)
		}
		bodies = append(bodies, []byte(body))
	}
	return bodies
}

func runCommands(g *graphics, bodies ...string) {
	for _, b := range bodies {
		g.command([]byte(b))
	}
}

// samePixels reports the first pixel where a and b differ.
func samePixels(t *testing.T, got, want image.Image) {
	t.Helper()
	if got.Bounds().Size() != want.Bounds().Size() {
		t.Fatalf("size %v, want %v", got.Bounds().Size(), want.Bounds().Size())
	}
	gb, wb := got.Bounds(), want.Bounds()
	for y := range wb.Dy() {
		for x := range wb.Dx() {
			if g, w := got.At(gb.Min.X+x, gb.Min.Y+y), want.At(wb.Min.X+x, wb.Min.Y+y); !sameColor(g, w) {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, g, w)
			}
		}
	}
}

func TestGraphicsAssemblesChunkedTransmit(t *testing.T) {
	t.Parallel()
	src := noise(64, 48)
	bodies := commandBodies(t, kittygfx.Encoder{}.Transmit(250, encodePNG(t, src), 7, 3))
	if len(bodies) < 3 {
		t.Fatalf("the transmit took %d commands; the test wants several chunks", len(bodies))
	}
	g := newGraphics(9, 18)
	for i, b := range bodies[:len(bodies)-1] {
		g.command(b)
		if g.images[250] != nil {
			t.Fatalf("the image is stored after chunk %d of %d", i+1, len(bodies))
		}
	}
	g.command(bodies[len(bodies)-1])
	img := g.images[250]
	if img == nil {
		t.Fatal("no image after the last chunk")
	}
	if img.cols != 7 || img.rows != 3 {
		t.Errorf("placement %dx%d, want 7x3", img.cols, img.rows)
	}
	if g.upload != nil {
		t.Error("the transmit is still open after m=0")
	}
	samePixels(t, img.img, src)
}

// A client may base64-encode each chunk alone, with its own padding;
// kitty decodes chunk by chunk.
func TestGraphicsDecodesEachChunkAlone(t *testing.T) {
	t.Parallel()
	src := quadrants(10, 6)
	data := encodePNG(t, src)
	// 25 bytes do not split into whole base64 quanta: each chunk ends in
	// padding.
	const chunk = 25
	if len(data) < 3*chunk {
		t.Fatalf("the PNG has %d bytes; the test wants at least three chunks", len(data))
	}
	g := newGraphics(9, 18)
	for i := 0; i < len(data); i += chunk {
		end := min(len(data), i+chunk)
		more := "m=1"
		if end == len(data) {
			more = "m=0"
		}
		keys := more
		if i == 0 {
			keys = "a=T,U=1,f=100,q=2,i=9,c=2,r=1," + more
		}
		g.command([]byte(keys + ";" + base64.StdEncoding.EncodeToString(data[i:end])))
	}
	if g.images[9] == nil {
		t.Fatal("no image")
	}
	samePixels(t, g.images[9].img, src)
}

func TestGraphicsCommandEndsUnfinishedTransmit(t *testing.T) {
	t.Parallel()
	data := base64.StdEncoding.EncodeToString(encodePNG(t, quadrants(4, 4)))
	g := newGraphics(9, 18)
	runCommands(g,
		"a=T,U=1,f=100,i=3,c=1,r=1,m=1;"+data[:8],
		"a=d,d=I,i=4",         // not a chunk: the transmit of 3 is dropped
		"m=0;"+data[8:],       // no transmit is open, so this does nothing
		"a=p,U=1,i=3,c=1,r=1", // there is no image 3 to place
	)
	if len(g.images) != 0 {
		t.Fatalf("images = %v, want none", g.images)
	}
}

func TestGraphicsIgnoresWhatKcDoesNotSend(t *testing.T) {
	t.Parallel()
	png64 := base64.StdEncoding.EncodeToString(encodePNG(t, quadrants(4, 4)))
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "raw RGBA, the default format", cmd: "a=T,U=1,i=5,s=4,v=4;" + png64},
		{name: "compressed", cmd: "a=T,U=1,f=100,o=z,i=5;" + png64},
		{name: "from a file", cmd: "a=T,U=1,f=100,t=f,i=5;" + png64},
		{name: "no image ID", cmd: "a=T,U=1,f=100;" + png64},
		{name: "not base64", cmd: "a=T,U=1,f=100,i=5;!!!!"},
		{name: "not a PNG", cmd: "a=T,U=1,f=100,i=5;" + base64.StdEncoding.EncodeToString([]byte("GIF89a"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := newGraphics(9, 18)
			runCommands(g, "a=T,U=1,f=100,i=5,c=1,r=1;"+png64)
			before := g.images[5]
			runCommands(g, tt.cmd)
			if g.images[5] != before {
				t.Fatal("the command replaced image 5")
			}
		})
	}
}

func TestGraphicsDelete(t *testing.T) {
	t.Parallel()
	png64 := base64.StdEncoding.EncodeToString(encodePNG(t, quadrants(4, 4)))
	tests := []struct {
		name       string
		cmds       []string
		wantImage  bool
		wantPlaced bool
	}{
		{name: "d=I frees the image", cmds: []string{"a=d,d=I,q=2,i=7"}},
		{name: "d=i keeps the data", cmds: []string{"a=d,d=i,i=7"}, wantImage: true},
		{name: "a=p places it again", cmds: []string{"a=d,d=i,i=7", "a=p,U=1,i=7,c=2,r=2"}, wantImage: true, wantPlaced: true},
		{name: "another ID", cmds: []string{"a=d,d=I,i=8"}, wantImage: true, wantPlaced: true},
		// Deletes by screen position never touch a virtual placement.
		{name: "d=a", cmds: []string{"a=d,d=a"}, wantImage: true, wantPlaced: true},
		{name: "d=A", cmds: []string{"a=d,d=A"}, wantImage: true, wantPlaced: true},
		{name: "a=d alone", cmds: []string{"a=d"}, wantImage: true, wantPlaced: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := newGraphics(9, 18)
			runCommands(g, "a=T,U=1,f=100,i=7,c=2,r=2;"+png64)
			runCommands(g, tt.cmds...)
			img := g.images[7]
			if (img != nil) != tt.wantImage {
				t.Fatalf("image kept = %v, want %v", img != nil, tt.wantImage)
			}
			if img != nil && (img.cols > 0) != tt.wantPlaced {
				t.Fatalf("placed = %v (%dx%d), want %v", img.cols > 0, img.cols, img.rows, tt.wantPlaced)
			}
		})
	}
}

func TestGraphicsPlacementSize(t *testing.T) {
	t.Parallel()
	// 18x36 pixels are 2x2 cells of 9x18.
	png64 := base64.StdEncoding.EncodeToString(encodePNG(t, quadrants(18, 36)))
	tests := []struct {
		keys       string
		cols, rows int
	}{
		{keys: "c=5,r=3", cols: 5, rows: 3},
		{keys: "", cols: 2, rows: 2},
		{keys: "c=4", cols: 4, rows: 4},
		{keys: "r=1", cols: 1, rows: 1},
		{keys: "c=1000,r=1000", cols: kittygfx.MaxCells, rows: kittygfx.MaxCells},
	}
	for _, tt := range tests {
		t.Run(tt.keys, func(t *testing.T) {
			t.Parallel()
			g := newGraphics(9, 18)
			runCommands(g, "a=T,U=1,f=100,i=1,"+tt.keys+";"+png64)
			img := g.images[1]
			if img == nil {
				t.Fatal("no image")
			}
			if img.cols != tt.cols || img.rows != tt.rows {
				t.Fatalf("placement %dx%d, want %dx%d", img.cols, img.rows, tt.cols, tt.rows)
			}
		})
	}
}

// tile draws cell (row, col) of image id on a blue cell and returns it.
func tile(t *testing.T, g *graphics, id uint32, row, col int) (*image.RGBA, bool) {
	t.Helper()
	dst := image.NewRGBA(image.Rect(0, 0, g.cellW, g.cellH))
	for i := 0; i < len(dst.Pix); i += 4 {
		copy(dst.Pix[i:], []byte{0, 0, 0xff, 0xff})
	}
	ok := g.drawTile(dst, dst.Bounds(), id, row, col)
	return dst, ok
}

func TestDrawTileCopiesAnImageOfTheBoxSize(t *testing.T) {
	t.Parallel()
	src := quadrants(18, 36)
	g := newGraphics(9, 18)
	runCommands(g, "a=T,U=1,f=100,i=2,c=2,r=2;"+base64.StdEncoding.EncodeToString(encodePNG(t, src)))
	for row := range 2 {
		for col := range 2 {
			got, ok := tile(t, g, 2, row, col)
			if !ok {
				t.Fatalf("no tile (%d,%d)", row, col)
			}
			samePixels(t, got, src.SubImage(image.Rect(col*9, row*18, col*9+9, row*18+18)))
		}
	}
	for _, c := range []struct {
		id       uint32
		row, col int
	}{{id: 2, row: 2}, {id: 2, col: 2}, {id: 2, row: -1}, {id: 3}} {
		if _, ok := tile(t, g, c.id, c.row, c.col); ok {
			t.Errorf("drawTile(%d, %d, %d) drew a cell the placement does not have", c.id, c.row, c.col)
		}
	}
	var none *graphics
	if none.drawTile(image.NewRGBA(image.Rect(0, 0, 9, 18)), image.Rect(0, 0, 9, 18), 2, 0, 0) {
		t.Error("a nil store drew a tile")
	}
}

// An image of another aspect ratio than its placement is fit into it and
// centered, as kitty does; the rest of the box shows the cell.
func TestDrawTileFitsAndCenters(t *testing.T) {
	t.Parallel()
	solid := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for i := 0; i < len(solid.Pix); i += 4 {
		copy(solid.Pix[i:], []byte{0xff, 0, 0, 0xff})
	}
	g := newGraphics(9, 18)
	// The box is 18x36; the 2:1 image fills its width, 9 pixels high,
	// from y 13 to 22.
	runCommands(g, "a=T,U=1,f=100,i=4,c=2,r=2;"+base64.StdEncoding.EncodeToString(encodePNG(t, solid)))
	for row := range 2 {
		got, ok := tile(t, g, 4, row, 0)
		if !ok {
			t.Fatalf("no tile in row %d", row)
		}
		for y := range 18 {
			want := blue
			if boxY := row*18 + y; boxY >= 13 && boxY < 22 {
				want = red
			}
			if c := got.At(4, y); !sameColor(c, want) {
				t.Fatalf("row %d, y %d: %v, want %v", row, y, c, want)
			}
		}
	}
}

func TestGraphicsDescribe(t *testing.T) {
	t.Parallel()
	png64 := base64.StdEncoding.EncodeToString(encodePNG(t, quadrants(18, 36)))
	g := newGraphics(9, 18)
	runCommands(g,
		"a=T,U=1,f=100,i=12,c=2,r=2;"+png64,
		"a=t,f=100,i=3;"+png64,
		// 18x36 pixels are neither 3x1 nor 2x1 cells of 9x18: a
		// terminal scales them.
		"a=T,U=1,f=100,i=40,c=3,r=1;"+png64,
		"a=T,U=1,f=100,i=41,c=2,r=1;"+png64,
	)
	got := strings.Join(g.describe(map[uint32]bool{12: true, 40: true, 99: true}), "\n")
	want := "image 3: 18x36 px, no placement\n" +
		"image 12: 18x36 px, placement 2x2 cells, shown\n" +
		"image 40: 18x36 px, placement 3x1 cells, scaled, shown\n" +
		"image 41: 18x36 px, placement 2x1 cells, scaled"
	if got != want {
		t.Fatalf("describe =\n%s\nwant\n%s", got, want)
	}
	var none *graphics
	if lines := none.describe(nil); len(lines) != 0 {
		t.Fatalf("a nil store describes %q", lines)
	}
}
