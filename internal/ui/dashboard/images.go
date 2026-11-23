package dashboard

import (
	tea "charm.land/bubbletea/v2"

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

// nextDraw is the draw mode b switches to: braille, blocks, then images
// when the terminal shows them, then braille again.
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
		return "images"
	case mode == chart.Blocks:
		return "blocks"
	}
	return "braille"
}

// switchDraw is b. Leaving images deletes them from the terminal.
func (m *Model) switchDraw() tea.Cmd {
	was := m.images()
	m.mode, m.pixels = m.nextDraw()
	if was && !m.images() {
		return raw(m.src.Images.Release())
	}
	if !was && m.images() {
		return kittygfx.QueryCellSize()
	}
	return nil
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
	if !m.images() || m.w < 20 || m.h < 6 {
		return nil
	}
	_, charts := m.frame(m.top())
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

// placeImage replaces the plot rows of a rendered chart (lines[1:], after
// the title) with the placeholders of its image, when the terminal has a
// picture of the plot's size; else the text plot stays.
func (m *Model) placeImage(wd widget, c chart.Chart, lines []string) {
	key, ok := imageKey(wd)
	if !ok || !m.images() {
		return
	}
	cols, rows := c.PlotSize()
	ph, ok := m.src.Images.Placeholders(key, cols, rows)
	if !ok || len(lines) < rows+1 {
		return
	}
	for i, p := range ph {
		lines[1+i] = place(lines[1+i], p, c.Width-cols, cols, c.Width)
	}
}
