package chart_test

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/chart"
)

var t0 = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

// every returns a point each step from t0 for n steps, valued by v.
func every(step time.Duration, n int, v func(i int) float64) []chart.Point {
	out := make([]chart.Point, 0, n)
	for i := range n {
		out = append(out, chart.Point{At: t0.Add(time.Duration(i) * step), V: v(i)})
	}
	return out
}

func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripANSI(l)
	}
	return out
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && (r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'):
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// plotColumn returns the plot cells of column x, top to bottom, and the
// axis cell below them.
func plotColumn(lines []string, label, x int) (string, rune) {
	var col []rune
	for _, l := range lines[:len(lines)-1] {
		col = append(col, []rune(l)[label+1+x])
	}
	return string(col), []rune(lines[len(lines)-1])[label+1+x]
}

func TestRenderSizeAndAxes(t *testing.T) {
	t.Parallel()
	for _, mode := range []chart.Mode{chart.Braille, chart.Blocks} {
		for _, size := range [][2]int{{80, 8}, {30, 4}, {200, 20}, {12, 3}} {
			c := chart.Chart{
				Start: t0, End: t0.Add(time.Hour), Mode: mode, Cursor: 3,
				Points: every(time.Minute, 61, func(i int) float64 { return float64(i % 7) }),
				Refs:   []chart.Ref{{V: 8, Style: lipgloss.NewStyle().Foreground(lipgloss.Red)}},
				Width:  size[0], Height: size[1],
			}
			lines := c.Render()
			if len(lines) != size[1] {
				t.Fatalf("mode %d %v: %d lines", mode, size, len(lines))
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w != size[0] {
					t.Errorf("mode %d %v: line %d is %d cells: %q", mode, size, i, w, stripANSI(l))
				}
			}
			p := plain(lines)
			if size[0] >= 30 && (!strings.Contains(p[len(p)-1], "10:00") || !strings.Contains(p[len(p)-1], "11:00")) {
				t.Errorf("mode %d %v: time axis %q lacks the span", mode, size, p[len(p)-1])
			}
			// The limit of 8 sets the scale: the top label is 10.
			if !strings.HasPrefix(strings.TrimSpace(p[0]), "10│") {
				t.Errorf("mode %d %v: top row %q, want the scale 10", mode, size, p[0])
			}
		}
	}
}

// TestGapIsNotZero: a zero draws the lowest unit, a gap draws nothing and
// marks the axis.
func TestGapIsNotZero(t *testing.T) {
	t.Parallel()
	// One sample per minute for 10 minutes, the 5th missing; 10 cells of
	// blocks, so one cell per minute.
	var pts []chart.Point
	for i := range 10 {
		if i != 4 {
			pts = append(pts, chart.Point{At: t0.Add(time.Duration(i)*time.Minute + time.Second), V: 0})
		}
	}
	pts[0].V = 4
	c := chart.Chart{Start: t0, End: t0.Add(10 * time.Minute), Points: pts, Mode: chart.Blocks, Width: 12, Height: 3, Cursor: -1, Format: func(v float64) string { return "" }}
	lines := plain(c.Render())
	zero, _ := plotColumn(lines, 0, 2)
	gap, _ := plotColumn(lines, 0, 4)
	if zero != " ▁" {
		t.Errorf("zero column %q, want the lowest eighth", zero)
	}
	if gap != "  " {
		t.Errorf("gap column %q, want blank", gap)
	}
	if c.Cell(4).OK {
		t.Error("Cell reports a value for the gap")
	}
	if cv := c.Cell(2); !cv.OK || cv.V != 0 {
		t.Errorf("Cell(2) = %+v, want a zero", cv)
	}
}

// TestPeakSurvivesPooling: many samples in one cell keep their largest.
func TestPeakSurvivesPooling(t *testing.T) {
	t.Parallel()
	pts := every(time.Second, 600, func(i int) float64 {
		if i == 301 {
			return 10
		}
		return 1
	})
	c := chart.Chart{Start: t0, End: t0.Add(10 * time.Minute), Points: pts, Mode: chart.Braille, Width: 11, Height: 5, Cursor: -1, Format: func(float64) string { return "" }}
	lines := plain(c.Render())
	if cv := c.Cell(5); !cv.OK || cv.V != 10 {
		t.Errorf("cell of the spike = %+v, want 10", cv)
	}
	// The spike's column is the tallest: its first drawn row is above
	// that of every other column.
	first := func(x int) int {
		for row, l := range lines[:len(lines)-1] {
			if r := []rune(l)[1+x]; r != ' ' && r != '⠀' {
				return row
			}
		}
		return len(lines)
	}
	for x := range 10 {
		if x != 5 && first(x) <= first(5) {
			t.Errorf("column %d reaches row %d, the spike row %d:\n%s", x, first(x), first(5), strings.Join(lines, "\n"))
		}
	}
}

func TestHoldBridgesSlowSamples(t *testing.T) {
	t.Parallel()
	// A sample every 15s over 5 minutes on 60 block cells (5s each).
	pts := every(15*time.Second, 21, func(int) float64 { return 1 })
	pts = append(pts[:8], pts[12:]...) // 60s without samples
	c := chart.Chart{Start: t0, End: t0.Add(5 * time.Minute), Points: pts, Hold: 37500 * time.Millisecond, Mode: chart.Blocks, Width: 61, Height: 3, Cursor: -1, Format: func(float64) string { return "" }}
	gapsIn := func(c chart.Chart, from, to int) int {
		n := 0
		for x := from; x < to; x++ {
			if !c.Cell(x).OK {
				n++
			}
		}
		return n
	}
	// Samples at 105s and 180s: cells from 142.5s on are gaps until 180s.
	if gaps := gapsIn(c, 0, 60); gaps == 0 || gaps > 8 {
		t.Errorf("%d gap cells, want the 60s hole only", gaps)
	}
	if gapsIn(c, 0, 20) > 0 || gapsIn(c, 40, 60) > 0 {
		t.Error("regular 15s samples show gaps")
	}
	c.Hold = 0
	if gaps := gapsIn(c, 0, 60); gaps < 30 {
		t.Errorf("without Hold the cells between samples must be gaps: %d", gaps)
	}
}

func TestReferenceLine(t *testing.T) {
	t.Parallel()
	c := chart.Chart{
		Start: t0, End: t0.Add(10 * time.Minute), Mode: chart.Blocks, Width: 11, Height: 5, Cursor: -1,
		Points: every(time.Minute, 10, func(int) float64 { return 2 }),
		Refs:   []chart.Ref{{V: 4}}, Format: func(float64) string { return "" },
	}
	lines := plain(c.Render())
	// Scale 5 over 4 rows: 4 is in the top row.
	if got := strings.Count(lines[0], "┄"); got != 10 {
		t.Errorf("row 0 %q has %d dashes, want the limit across the plot", lines[0], got)
	}
	for _, l := range lines[1:4] {
		if strings.Contains(l, "┄") {
			t.Errorf("row %q carries the limit too", l)
		}
	}
}

func TestBrailleTwoSamplesPerCell(t *testing.T) {
	t.Parallel()
	// Left sample at half the scale (usage keeps headroom), right sample
	// empty: the cell has only the left dot column.
	c := chart.Chart{
		Start: t0, End: t0.Add(4 * time.Minute), Mode: chart.Braille, Width: 3, Height: 2, Cursor: -1,
		Points: []chart.Point{{At: t0, V: 1}, {At: t0.Add(2 * time.Minute), V: 1}, {At: t0.Add(3 * time.Minute), V: 0}},
		Format: func(float64) string { return "" },
	}
	if got := chart.Samples(c.Cells(), c.Mode); got != 4 {
		t.Fatalf("samples = %d, want 4 for 2 cells", got)
	}
	row := []rune(plain(c.Render())[0])
	if row[1] != '⡄' || row[2] != '⣄' {
		t.Errorf("row %q, want ⡄ (left only, right a gap) and ⣄ (left half, right zero)", string(row))
	}
}

func TestCellSpan(t *testing.T) {
	t.Parallel()
	c := chart.Chart{Start: t0, End: t0.Add(10 * time.Minute), Mode: chart.Blocks, Width: 11, Height: 3, Format: func(float64) string { return "" },
		Points: every(time.Minute, 10, func(i int) float64 { return float64(i) })}
	cv := c.Cell(3)
	if !cv.From.Equal(t0.Add(3*time.Minute)) || !cv.To.Equal(t0.Add(4*time.Minute)) || cv.V != 3 || !cv.OK {
		t.Errorf("Cell(3) = %+v", cv)
	}
	if c.Cell(10).OK {
		t.Error("Cell past the plot reports a value")
	}
}

// TestPlotCellsWidensTheLabels: a fixed plot width that fits pads the
// labels; one that does not fit is ignored.
func TestPlotCellsWidensTheLabels(t *testing.T) {
	t.Parallel()
	c := chart.Chart{Start: t0, End: t0.Add(10 * time.Minute), Mode: chart.Blocks, Width: 20, Height: 4, Format: func(float64) string { return "1" },
		Points: every(time.Minute, 10, func(int) float64 { return 1 })}
	free := c.Cells()
	c.PlotCells = free - 3
	if got := c.Cells(); got != free-3 {
		t.Errorf("cells = %d, want %d", got, free-3)
	}
	for _, l := range plain(c.Render()) {
		if n := len([]rune(l)); n != 20 {
			t.Errorf("line %q is %d cells, want 20", l, n)
		}
	}
	c.PlotCells = free + 5
	if got := c.Cells(); got != free {
		t.Errorf("cells = %d, want the free width %d", got, free)
	}
}

// TestRefFollowsItsPoints: a reference line with points sits at the value
// of each time, and leaves a gap where its points stop for longer than
// Hold; RefAt reads it back per cell.
func TestRefFollowsItsPoints(t *testing.T) {
	t.Parallel()
	// 20 minutes in 20 cells of a 5-row plot with a top of 5: a request
	// of 2 (row 3) for ten minutes, then of 4 (row 1), with no point in
	// minutes 14 to 16.
	pts := every(time.Minute, 21, func(i int) float64 {
		if i < 10 {
			return 2
		}
		return 4
	})
	pts = append(pts[:14], pts[17:]...)
	c := chart.Chart{
		Start: t0, End: t0.Add(20 * time.Minute), Mode: chart.Blocks, Width: 22, Height: 6, Format: func(float64) string { return "5" },
		Refs: []chart.Ref{{Points: pts, Hold: 90 * time.Second}},
	}
	if got := c.Cells(); got != 20 {
		t.Fatalf("cells = %d, want 20", got)
	}
	lines := plain(c.Render())
	line := func(row int) string { return string([]rune(lines[row])[2:]) }
	// Minute 13's point holds cell 14; cells 15 and 16 are over 90s from it.
	if got, want := line(1), "          ┄┄┄┄┄  ┄┄┄"; got != want {
		t.Errorf("row 1 %q, want %q: the request of 4 after minute 10, a gap in cells 15 and 16", got, want)
	}
	if got, want := line(3), "┄┄┄┄┄┄┄┄┄┄          "; got != want {
		t.Errorf("row 3 %q, want %q: the request of 2 in the first ten minutes only", got, want)
	}
	for _, tc := range []struct {
		cell int
		v    float64
		ok   bool
	}{{0, 2, true}, {9, 2, true}, {10, 4, true}, {15, 0, false}, {17, 4, true}, {19, 4, true}} {
		v, ok := c.RefAt(0, tc.cell)
		if v != tc.v || ok != tc.ok {
			t.Errorf("RefAt(0, %d) = %v, %v; want %v, %v", tc.cell, v, ok, tc.v, tc.ok)
		}
	}
	if _, ok := c.RefAt(1, 0); ok {
		t.Error("RefAt of a missing line reports a value")
	}
}

// TestScaleFitsUsage: a request far above the usage does not flatten it;
// it shows as arrows on the top row and OffScale says so.
func TestScaleFitsUsage(t *testing.T) {
	t.Parallel()
	c := chart.Chart{
		Start: t0, End: t0.Add(10 * time.Minute), Mode: chart.Blocks, Width: 11, Height: 5, Cursor: -1,
		Points: every(time.Minute, 10, func(int) float64 { return 0.002 }),
		Refs:   []chart.Ref{{V: 0.04}, {V: 0.005}},
	}
	lines := plain(c.Render())
	if !strings.HasPrefix(lines[0], "0.005") {
		t.Errorf("top label of %q, want the scale to fit the usage and the near line", lines[0])
	}
	if !strings.Contains(lines[0], "↑") {
		t.Errorf("row 0 %q has no arrow for the line above the scale", lines[0])
	}
	if !c.OffScale(0) || c.OffScale(1) {
		t.Errorf("OffScale = %v, %v; want the far line only", c.OffScale(0), c.OffScale(1))
	}
	c.Points = nil
	if lines := plain(c.Render()); !strings.HasPrefix(lines[0], "0.05") {
		t.Errorf("without usage the lines set the scale: %q", lines[0])
	}
}

// TestGapRunSaysNoData: a wide run of cells without samples is labeled.
func TestGapRunSaysNoData(t *testing.T) {
	t.Parallel()
	c := chart.Chart{
		Start: t0, End: t0.Add(30 * time.Minute), Mode: chart.Blocks, Width: 31, Height: 5, Cursor: -1,
		Points: every(time.Minute, 5, func(int) float64 { return 1 }),
		Format: func(float64) string { return "" },
	}
	lines := plain(c.Render())
	if !strings.Contains(lines[2], "no data") {
		t.Errorf("middle row %q does not say no data", lines[2])
	}
	if strings.Contains(lines[2][:8], "no data") {
		t.Errorf("the label sits on samples: %q", lines[2])
	}
}

// TestBrailleAreaIsSolid: under the edge, the area is full blocks, not
// full braille cells, which most fonts draw as a grid of dots.
func TestBrailleAreaIsSolid(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	var pts []chart.Point
	for i := range 21 {
		pts = append(pts, chart.Point{At: t0.Add(time.Duration(i) * 30 * time.Second), V: 7})
	}
	pts = append(pts, chart.Point{At: t0.Add(10 * time.Minute), V: 10})
	c := chart.Chart{Start: t0, End: t0.Add(10 * time.Minute), Points: pts, Mode: chart.Braille, Width: 11, Height: 5, Cursor: -1, Format: func(float64) string { return "" }}
	out := strings.Join(c.Render(), "\n")
	if strings.ContainsRune(out, '⣿') {
		t.Errorf("full braille cells in the area:\n%s", out)
	}
	if !strings.ContainsRune(out, '█') {
		t.Errorf("no solid area:\n%s", out)
	}
}
