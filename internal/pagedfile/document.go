package pagedfile

import (
	"container/list"
	"slices"
)

// Sizes used when Options leaves them zero.
const (
	// DefaultBlockSize is the unit of a read: 256 KiB, one exec command
	// for a container file.
	DefaultBlockSize = 256 << 10
	// DefaultMaxBlocks bounds the cache: 16 blocks, 4 MiB.
	DefaultMaxBlocks = 16
	// DefaultSplitBytes is the split size of long lines (see lines.go), so
	// a shown line is at most 16 KiB.
	DefaultSplitBytes = 8 << 10
)

// Options sets the sizes of a Document.
type Options struct {
	BlockSize  int
	MaxBlocks  int
	SplitBytes int
}

func (o Options) withDefaults() Options {
	if o.BlockSize <= 0 {
		o.BlockSize = DefaultBlockSize
	}
	if o.MaxBlocks <= 0 {
		o.MaxBlocks = DefaultMaxBlocks
	}
	// A window spans up to five split sizes; two blocks must hold it.
	o.MaxBlocks = max(o.MaxBlocks, 3)
	if o.SplitBytes <= 0 {
		o.SplitBytes = DefaultSplitBytes
	}
	// A search chunk must hold more than one line (two split sizes).
	o.SplitBytes = max(1, min(o.SplitBytes, o.BlockSize/4))
	return o
}

type block struct {
	idx  int64
	data []byte
}

func blockOf(el *list.Element) *block {
	b, _ := el.Value.(*block)
	return b
}

// Document is the cached view of a Source: at most MaxBlocks blocks, least
// recently used first out, and the line index as far as blocks were read
// in order from the top. It does no I/O: the caller reads the blocks it
// names as missing and hands them to Put. It is not safe for concurrent
// use; the viewer owns it on the Bubble Tea update loop.
type Document struct {
	opts   Options
	size   int64
	blocks map[int64]*list.Element
	lru    *list.List
	// index[b] is the counter at offset b*BlockSize. It grows while the
	// blocks after it arrive in order.
	index []Counter
	// total is the number of lines once the index reached the end; -1
	// until then.
	total int64
}

// NewDocument returns an empty document of a file of size bytes.
func NewDocument(size int64, opts Options) *Document {
	return &Document{
		opts:   opts.withDefaults(),
		size:   max(size, 0),
		blocks: make(map[int64]*list.Element),
		lru:    list.New(),
		index:  []Counter{NewCounter()},
		total:  -1,
	}
}

// Size returns the size of the file as last seen.
func (d *Document) Size() int64 { return d.size }

// BlockSize returns the size of a block.
func (d *Document) BlockSize() int64 { return int64(d.opts.BlockSize) }

// SetSize records a new size of the file. Blocks past the end go, and the
// index forgets what a shrink invalidated.
func (d *Document) SetSize(size int64) {
	size = max(size, 0)
	if size == d.size {
		return
	}
	if size < d.size {
		for idx, el := range d.blocks {
			if idx*d.BlockSize() >= size {
				d.lru.Remove(el)
				delete(d.blocks, idx)
			}
		}
		d.truncateIndex(size / d.BlockSize())
	}
	if d.total >= 0 {
		// The last block changes when the file grows or shrinks.
		d.truncateIndex(min(size, d.size) / d.BlockSize())
	}
	d.size = size
}

// truncateIndex keeps the index for blocks before b and b itself.
func (d *Document) truncateIndex(b int64) {
	if int64(len(d.index)) > b+1 {
		d.index = d.index[:b+1]
	}
	d.total = -1
}

// Put stores block idx, read when the file was size bytes long. A block
// shorter than BlockSize ends the file. The index grows over the blocks
// that now follow it in order.
func (d *Document) Put(idx int64, data []byte, size int64) {
	bs := d.BlockSize()
	start := idx * bs
	switch {
	case int64(len(data)) < bs:
		size = start + int64(len(data))
	default:
		size = max(size, start+bs)
	}
	d.SetSize(size)
	if int64(len(data)) > bs {
		data = data[:bs]
	}
	if el, ok := d.blocks[idx]; ok {
		old := blockOf(el)
		if len(old.data) != len(data) && int64(len(d.index)) > idx+1 {
			d.truncateIndex(idx)
		}
		old.data = data
		d.lru.MoveToFront(el)
	} else {
		d.blocks[idx] = d.lru.PushFront(&block{idx: idx, data: data})
	}
	for d.lru.Len() > d.opts.MaxBlocks {
		el := d.lru.Back()
		d.lru.Remove(el)
		delete(d.blocks, blockOf(el).idx)
	}
	d.extendIndex()
}

// Drop forgets block idx, so the next access reads it again.
func (d *Document) Drop(idx int64) {
	if el, ok := d.blocks[idx]; ok {
		d.lru.Remove(el)
		delete(d.blocks, idx)
	}
}

func (d *Document) extendIndex() {
	split := int64(d.opts.SplitBytes)
	for d.total < 0 {
		b := int64(len(d.index) - 1)
		el, ok := d.blocks[b]
		if !ok {
			return
		}
		data := blockOf(el).data
		c := d.index[b]
		c.Feed(data, split)
		if c.Pos() >= d.size {
			d.total = c.Lines()
			return
		}
		if int64(len(data)) < d.BlockSize() {
			return // stale short block; it is read again
		}
		d.index = append(d.index, c)
	}
}

