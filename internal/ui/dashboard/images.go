package dashboard

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/chart"
	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// Image keys of the charts in kittygfx.Images.
const (
	imageCPU = iota
	imageMemory
	// ImageKeys is how many images a dashboard shows at most.
	ImageKeys
)

func imageKey(wd widget) (int, bool) {
	switch wd {
	case widgetCPU:
		return imageCPU, true
	case widgetMemory:
		return imageMemory, true
	}
	return 0, false
}

// images reports whether the charts draw images now.
func (m *Model) images() bool { return m.src.Images != nil && m.pixels }

// nextDraw is the draw mode b and F6 switch to: images, when the
// terminal shows them, then braille, then blocks, then images again.
func (m *Model) nextDraw() (chart.Mode, bool) {
	switch {
	case m.images():
		return chart.Braille, false
	case m.mode == chart.Braille:
		return chart.Blocks, false
	case m.src.Images != nil:
		return chart.Braille, true
	}
	return chart.Braille, false
}

// nextDrawName names nextDraw for the key line.
func (m *Model) nextDrawName() string {
	switch mode, pixels := m.nextDraw(); {
	case pixels:
		return drawImages
	case mode == chart.Blocks:
		return drawBlocks
	}
	return drawBraille
}

// drawKey is the prefix of the keys the View menu runs: "draw:blocks".
const drawKey = "draw:"

// Names of the draw modes, as nextDrawName and the View menu say them.
const (
	drawImages  = "images"
	drawBraille = "braille"
	drawBlocks  = "blocks"
)

// drawName names the draw mode the charts use now.
func (m *Model) drawName() string {
	switch {
	case m.images():
		return drawImages
	case m.mode == chart.Blocks:
		return drawBlocks
	}
	return drawBraille
}

// drawNamed is the draw mode of a name; images only where the terminal
// shows them.
func (m *Model) drawNamed(name string) (mode chart.Mode, pixels, ok bool) {
	switch name {
	case drawImages:
		return chart.Braille, true, m.src.Images != nil
	case drawBraille:
		return chart.Braille, false, true
	case drawBlocks:
		return chart.Blocks, false, true
	}
	return chart.Braille, false, false
}

// drawAs switches the charts to mode, or to images. Leaving images
// deletes them from the terminal.
func (m *Model) drawAs(mode chart.Mode, pixels bool) tea.Cmd {
	was := m.images()
	m.mode, m.pixels = mode, pixels
	if was && !m.images() {
		return raw(m.src.Images.Release())
	}
	if !was && m.images() {
		return kittygfx.QueryCellSize()
	}
	return nil
}

// resizeSettle is how long the size must stay put before the charts are
// drawn as images again. A drag sends a resize per step; the text charts
// show until it stops.
const resizeSettle = 300 * time.Millisecond

// settledMsg ends the settling of resize number seq of dashboard id.
type settledMsg struct{ id, seq int }

// resized handles a change of the screen size. With images on, it deletes
// them, so no placement of the old size stays, clears the screen, and
// holds new images back until the size settles; meanwhile the plots are
// text.
func (m *Model) resized() tea.Cmd {
	if !m.images() {
		return nil
	}
	m.resizes++
	m.settling = true
	id, seq := m.id, m.resizes
	tick := m.src.Tick
	if tick == nil {
		tick = tea.Tick
	}
	return tea.Batch(
		raw(m.src.Images.Release()),
		tea.ClearScreen,
		tick(resizeSettle, func(time.Time) tea.Msg { return settledMsg{id: id, seq: seq} }),
	)
}

func raw(seq string) tea.Cmd {
	if seq == "" {
		return nil
	}
	return tea.Raw(seq)
}

// syncImages rasterizes the charts of the frame the screen will show and
// returns the transmits of the pictures the terminal does not have.
func (m *Model) syncImages() tea.Cmd {
	if !m.images() || m.settling || m.w < 20 || m.h < 6 {
		return nil
	}
	v := m.top()
	charts := m.placeCharts(v, layout(v, m.w, m.h))
	var cmds []tea.Cmd
	for wd, c := range charts {
		key, ok := imageKey(wd)
		if !ok {
			continue
		}
		cols, rows := c.PlotSize()
		if cols == 0 || cols > kittygfx.MaxCells || rows > kittygfx.MaxCells {
			continue
		}
		cw, ch := m.src.Images.CellSize(cols, rows)
		cmds = append(cmds, m.src.Images.Show(key, c.Raster(cw, ch), cols, rows))
	}
	return tea.Batch(cmds...)
}

// placeImage replaces the plot cells of a rendered chart (lines[1:],
// after the legend) with the placeholders of its image, when the terminal
// has a picture of the plot's size; else the text plot stays. The cells
// the image leaves to text (chart.TextCells: the "no data" band and the
// above-the-scale arrows) keep their text.
func (m *Model) placeImage(wd widget, c chart.Chart, lines []string) {
	key, ok := imageKey(wd)
	if !ok || !m.images() {
		return
	}
	cols, rows := c.PlotSize()
	id, ok := m.src.Images.Shown(key, cols, rows)
	if !ok || len(lines) < rows+1 {
		return
	}
	text := c.TextCells()
	off := c.Width - cols
	color := kittygfx.Color(id)
	for row := range rows {
		line := lines[1+row]
		var b strings.Builder
		b.WriteString(ansi.Truncate(line, off, ""))
		for x := 0; x < cols; {
			end := x + 1
			for end < cols && text[row][end] == text[row][x] {
				end++
			}
			if text[row][x] {
				b.WriteString(ansi.Cut(line, off+x, off+end))
			} else {
				b.WriteString(color + kittygfx.Cells(row, x, end) + "\x1b[39m")
			}
			x = end
		}
		b.WriteString(ansi.TruncateLeft(line, c.Width, ""))
		lines[1+row] = b.String()
	}
}
