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
// Images is used from the Bubble Tea update goroutine only; Job.Encode is
// the part that may run elsewhere.
type Images struct {
	enc   Encoder
	slots []slot
	used  map[uint8]bool // IDs that may hold data on the terminal
	gen   int
	cellW int
	cellH int
}

type slot struct {
	ids    [2]uint8
	held   [2]picture // what each ID holds on the terminal
	shown  int        // index into ids; -1 when none is shown
	flight picture    // the newest transmit on its way, if any
	// encoding is set from Begin to Finish or Drop; again records a
	// Begin refused meanwhile. Release keeps both: the job still runs.
	encoding, again bool
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

// Job is one picture of a key, begun by Begin on the update goroutine.
// Its Encode runs on any goroutine, so the raster and the PNG stay off the
// event loop; Finish or Drop takes the result back on the update
// goroutine.
type Job struct {
	key, cols, rows int
	gen             int
	cellW, cellH    int
	// known are the pictures the terminal holds or has on its way when
	// the job began: one of those needs no PNG.
	known [3]picture
}

// Key, Cols and Rows are the key and the cell size of the picture.
func (j Job) Key() int  { return j.key }
func (j Job) Cols() int { return j.cols }
func (j Job) Rows() int { return j.rows }

// CellSize is the pixel size of a cell for the raster.
func (j Job) CellSize() (w, h int) { return j.cellW, j.cellH }

// Picture is the result of a Job: its pixels' hash and, when the terminal
// may not have them, the PNG.
type Picture struct {
	job Job
	pic picture
	png []byte
}

// Job is the job p is the result of.
func (p Picture) Job() Job { return p.job }

// Begin starts a picture of key at cols by rows cells. It is false while
// a picture of key is still being encoded, so at most one is in flight per
// key; Finish or Drop then reports that the key was asked for again.
func (im *Images) Begin(key, cols, rows int) (Job, bool) {
	if key < 0 || key >= len(im.slots) || cols < 1 || rows < 1 || cols > MaxCells || rows > MaxCells {
		return Job{}, false
	}
	sl := &im.slots[key]
	if sl.encoding {
		sl.again = true
		return Job{}, false
	}
	sl.encoding, sl.again = true, false
	w, h := im.CellSize(cols, rows)
	return Job{
		key: key, cols: cols, rows: rows, gen: im.gen, cellW: w, cellH: h,
		known: [3]picture{sl.held[0], sl.held[1], sl.flight},
	}, true
}

// Encode hashes img and, unless the terminal holds these pixels already,
// compresses them. It touches no state of Images: any goroutine may run
// it.
func (j Job) Encode(img *image.RGBA) Picture {
	p := Picture{job: j}
	if img == nil {
		return p
	}
	p.pic = picture{hash: hashImage(img), cols: j.cols, rows: j.rows, ok: true}
	for _, k := range j.known {
		if k == p.pic {
			return p
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		p.pic = picture{}
		return p
	}
	p.png = buf.Bytes()
	return p
}

// Drop ends the job of p without showing it (its size is no longer the
// plot's) and reports whether its key was asked for again meanwhile.
func (im *Images) Drop(p Picture) (again bool) {
	key := p.job.key
	if key < 0 || key >= len(im.slots) {
		return false
	}
	sl := &im.slots[key]
	again = sl.again
	sl.encoding, sl.again = false, false
	return again
}

// Finish ends the job of p and makes p the picture of its key. It returns
// the transmit command when the terminal does not have these pixels yet,
// and nil when it does, the same picture is on its way, or p began before
// a Release. A different picture goes out at once, to the hidden ID.
// Bubble Tea writes a tea.Raw to the output just before Update sees it
// come back, so Handle records each ID's pictures in the order the
// terminal receives them. again reports that the key was asked for while
// p was being encoded.
func (im *Images) Finish(p Picture) (cmd tea.Cmd, again bool) {
	again = im.Drop(p)
	if p.job.gen != im.gen || !p.pic.ok || p.job.key < 0 || p.job.key >= len(im.slots) {
		return nil, again
	}
	sl := &im.slots[p.job.key]
	pic := p.pic
	if sl.shown >= 0 && sl.held[sl.shown] == pic {
		return nil, again
	}
	if sl.flight == pic {
		return nil, again
	}
	next := 0
	if sl.shown == 0 {
		next = 1
	}
	if sl.held[next] == pic {
		sl.shown = next
		return nil, again
	}
	if p.png == nil {
		// The terminal held these pixels when the job began, but no
		// longer: the next picture sends them.
		return nil, true
	}
	id := sl.ids[next]
	im.used[id] = true
	sl.flight = pic
	sl.held[next] = picture{}
	return tea.Raw(sentMsg{
		seq: im.enc.Transmit(uint32(id), p.png, p.job.cols, p.job.rows),
		gen: im.gen, key: p.job.key, idx: next, pic: pic,
	}), again
}

// Show is Begin, Encode and Finish at once, on the calling goroutine:
// img, cols by rows cells, becomes the picture of key.
func (im *Images) Show(key int, img *image.RGBA, cols, rows int) tea.Cmd {
	if img == nil {
		return nil
	}
	job, ok := im.Begin(key, cols, rows)
	if !ok {
		return nil
	}
	cmd, _ := im.Finish(job.Encode(img))
	return cmd
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
