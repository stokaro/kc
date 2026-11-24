package main

import (
	"fmt"
	"io"
)

// Bounds of what outputFilter holds of one sequence. A graphics command
// longer than maxCommand is read to its end and dropped; kc sends chunks of
// 4096 bytes.
const (
	maxCommand = maxImageData * 4 / 3
	maxCSI     = 32
)

const escByte = 0x1b

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
// It runs on the emulator's PTY reader goroutine only.
type outputFilter struct {
	src   io.Reader
	reply io.Writer
	gfx   *graphics
	// size is the terminal size in cells, for CSI 14 t.
	size func() (cols, rows int)

	state scanState
	// seq is the graphics command or the CSI parameters being read.
	seq     []byte
	dropSeq bool // seq passed its bound; the sequence is dropped
	buf     []byte
	out     []byte
	err     error
}

func newOutputFilter(src io.Reader, reply io.Writer, gfx *graphics, size func() (cols, rows int)) *outputFilter {
	return &outputFilter{src: src, reply: reply, gfx: gfx, size: size, buf: make([]byte, 32*1024)}
}

// Read returns the child's output without the graphics commands. It reads
// on until it has something to return, so a read that held only a command
// does not look like the end of the output.
func (f *outputFilter) Read(p []byte) (int, error) {
	for len(f.out) == 0 && f.err == nil {
		n, err := f.src.Read(f.buf)
		for _, b := range f.buf[:n] {
			f.step(b)
		}
		f.err = err
	}
	if len(f.out) == 0 {
		return 0, f.err
	}
	n := copy(p, f.out)
	if n == len(f.out) {
		f.out = f.out[:0]
	} else {
		f.out = f.out[n:]
	}
	return n, nil
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
