package dashboard

import (
	"slices"
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
// terminal shows them, then blocks, then braille, then images again.
// Text starts in blocks: a solid area with an edge in eighths reads the
// same in every font; braille draws two samples per cell at the edge.
func (m *Model) nextDraw() (chart.Mode, bool) {
	switch {
	case m.images():
		return chart.Blocks, false
	case m.mode == chart.Blocks:
		return chart.Braille, false
	case m.src.Images != nil:
		return chart.Blocks, true
	}
	return chart.Blocks, false
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
		return chart.Blocks, true, m.src.Images != nil
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

// encodedMsg is a chart picture rastered and compressed off the event
// loop, for dashboard id.
type encodedMsg struct {
	id  int
	pic kittygfx.Picture
}

// syncImages starts, for each chart of the frame the screen will show, a
// command that rasters and compresses its picture off the event loop.
// A chart whose last picture is still being made gets none; that one's
// return starts the next (see showEncoded).
func (m *Model) syncImages() tea.Cmd { return m.syncKey(-1) }

// syncKey is syncImages for the chart of image key only, or for all with
// -1. A returning picture restarts its own chart only: restarting all
// would mark the other chart's picture in flight as asked for again, and
// the two returns would restart each other without end.
func (m *Model) syncKey(only int) tea.Cmd {
	if !m.images() || m.settling || m.w < 20 || m.h < 6 {
		return nil
	}
	v := m.top()
	charts := m.placeCharts(v, layout(v, m.w, m.h))
	var cmds []tea.Cmd
	for wd, c := range charts {
		key, ok := imageKey(wd)
		if !ok || (only >= 0 && key != only) {
			continue
		}
		cols, rows := c.PlotSize()
		if cols == 0 {
			continue
		}
		cw, ch := m.src.Images.CellSize(cols, rows)
		// The cursor is text over the image: when only it moved, the
		// terminal has the picture already and nothing is drawn.
		rk := c.RasterKey(cw, ch)
		if _, shown := m.src.Images.Shown(key, cols, rows); shown && m.rasterKeys[key] == rk {
			continue
		}
		job, ok := m.src.Images.Begin(key, cols, rows)
		if !ok {
			continue
		}
		m.rasterKeys[key] = rk
		c, id := detached(c), m.id
		cmds = append(cmds, func() tea.Msg {
			return encodedMsg{id: id, pic: job.Encode(c.Raster(job.CellSize()))}
		})
	}
	return tea.Batch(cmds...)
}

// showEncoded takes a picture back on the event loop. One made for
// another dashboard, before a resize, or for a plot of another size than
// the chart's now is dropped, so it never paints an old size. A chart
// asked for while its picture was made starts the next one.
func (m *Model) showEncoded(msg encodedMsg) tea.Cmd {
	im := m.src.Images
	if im == nil {
		return nil
	}
	job := msg.pic.Job()
	cols, rows, ok := m.plotSize(job.Key())
	if msg.id != m.id || !m.images() || m.settling || !ok || cols != job.Cols() || rows != job.Rows() {
		// The dropped picture never reached the terminal: its raster key
		// must not stop the next one.
		m.forgetRaster(job.Key())
		if im.Drop(msg.pic) {
			return m.syncKey(job.Key())
		}
		return nil
	}
	cmd, again := im.Finish(msg.pic)
	if again {
		m.forgetRaster(job.Key())
		return tea.Batch(cmd, m.syncKey(job.Key()))
	}
	return cmd
}

// forgetRaster clears the raster key of image key, so the next sync
// rasters that chart even when its samples did not change.
func (m *Model) forgetRaster(key int) {
	if key >= 0 && key < len(m.rasterKeys) {
		m.rasterKeys[key] = 0
	}
}

// plotSize is the plot size of the chart of image key in the frame the
// screen shows now.
func (m *Model) plotSize(key int) (cols, rows int, ok bool) {
	if m.w < 20 || m.h < 6 {
		return 0, 0, false
	}
	v := m.top()
	for wd, c := range m.placeCharts(v, layout(v, m.w, m.h)) {
		if k, isImage := imageKey(wd); isImage && k == key {
			cols, rows = c.PlotSize()
			return cols, rows, cols > 0
		}
	}
	return 0, 0, false
}

// detached copies the samples of c, so a command can raster it while the
// session history goes on changing.
func detached(c chart.Chart) chart.Chart {
	c.Points = slices.Clone(c.Points)
	c.Refs = slices.Clone(c.Refs)
	for i := range c.Refs {
		c.Refs[i].Points = slices.Clone(c.Refs[i].Points)
	}
	return c
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
