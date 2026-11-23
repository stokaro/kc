// Package kittygfx shows images with the Unicode placeholders of the
// Kitty graphics protocol: an image is sent once, as a virtual placement,
// and the screen shows it through ordinary text cells, the character
// U+10EEEE with diacritics for its row and column and the foreground
// color for its image ID. The cells go through the Bubble Tea renderer
// like any other text, so a redraw, a resize or an overlay drawn on top
// needs no extra care. Only the transmit and delete commands are raw
// output, and they do not depend on the cursor.
//
// See https://sw.kovidgoyal.net/kitty/graphics-protocol/#unicode-placeholders.
package kittygfx

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// Placeholder is the character a placeholder cell holds.
const Placeholder = '\U0010EEEE'

// chunkSize is the largest base64 payload of one command. Every chunk
// but the last must be a multiple of 4; 4096 is.
const chunkSize = 4096

// MaxCells is the most rows or columns one image can span: one per
// diacritic.
const MaxCells = len(diacritics)

// Diacritic is the combining mark that numbers row or column n, and false
// when n is outside [0, MaxCells).
func Diacritic(n int) (rune, bool) {
	if n < 0 || n >= MaxCells {
		return 0, false
	}
	return diacritics[n], true
}

// Encoder writes the raw commands; Tmux wraps each in the tmux DCS
// passthrough (tmux 3.3 or later with allow-passthrough on).
type Encoder struct {
	Tmux bool
}

// Transmit is the command that stores a PNG as image id and makes a
// virtual placement of cols by rows cells for it (a=T, U=1), without a
// reply (q=2). The payload goes in chunks of at most 4096 bytes; each
// chunk is its own command, so a tmux wrap stays small.
func (e Encoder) Transmit(id uint32, png []byte, cols, rows int) string {
	data := base64.StdEncoding.EncodeToString(png)
	var b strings.Builder
	b.Grow(len(data) + len(data)/chunkSize*32 + 64)
	first := "a=T,U=1,C=1,f=100,q=2,i=" + strconv.FormatUint(uint64(id), 10) +
		",c=" + strconv.Itoa(cols) + ",r=" + strconv.Itoa(rows)
	for i := 0; ; i += chunkSize {
		end := min(len(data), i+chunkSize)
		more := "0"
		if end < len(data) {
			more = "1"
		}
		keys := "m=" + more + ",q=2"
		if i == 0 {
			keys = first + ",m=" + more
		}
		b.WriteString(e.wrap(apc(keys, data[i:end])))
		if end == len(data) {
			break
		}
	}
	return b.String()
}

// Delete is the command that deletes image id and frees its data
// (a=d, d=I).
func (e Encoder) Delete(id uint32) string {
	return e.wrap(apc("a=d,d=I,q=2,i="+strconv.FormatUint(uint64(id), 10), ""))
}

func apc(keys, payload string) string {
	if payload == "" {
		return "\x1b_G" + keys + "\x1b\\"
	}
	return "\x1b_G" + keys + ";" + payload + "\x1b\\"
}

// wrap puts seq in a tmux passthrough: ESC P tmux; seq with every ESC
// doubled, ESC \.
func (e Encoder) wrap(seq string) string {
	if !e.Tmux {
		return seq
	}
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// Rows are the placeholder text of an image of cols by rows cells, one
// string per row, without the color: the caller sets the foreground to
// the image ID (see Color). Every cell carries its row and column
// diacritics, so any cell can be redrawn alone. Rows is nil when the size
// is out of range.
func Rows(cols, rows int) []string {
	if cols < 1 || rows < 1 || cols > MaxCells || rows > MaxCells {
		return nil
	}
	out := make([]string, rows)
	var b strings.Builder
	for r := range rows {
		b.Reset()
		for c := range cols {
			b.WriteRune(Placeholder)
			b.WriteRune(diacritics[r])
			b.WriteRune(diacritics[c])
		}
		out[r] = b.String()
	}
	return out
}

// Color is the SGR foreground of image id: the 256-color index, which
// carries an 8-bit ID. It survives a 256-color profile, which a 24-bit ID
// would not under tmux without true color. Callers keep IDs at 16 or
// above, away from the 16 colors a terminal theme redefines.
func Color(id uint8) string {
	return "\x1b[38;5;" + strconv.Itoa(int(id)) + "m"
}
