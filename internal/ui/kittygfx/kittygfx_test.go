package kittygfx_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

func TestDiacritics(t *testing.T) {
	t.Parallel()
	// The ends of kitty's rowcolumn-diacritics.txt.
	for n, want := range map[int]rune{0: 0x0305, 1: 0x030D, 2: 0x030E, 9: 0x034A, 296: 0x1D244} {
		got, ok := kittygfx.Diacritic(n)
		if !ok || got != want {
			t.Errorf("Diacritic(%d) = %U, %v; want %U", n, got, ok, want)
		}
	}
	if kittygfx.MaxCells != 297 {
		t.Errorf("MaxCells = %d, want 297", kittygfx.MaxCells)
	}
	for _, n := range []int{-1, kittygfx.MaxCells} {
		if _, ok := kittygfx.Diacritic(n); ok {
			t.Errorf("Diacritic(%d) is valid", n)
		}
	}
	seen := make(map[rune]int)
	for n := range kittygfx.MaxCells {
		d, _ := kittygfx.Diacritic(n)
		if prev, dup := seen[d]; dup {
			t.Fatalf("diacritic %U stands for %d and %d", d, prev, n)
		}
		seen[d] = n
	}
}

func TestRowsEncodeRowAndColumnInEveryCell(t *testing.T) {
	t.Parallel()
	rows := kittygfx.Rows(3, 2)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	for r, line := range rows {
		cells := []rune(line)
		if len(cells) != 9 {
			t.Fatalf("row %d has %d runes, want 3 cells of 3", r, len(cells))
		}
		for c := range 3 {
			row, _ := kittygfx.Diacritic(r)
			col, _ := kittygfx.Diacritic(c)
			got := cells[3*c : 3*c+3]
			if got[0] != kittygfx.Placeholder || got[1] != row || got[2] != col {
				t.Errorf("cell (%d,%d) = %U, want %U %U %U", r, c, got, kittygfx.Placeholder, row, col)
			}
		}
	}
	for _, size := range [][2]int{{0, 1}, {1, 0}, {kittygfx.MaxCells + 1, 1}, {1, kittygfx.MaxCells + 1}} {
		if got := kittygfx.Rows(size[0], size[1]); got != nil {
			t.Errorf("Rows(%d, %d) = %d rows, want nil", size[0], size[1], len(got))
		}
	}
}

// TestPlaceholdersAreOneCellEach draws placeholder rows the way Bubble Tea
// does (an ultraviolet styled string into a screen buffer) with both width
// methods, and measures them as lipgloss and ansi do. Every diacritic must
// stay with its placeholder in one cell of width 1, or the plot would
// shift the frame and the image would read the wrong row or column.
func TestPlaceholdersAreOneCellEach(t *testing.T) {
	t.Parallel()
	n := kittygfx.MaxCells
	for _, size := range [][2]int{{n, 2}, {2, n}} {
		cols, rows := size[0], size[1]
		lines := kittygfx.Rows(cols, rows)
		for r, line := range lines {
			styled := kittygfx.Color(240) + line + "\x1b[39m|"
			if w := lipgloss.Width(styled); w != cols+1 {
				t.Fatalf("%dx%d row %d: lipgloss width %d, want %d", cols, rows, r, w, cols+1)
			}
			if w := ansi.StringWidthWc(styled); w != cols+1 {
				t.Fatalf("%dx%d row %d: wcwidth %d, want %d", cols, rows, r, w, cols+1)
			}
			if got := ansi.Strip(ansi.Truncate(styled, 1, "")); got != string([]rune(line)[:3]) {
				t.Fatalf("%dx%d row %d: Truncate(1) = %q", cols, rows, r, got)
			}
			for _, m := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
				buf := uv.NewScreenBuffer(cols+1, 1)
				buf.Method = m
				uv.NewStyledString(styled).Draw(buf, buf.Bounds())
				cells := []rune(line)
				for c := range cols {
					cell := buf.CellAt(c, 0)
					if cell == nil || cell.Width != 1 || cell.Content != string(cells[3*c:3*c+3]) {
						t.Fatalf("method %v, %dx%d row %d col %d: cell %+v", m, cols, rows, r, c, cell)
					}
					if cell.Style.Fg != ansi.IndexedColor(240) {
						t.Fatalf("method %v: col %d foreground %v, want index 240", m, c, cell.Style.Fg)
					}
				}
				if cell := buf.CellAt(cols, 0); cell == nil || cell.Content != "|" {
					t.Fatalf("method %v, %dx%d row %d: the cell after the image is %+v", m, cols, rows, r, cell)
				}
			}
		}
	}
}

