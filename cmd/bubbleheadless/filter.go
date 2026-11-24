package main

import (
	"fmt"
	"io"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Bounds of what outputFilter holds of one sequence. A graphics command
// longer than maxCommand is read to its end and dropped; kc sends chunks of
// 4096 bytes.
const (
	maxCommand = maxImageData * 4 / 3
	maxCSI     = 32
)

const escByte = 0x1b

// The emulator ends a grapheme cluster at the end of every write, so a
// placeholder whose diacritics arrive in the next write loses them. The
// filter returns whole clusters: it holds the last cluster of the output
// until more output says it is whole, or until the output pauses for
// holdFor. maxHold bounds what it holds of one cluster.
const (
	holdFor = 20 * time.Millisecond
	maxHold = 256
)

type scanState int

const (
	scanGround      scanState = iota
	scanEsc                   // after ESC
	scanAPC                   // after ESC _
	scanGraphics              // in ESC _ G ...
	scanGraphicsEsc           // after an ESC in a graphics command
	scanCSI                   // in ESC [ ...
)

// outputFilter is what the emulator reads instead of the PTY. It takes the
// Kitty graphics commands (ESC _ G ... ESC \) out of the child's output and
// runs them on gfx, and it answers the size queries the emulator ignores,
// CSI 16 t (cell size) and CSI 14 t (text area size), in the pixels of a
// color snapshot, so kc draws its images at the size the snapshot shows
// them. All other bytes, the queries included, pass through unchanged.
//
// Read never ends in the middle of a grapheme cluster (see holdFor).
//
// Read runs on the emulator's reader goroutine only; a goroutine of the
// filter reads the source until it fails or Close is called.
type outputFilter struct {
	reply io.Writer
	gfx   *graphics
	// size is the terminal size in cells, for CSI 14 t.
	size    func() (cols, rows int)
	holdFor time.Duration

	reads     chan readResult
	done      chan struct{}
	closeOnce sync.Once

	state scanState
	// seq is the graphics command or the CSI parameters being read.
	seq     []byte
	dropSeq bool // seq passed its bound; the sequence is dropped
	out     []byte
	// hold is how many bytes at the end of out wait for the output that
	// may continue their grapheme cluster.
	hold int
	err  error
}

type readResult struct {
	data []byte
	err  error
}

func newOutputFilter(src io.Reader, reply io.Writer, gfx *graphics, size func() (cols, rows int)) *outputFilter {
	f := &outputFilter{
		reply:   reply,
		gfx:     gfx,
		size:    size,
		holdFor: holdFor,
		reads:   make(chan readResult),
		done:    make(chan struct{}),
	}
	go f.readLoop(src)
	return f
}

// readLoop reads the source until it fails, so Read can stop waiting for
// output that does not come. It reads into two buffers by turns: Read is
// done with one before it takes the read after it.
func (f *outputFilter) readLoop(src io.Reader) {
	bufs := [2][]byte{make([]byte, 32*1024), make([]byte, 32*1024)}
	for k := 0; ; k = 1 - k {
		n, err := src.Read(bufs[k])
		select {
		case f.reads <- readResult{data: bufs[k][:n], err: err}:
		case <-f.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// Close ends the read loop once its read of the source returns; closing
// the source makes that happen.
func (f *outputFilter) Close() error {
	f.closeOnce.Do(func() { close(f.done) })
	return nil
}

// Read returns the child's output without the graphics commands, in whole
// grapheme clusters. It reads on until it has something to return, so a
// read that held only a command does not look like the end of the output.
func (f *outputFilter) Read(p []byte) (int, error) {
	for {
		if n := f.ready(len(p)); n > 0 {
			copy(p, f.out[:n])
			f.out = f.out[n:]
			return n, nil
		}
		if len(f.out) > 0 && f.err != nil {
			// No more output can continue the held cluster.
			f.hold = 0
			continue
		}
		if f.err != nil {
			return 0, f.err
		}
		r, ok := f.wait()
		if !ok {
			// The output paused: the held cluster is whole.
			f.hold = 0
			continue
		}
		for _, b := range r.data {
			f.step(b)
		}
		f.hold = f.pending()
		f.err = r.err
	}
}

// wait returns the next read of the source; with bytes held, it waits
// holdFor at most and reports false when nothing came.
func (f *outputFilter) wait() (readResult, bool) {
	if f.hold == 0 {
		return <-f.reads, true
	}
	timer := time.NewTimer(f.holdFor)
	defer timer.Stop()
	select {
	case r := <-f.reads:
		return r, true
	case <-timer.C:
		return readResult{}, false
	}
}

// ready is how many bytes Read returns into a buffer of size bytes: not
// the held bytes, and not part of a cluster that goes on past the buffer.
func (f *outputFilter) ready(size int) int {
	n := len(f.out) - f.hold
	if n <= 0 {
		return 0
	}
	if size >= n {
		return n
	}
	return clusterCut(f.out, size)
}

// pending is how many bytes at the end of the output may be a grapheme
// cluster that the next read continues: the last non-ASCII rune that is
// no mark, with the marks and the cut rune after it, or else the cut rune
// alone. The emulator draws ASCII at once, and the bytes before an escape
// sequence are whole.
func (f *outputFilter) pending() int {
	if f.state != scanGround {
		return 0
	}
	end := len(f.out)
	cut := end // the start of a rune the read cut, if any
	for k := 1; k < utf8.UTFMax && k <= end; k++ {
		c := f.out[end-k]
		if c < utf8.RuneSelf {
			break
		}
		if utf8.RuneStart(c) {
			if !utf8.FullRune(f.out[end-k:]) {
				cut = end - k
			}
			break
		}
	}
	for i := cut; i > 0 && end-i < maxHold; {
		r, size := utf8.DecodeLastRune(f.out[:i])
		if r < utf8.RuneSelf || r == utf8.RuneError {
			break
		}
		i -= size
		if prev, _ := utf8.DecodeLastRune(f.out[:i]); !extendsCluster(r) && prev != zeroWidthJoiner {
			return end - i
		}
	}
	return end - cut
}

// clusterCut is the largest n <= size where b can be cut between grapheme
// clusters, or size when no such place is near it.
func clusterCut(b []byte, size int) int {
	for n := size; n > 0 && size-n < maxHold; n-- {
		if !utf8.RuneStart(b[n]) {
			continue
		}
		r, _ := utf8.DecodeRune(b[n:])
		prev, _ := utf8.DecodeLastRune(b[:n])
		if !extendsCluster(r) && prev != zeroWidthJoiner {
			return n
		}
	}
	return size
}

const zeroWidthJoiner = 0x200D

// extendsCluster reports whether r belongs to the grapheme cluster before
// it: marks (kitty's placeholder diacritics among them), a joiner,
// variation selectors and emoji skin tones. A rune after a joiner belongs
// to the cluster before the joiner too.
func extendsCluster(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc, unicode.Variation_Selector) ||
		r == zeroWidthJoiner || r >= 0x1F3FB && r <= 0x1F3FF
}

// step reads one byte of output. An ESC is held until the next byte says
// what it starts.
func (f *outputFilter) step(b byte) {
	switch f.state {
	case scanGround:
		if b == escByte {
			f.state = scanEsc
			return
		}
		f.out = append(f.out, b)
	case scanEsc:
		switch b {
		case '_':
			f.state = scanAPC
		case '[':
			f.out = append(f.out, escByte, b)
			f.begin(scanCSI)
		case escByte:
			f.out = append(f.out, escByte)
		default:
			f.out = append(f.out, escByte, b)
			f.state = scanGround
		}
	case scanAPC:
		if b == 'G' {
			f.begin(scanGraphics)
			return
		}
		// Another APC: it passes, and the emulator ignores it.
		f.out = append(f.out, escByte, '_')
		f.state = scanGround
		f.step(b)
	case scanGraphics:
		if b == escByte {
			f.state = scanGraphicsEsc
			return
		}
		f.keep(b, maxCommand)
	case scanGraphicsEsc:
		if b == '\\' {
			if !f.dropSeq {
				f.gfx.command(f.seq)
			}
			f.state = scanGround
			return
		}
		// An ESC that is not the string terminator cancels the command
		// and starts a new sequence, as in a VT parser.
		f.state = scanEsc
		f.step(b)
	case scanCSI:
		f.stepCSI(b)
	}
}

func (f *outputFilter) stepCSI(b byte) {
	switch {
	case b == escByte:
		f.state = scanEsc
	case b >= 0x20 && b <= 0x3f: // parameters and intermediates
		f.out = append(f.out, b)
		f.keep(b, maxCSI)
	case b >= 0x40 && b <= 0x7e: // final byte
		f.out = append(f.out, b)
		f.state = scanGround
		if b == 't' && !f.dropSeq {
			f.answer(string(f.seq))
		}
	default: // a control character inside the sequence acts on its own
		f.out = append(f.out, b)
	}
}

func (f *outputFilter) begin(s scanState) {
	f.state = s
	f.seq = f.seq[:0]
	f.dropSeq = false
}

func (f *outputFilter) keep(b byte, limit int) {
	if f.dropSeq {
		return
	}
	if len(f.seq) >= limit {
		f.dropSeq = true
		f.seq = f.seq[:0]
		return
	}
	f.seq = append(f.seq, b)
}

// answer replies to a window report request (CSI Ps t) the snapshot can
// answer. A failed write means the child is gone; nothing waits for it.
func (f *outputFilter) answer(params string) {
	switch params {
	case "16":
		_, _ = fmt.Fprintf(f.reply, "\x1b[6;%d;%dt", f.gfx.cellH, f.gfx.cellW)
	case "14":
		cols, rows := f.size()
		_, _ = fmt.Fprintf(f.reply, "\x1b[4;%d;%dt", rows*f.gfx.cellH, cols*f.gfx.cellW)
	}
}