// AdoptIndex takes an index counted elsewhere (a scan of the whole file):
// index[b] is the counter at offset b*BlockSize, and total the number of
// lines, or -1 when the scan did not reach the end.
func (d *Document) AdoptIndex(index []Counter, total int64) {
	better := len(index) > len(d.index) || (total >= 0 && d.total < 0)
	if len(index) == 0 || !better {
		return
	}
	d.index = slices.Clone(index)
	d.total = total
	d.extendIndex()
}

// CachedBlocks returns the indexes of the cached blocks, most recently
// used first, and their size in bytes.
func (d *Document) CachedBlocks() (idx []int64, bytes int64) {
	for el := d.lru.Front(); el != nil; el = el.Next() {
		b := blockOf(el)
		idx = append(idx, b.idx)
		bytes += int64(len(b.data))
	}
	return idx, bytes
}

// window returns the bytes [lo, hi), clipped to the file, or the blocks
// that are missing for them. A cached block that ends before the bytes it
// should hold (the file grew) is missing too.
func (d *Document) window(lo, hi int64) (window, []int64) {
	lo = max(lo, 0)
	hi = min(hi, d.size)
	w := window{base: lo, size: d.size, split: int64(d.opts.SplitBytes)}
	if hi <= lo {
		return w, nil
	}
	bs := d.BlockSize()
	first, last := lo/bs, (hi-1)/bs
	var missing []int64
	parts := make([][]byte, 0, last-first+1)
	for b := first; b <= last; b++ {
		el, ok := d.blocks[b]
		if !ok {
			missing = append(missing, b)
			continue
		}
		d.lru.MoveToFront(el)
		data := blockOf(el).data
		need := min(hi, (b+1)*bs) - b*bs
		if int64(len(data)) < need {
			missing = append(missing, b)
			continue
		}
		parts = append(parts, data)
	}
	if len(missing) > 0 {
		return w, missing
	}
	from := lo - first*bs
	to := hi - last*bs
	if len(parts) == 1 {
		w.data = parts[0][from:to]
		return w, nil
	}
	buf := make([]byte, 0, hi-lo)
	for i, p := range parts {
		switch i {
		case 0:
			buf = append(buf, p[from:]...)
		case len(parts) - 1:
			buf = append(buf, p[:to]...)
		default:
			buf = append(buf, p...)
		}
	}
	w.data = buf
	return w, nil
}

// Line returns the text of the line that starts at s and the start of the
// next line (Size for the last one), or the blocks missing for them.
func (d *Document) Line(s int64) (text []byte, next int64, missing []int64) {
	if s >= d.size {
		return nil, d.size, nil
	}
	split := int64(d.opts.SplitBytes)
	w, missing := d.window(s-split-1, s+2*split+1)
	if missing != nil {
		return nil, 0, missing
	}
	next = w.nextStart(s)
	return w.lineText(s, next), next, nil
}

// Prev returns the start of the line before s (0 when s is the first), or
// the blocks missing for it. Prev(Size()) is the start of the last line.
func (d *Document) Prev(s int64) (int64, []int64) {
	s = min(s, d.size)
	if s <= 0 {
		return 0, nil
	}
	split := int64(d.opts.SplitBytes)
	w, missing := d.window(s-3*split-1, s)
	if missing != nil {
		return 0, missing
	}
	return w.prevStart(s), nil
}

// LineStartAt returns the first line start at or after off, or the blocks
// missing for it.
func (d *Document) LineStartAt(off int64) (int64, []int64) {
	if off <= 0 {
		return 0, nil
	}
	if off >= d.size {
		return d.size, nil
	}
	split := int64(d.opts.SplitBytes)
	w, missing := d.window(off-split-1, off+2*split+1)
	if missing != nil {
		return 0, missing
	}
	return w.firstStartFrom(off), nil
}

// LineNumber returns the zero-based number of the line that starts at s,
// when the index reaches it and its block is cached.
func (d *Document) LineNumber(s int64) (int64, bool) {
	b := s / d.BlockSize()
	if b >= int64(len(d.index)) {
		return 0, false
	}
	c := d.index[b]
	if rest := s - b*d.BlockSize(); rest > 0 {
		el, ok := d.blocks[b]
		if !ok {
			return 0, false
		}
		data := blockOf(el).data
		if int64(len(data)) < rest {
			return 0, false
		}
		c.Feed(data[:rest], int64(d.opts.SplitBytes))
	}
	return c.Lines(), true
}

// Lines returns the number of lines once the index reached the end.
func (d *Document) Lines() (int64, bool) { return d.total, d.total >= 0 }

// IndexedBytes returns how far from the top the line index reaches.
func (d *Document) IndexedBytes() int64 {
	if d.total >= 0 {
		return d.size
	}
	return int64(len(d.index)-1) * d.BlockSize()
}

// Split returns the split size of long lines.
func (d *Document) Split() int64 { return int64(d.opts.SplitBytes) }
