package logmux

import (
	"hash/fnv"
	"io"

	"charm.land/lipgloss/v2"
)

// palette holds the prefix colors. They are the basic ANSI colors, so they
// follow the terminal's color scheme; red and white are left out because
// they read as errors or as plain text.
var palette = []string{"6", "2", "5", "3", "4", "14", "10", "13", "11", "12"}

// Color returns the ANSI color of a target's prefix. It depends only on the
// `pod/container` name, so a target keeps its color across restarts and
// across viewers.
func Color(t Target) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name()))
	return palette[h.Sum32()%uint32(len(palette))]
}

// Prefix returns the plain prefix of a line from t: `[pod/container]`.
func Prefix(t Target) string { return "[" + t.Name() + "]" }

// Format renders a line as it is shown: a container line gets its prefix,
// colored when color is true; a notice is prefixed with `[kc]`.
func Format(l Line, color bool) string {
	if l.Notice {
		return "[kc] " + l.Text
	}
	prefix := Prefix(l.Target)
	if color {
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color(Color(l.Target))).Render(prefix)
	}
	return prefix + " " + l.Text
}

// Reader exposes the merged stream as text, one formatted line per line, so
// a viewer that reads a single container log can read it unchanged. Close
// closes the mux.
type Reader struct {
	mux    *Mux
	format func(Line) string
	buf    []byte
}

// NewReader returns a reader over m's lines, formatted with format.
func NewReader(m *Mux, format func(Line) string) *Reader {
	return &Reader{mux: m, format: format}
}

// Read blocks until a line arrives. It returns io.EOF once the mux is
// closed.
func (r *Reader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		select {
		case l, ok := <-r.mux.Lines():
			if !ok {
				return 0, io.EOF
			}
			r.buf = append(r.buf[:0], r.format(l)...)
			r.buf = append(r.buf, '\n')
		case <-r.mux.Done():
			return 0, io.EOF
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// Close closes the mux and with it every stream.
func (r *Reader) Close() error {
	r.mux.Close()
	return nil
}
