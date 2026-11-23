package kittygfx

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Fallback cell size in pixels until the terminal reports its own. The
// terminal scales an image to its cells, so a wrong guess only costs
// sharpness.
const (
	FallbackCellWidth  = 10
	FallbackCellHeight = 20
)

// maxPixels bounds the pixels of one image: on a large HiDPI window the
// reported cell size would make images of tens of megabytes to draw,
// compress and send every second.
const maxPixels = 1_500_000

// Images keeps the images of a fixed set of keys (one per chart) on the
// terminal. Each key owns two image IDs: the one shown, and the one the
// next picture goes to. The screen switches to the new ID only once its
// transmit is in the output buffer, so a frame never shows an ID whose
// pixels have not been sent, and a picture that comes back (a cursor
// moved back) switches without sending anything.
//
// Images is used from the Bubble Tea update goroutine only.
type Images struct {
	enc    Encoder
	slots  []slot
	used   map[uint8]bool // IDs that may hold data on the terminal
	gen    int
	cellW  int
	cellH  int
	png    png.Encoder
	pngBuf bytes.Buffer
}

type slot struct {
	ids    [2]uint8
	held   [2]picture // what each ID holds on the terminal
	shown  int        // index into ids; -1 when none is shown
	flight picture    // the newest transmit on its way, if any
}

// picture identifies the pixels and the cell size of an image.
type picture struct {
	hash       uint64
	cols, rows int
	ok         bool
}

// sentMsg is the tea.Raw payload of a transmit: printed, it is the
// command; handed back to Update, it says the command is in the output
// buffer, which Bubble Tea writes before the next frame of cells.
type sentMsg struct {
	seq      string
	gen, key int
	idx      int
	pic      picture
}

func (s sentMsg) String() string { return s.seq }

// NewImages returns the images of keys keys, with IDs from firstID up.
// firstID must be at least 16 and leave room for 2*keys IDs below 256.
func NewImages(enc Encoder, firstID uint8, keys int) *Images {
	im := &Images{enc: enc, used: make(map[uint8]bool)}
	im.png.CompressionLevel = png.BestSpeed
	id := firstID
	for range keys {
		im.slots = append(im.slots, slot{ids: [2]uint8{id, id + 1}, shown: -1})
		id += 2
	}
	return im
}

// IsMsg reports whether msg is one Images handles.
func IsMsg(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case uv.CellSizeEvent:
		return true
	case tea.RawMsg:
		_, ok := msg.Msg.(sentMsg)
		return ok
	}
	return false
}

// QueryCellSize asks the terminal for its cell size (CSI 16 t). The
// answer comes as an input event that Handle reads; nothing waits for it.
func QueryCellSize() tea.Cmd {
	return tea.Raw(ansi.WindowOp(16))
}

// Handle takes a message IsMsg accepts and reports whether the cell size
// changed.
func (im *Images) Handle(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case uv.CellSizeEvent:
		if msg.Width <= 0 || msg.Height <= 0 || (msg.Width == im.cellW && msg.Height == im.cellH) {
			return false
		}
		im.cellW, im.cellH = msg.Width, msg.Height
		return true
	case tea.RawMsg:
		s, ok := msg.Msg.(sentMsg)
		if !ok || s.gen != im.gen || s.key < 0 || s.key >= len(im.slots) {
			return false
		}
		sl := &im.slots[s.key]
		sl.held[s.idx], sl.shown = s.pic, s.idx
		if sl.flight == s.pic {
			sl.flight = picture{}
		}
	}
	return false
}

// CellSize is the cell size in pixels for an image of cols by rows: the
// terminal's, else the fallback, scaled down to stay under maxPixels.
func (im *Images) CellSize(cols, rows int) (w, h int) {
	w, h = im.cellW, im.cellH
	if w <= 0 || h <= 0 {
		w, h = FallbackCellWidth, FallbackCellHeight
	}
	for w > 2 && h > 2 && cols*rows*w*h > maxPixels {
		w, h = w*3/4, h*3/4
	}
	return w, h
}

// Show makes img, cols by rows cells, the picture of key. It returns the
// transmit command when the terminal does not have these pixels yet, and
// nil when it does or the same picture is on its way. A different picture
// goes out at once, to the same hidden ID. Bubble Tea writes a tea.Raw
// to the output just before Update sees it come back, so Handle records
// each ID's pictures in the order the terminal receives them.
func (im *Images) Show(key int, img *image.RGBA, cols, rows int) tea.Cmd {
	if key < 0 || key >= len(im.slots) || img == nil || cols < 1 || rows < 1 || cols > MaxCells || rows > MaxCells {
		return nil
	}
	sl := &im.slots[key]
	pic := picture{hash: hashImage(img), cols: cols, rows: rows, ok: true}
	if sl.shown >= 0 && sl.held[sl.shown] == pic {
		return nil
	}
	if sl.flight == pic {
		return nil
	}
	next := 0
	if sl.shown == 0 {
		next = 1
	}
	if sl.held[next] == pic {
		sl.shown = next
		return nil
	}
	im.pngBuf.Reset()
	if err := im.png.Encode(&im.pngBuf, img); err != nil {
		return nil
	}
	id := sl.ids[next]
	im.used[id] = true
	sl.flight = pic
	sl.held[next] = picture{}
	return tea.Raw(sentMsg{
		seq: im.enc.Transmit(uint32(id), im.pngBuf.Bytes(), cols, rows),
		gen: im.gen, key: key, idx: next, pic: pic,
	})
}

// Placeholders are the colored placeholder rows of key when the picture
// shown is cols by rows cells, and false when there is none of that size:
// the caller draws text instead.
func (im *Images) Placeholders(key, cols, rows int) ([]string, bool) {
	id, ok := im.Shown(key, cols, rows)
	if !ok {
		return nil, false
	}
	lines := Rows(cols, rows)
	color := Color(id)
	for i, l := range lines {
		lines[i] = color + l + "\x1b[39m"
	}
	return lines, true
}

// Shown is the image ID of key when the picture shown is cols by rows
// cells, and false when there is none of that size. A caller that mixes
// text into the picture draws its cells with Cells in Color(id).
func (im *Images) Shown(key, cols, rows int) (uint8, bool) {
	if key < 0 || key >= len(im.slots) {
		return 0, false
	}
	sl := im.slots[key]
	if sl.shown < 0 {
		return 0, false
	}
	pic := sl.held[sl.shown]
	if !pic.ok || pic.cols != cols || pic.rows != rows {
		return 0, false
	}
	return sl.ids[sl.shown], true
}

// Release forgets every picture and returns the commands that delete the
// images kc sent, or "" when it sent none. A transmit still on its way is
// covered: its ID is deleted too, and its arrival is ignored.
func (im *Images) Release() string {
	im.gen++
	for i := range im.slots {
		im.slots[i].held = [2]picture{}
		im.slots[i].shown, im.slots[i].flight = -1, picture{}
	}
	if len(im.used) == 0 {
		return ""
	}
	var b strings.Builder
	for _, sl := range im.slots {
		for _, id := range sl.ids {
			if im.used[id] {
				b.WriteString(im.enc.Delete(uint32(id)))
				delete(im.used, id)
			}
		}
	}
	return b.String()
}

func hashImage(img *image.RGBA) uint64 {
	h := fnv.New64a()
	b := img.Bounds()
	_, _ = fmt.Fprintf(h, "%dx%d;", b.Dx(), b.Dy())
	_, _ = h.Write(img.Pix)
	return h.Sum64()
}