func TestColor(t *testing.T) {
	t.Parallel()
	if got := kittygfx.Color(241); got != "\x1b[38;5;241m" {
		t.Errorf("Color(241) = %q", got)
	}
}

// apcs splits a run of graphics commands into their keys and payloads.
func apcs(t *testing.T, s string) (keys, payloads []string) {
	t.Helper()
	for s != "" {
		if !strings.HasPrefix(s, "\x1b_G") {
			t.Fatalf("not a graphics command: %q", s[:min(len(s), 20)])
		}
		end := strings.Index(s, "\x1b\\")
		if end < 0 {
			t.Fatalf("unterminated command")
		}
		k, p, _ := strings.Cut(s[3:end], ";")
		keys, payloads = append(keys, k), append(payloads, p)
		s = s[end+2:]
	}
	return keys, payloads
}

func TestTransmitChunks(t *testing.T) {
	t.Parallel()
	png := []byte(strings.Repeat("0123456789abcdef", 480)) // 7680 bytes, 10240 in base64
	keys, payloads := apcs(t, kittygfx.Encoder{}.Transmit(241, png, 30, 7))
	want := []string{
		"a=T,U=1,C=1,f=100,q=2,i=241,c=30,r=7,m=1",
		"m=1,q=2",
		"m=0,q=2",
	}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Errorf("keys %q, want %q", keys, want)
	}
	for i, p := range payloads[:len(payloads)-1] {
		if len(p) != 4096 {
			t.Errorf("chunk %d is %d bytes, want 4096", i, len(p))
		}
	}
	data, err := base64.StdEncoding.DecodeString(strings.Join(payloads, ""))
	if err != nil || string(data) != string(png) {
		t.Errorf("payload does not round-trip: %v", err)
	}

	keys, _ = apcs(t, kittygfx.Encoder{}.Transmit(16, []byte("x"), 1, 1))
	if len(keys) != 1 || keys[0] != "a=T,U=1,C=1,f=100,q=2,i=16,c=1,r=1,m=0" {
		t.Errorf("a small image is one command: %q", keys)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	if got := (kittygfx.Encoder{}).Delete(242); got != "\x1b_Ga=d,d=I,q=2,i=242\x1b\\" {
		t.Errorf("Delete = %q", got)
	}
}

func TestTmuxWrapsEachCommand(t *testing.T) {
	t.Parallel()
	enc := kittygfx.Encoder{Tmux: true}
	got := enc.Delete(240)
	if got != "\x1bPtmux;\x1b\x1b_Ga=d,d=I,q=2,i=240\x1b\x1b\\\x1b\\" {
		t.Errorf("Delete under tmux = %q", got)
	}
	png := make([]byte, 4000) // two chunks
	seq := enc.Transmit(240, png, 2, 2)
	if n := strings.Count(seq, "\x1bPtmux;"); n != 2 {
		t.Errorf("%d passthrough wraps, want one per chunk (2)", n)
	}
	unwrapped := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(seq, "\x1bPtmux;", ""), "\x1b\x1b", "\x01"), "\x1b\\", "")
	unwrapped = strings.ReplaceAll(unwrapped, "\x01", "\x1b")
	if want := (kittygfx.Encoder{}).Transmit(240, png, 2, 2); unwrapped != want {
		t.Errorf("the wrapped commands differ from the plain ones")
	}
}
