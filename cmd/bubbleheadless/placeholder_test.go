package main

import (
	"image/color"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// cell is the content of a placeholder cell with the diacritics for the
// numbers given.
func cell(t *testing.T, marks ...int) string {
	t.Helper()
	s := string(kittygfx.Placeholder)
	for _, n := range marks {
		r, ok := kittygfx.Diacritic(n)
		if !ok {
			t.Fatalf("no diacritic for %d", n)
		}
		s += string(r)
	}
	return s
}

func TestReadPlaceholder(t *testing.T) {
	t.Parallel()
	last := kittygfx.MaxCells - 1
	id240 := ansi.IndexedColor(240)
	tests := []struct {
		name    string
		content string
		fg      color.Color
		left    *placeholder
		want    placeholder
		notOne  bool
	}{
		{name: "row and column", content: cell(t, 3, 5), fg: id240, want: placeholder{id: 240, row: 3, col: 5}},
		{name: "the last diacritic", content: cell(t, last, last), fg: id240, want: placeholder{id: 240, row: last, col: last}},
		{name: "third diacritic is the high byte", content: cell(t, 1, 2, 7), fg: id240, want: placeholder{id: 7<<24 | 240, row: 1, col: 2}},
		{name: "16-color ID", content: cell(t, 0, 0), fg: ansi.BasicColor(5), want: placeholder{id: 5}},
		{name: "true color ID", content: cell(t, 0, 1), fg: color.RGBA{R: 1, G: 2, B: 3, A: 0xff}, want: placeholder{id: 0x010203, col: 1}},
		{name: "default color is no image", content: cell(t, 2, 2), want: placeholder{row: 2, col: 2}},
		{name: "a rune that is no diacritic ends them", content: cell(t, 4) + "x", fg: id240, want: placeholder{id: 240, row: 4}},
		{name: "not a placeholder", content: "x", fg: id240, notOne: true},
		{name: "empty cell", content: "", notOne: true},

		{name: "none: next column of left", content: cell(t), fg: id240, left: &placeholder{id: 240, row: 2, col: 4}, want: placeholder{id: 240, row: 2, col: 5}},
		{name: "none: high byte of left", content: cell(t), fg: id240, left: &placeholder{id: 9<<24 | 240, row: 2, col: 4}, want: placeholder{id: 9<<24 | 240, row: 2, col: 5}},
		{name: "none: left of another image", content: cell(t), fg: id240, left: &placeholder{id: 241, row: 2, col: 4}, want: placeholder{id: 240}},
		{name: "none: no left", content: cell(t), fg: id240, want: placeholder{id: 240}},
		{name: "row only: same row as left", content: cell(t, 2), fg: id240, left: &placeholder{id: 240, row: 2, col: 4}, want: placeholder{id: 240, row: 2, col: 5}},
		{name: "row only: another row", content: cell(t, 3), fg: id240, left: &placeholder{id: 240, row: 2, col: 4}, want: placeholder{id: 240, row: 3}},
		{name: "row and column after left: its high byte", content: cell(t, 2, 5), fg: id240, left: &placeholder{id: 9<<24 | 240, row: 2, col: 4}, want: placeholder{id: 9<<24 | 240, row: 2, col: 5}},
		{name: "row and column not after left", content: cell(t, 2, 7), fg: id240, left: &placeholder{id: 9<<24 | 240, row: 2, col: 4}, want: placeholder{id: 240, row: 2, col: 7}},
		{name: "all three: left is ignored", content: cell(t, 2, 5, 1), fg: id240, left: &placeholder{id: 9<<24 | 240, row: 2, col: 4}, want: placeholder{id: 1<<24 | 240, row: 2, col: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := readPlaceholder(tt.content, tt.fg, tt.left)
			if ok == tt.notOne {
				t.Fatalf("ok = %v, want %v", ok, !tt.notOne)
			}
			if ok && got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Every cell kc's encoder writes reads back as its row and column.
func TestReadPlaceholderReadsKittygfxRows(t *testing.T) {
	t.Parallel()
	const cols, rows = 40, 12
	for r, line := range kittygfx.Rows(cols, rows) {
		c := 0
		for _, content := range splitPlaceholders(line) {
			got, ok := readPlaceholder(content, ansi.IndexedColor(243), nil)
			if !ok || got != (placeholder{id: 243, row: r, col: c}) {
				t.Fatalf("cell (%d,%d) reads %+v, %v", r, c, got, ok)
			}
			c++
		}
		if c != cols {
			t.Fatalf("row %d has %d cells, want %d", r, c, cols)
		}
	}
}

// splitPlaceholders cuts a row of placeholder cells before each U+10EEEE.
func splitPlaceholders(line string) []string {
	var cells []string
	start := -1
	for i, r := range line {
		if r == kittygfx.Placeholder {
			if start >= 0 {
				cells = append(cells, line[start:i])
			}
			start = i
		}
	}
	if start >= 0 {
		cells = append(cells, line[start:])
	}
	return cells
}
