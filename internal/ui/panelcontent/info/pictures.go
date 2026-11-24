package info

import (
	"image"
	"image/color"
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/chart"
	"github.com/stokaro/kc/internal/ui/kittygfx"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// With images on (Options.Images), Info draws its bars as Grafana bar
// gauges and its usage history as Grafana stat panel sparklines; every
// other cell stays text. The pictures go through the Kitty Unicode
// placeholders of kittygfx, like the dashboard's charts (ADR 0320).

// Image keys: one gauge per bar, one sparkline per usage history.
const (
	keyGaugeCPU = iota
	keyGaugeMemory
	keyGaugePods
	keyGaugeDisk
	keySparkCPU
	keySparkMemory
	// ImageKeys is how many images one Info panel shows at most.
	ImageKeys
)

// sparkRows is the height of a sparkline drawn as an image: two rows
// show its shape, where the text sparkline has eight steps in one.
const sparkRows = 2

// resizeSettle is how long the panel width must stay put before the
// pictures of the new width are made; meanwhile the bars are text.
const resizeSettle = 300 * time.Millisecond

func gaugeKey(label string) (int, bool) {
	switch label {
	case "CPU":
		return keyGaugeCPU, true
	case "Memory":
		return keyGaugeMemory, true
	case "Pods":
		return keyGaugePods, true
	case "Disk":
		return keyGaugeDisk, true
	}
	return 0, false
}

func sparkKey(label string) (int, bool) {
	switch label {
	case "CPU":
		return keySparkCPU, true
	case "Memory":
		return keySparkMemory, true
	}
	return 0, false
}

// picSlot places row of a picture of cols by rows cells at cell x of a
// line. The line's text has the text version in those cells.
type picSlot struct {
	key, x, cols, row, rows int
}

// picture is what draws one image: a chart.Gauge or a chart.Spark.
type picture interface {
	Raster(cols, rows, cellW, cellH int) *image.RGBA
	RasterKey(cols, rows, cellW, cellH int) uint64
}

// wanted is a picture the content shows, at its size in cells.
type wanted struct {
	key, cols, rows int
	draw            picture
}

// pictureState follows the pictures of one widget.
type pictureState struct {
	// asked is the raster key of the picture last begun per image key;
	// zero after a dropped picture, so the next sync draws it again.
	asked [ImageKeys]uint64
	// width is the panel width the pictures were made for; settling is
	// set from a width change until it has stayed put for resizeSettle.
	width    int
	settling bool
	resizes  int
}

// pictureMsg is a picture rastered and compressed off the event loop.
type pictureMsg struct {
	w   *Widget
	pic kittygfx.Picture
}

func (pictureMsg) WidgetBackground() {}

// settledMsg ends the settling of resize number seq.
type settledMsg struct {
	w   *Widget
	seq int
}

func (settledMsg) WidgetBackground() {}

// levelColor is the fill of a gauge in the state of what its bar shows,
// as Grafana's classic thresholds color it.
func levelColor(h models.RowHealth) color.RGBA {
	switch h {
	case models.RowHealthNotReady:
		return chart.Orange
	case models.RowHealthFailed:
		return chart.Red
	}
	return chart.Green
}

// Mark colors: the dashboard's request and limit lines.
var (
	requestColor = chart.Yellow
	limitColor   = chart.Red
)

// gaugeOf is the picture of bar b.
func gaugeOf(b models.InfoBar) chart.Gauge {
	g := chart.Gauge{Percent: b.Percent, Color: levelColor(b.Health)}
	for _, m := range b.Marks {
		col := requestColor
		if m.Kind == models.InfoMarkLimit {
			col = limitColor
		}
		g.Marks = append(g.Marks, chart.Mark{Percent: m.Percent, Color: col})
	}
	return g
}

// sparkOf is the picture of series s as view shows it, in the color of
// the bar above it.
func sparkOf(s series, view sparkView, col color.RGBA) chart.Spark {
	pts := make([]chart.Point, 0, len(s.samples))
	for _, sm := range s.samples {
		pts = append(pts, chart.Point{At: sm.at, V: sm.v})
	}
	return chart.Spark{Start: view.start, End: view.end, Points: pts, Top: view.top, Gap: gapAfter, Color: col}
}

// splitSparkMark is sparkMark for a sparkline of two rows: the scale on
// the top row, next to the top of the picture, and the span on the
// bottom row.
func splitSparkMark(span time.Duration, top float64) (scale, spanText string) {
	spanText = " " + shortAge(historyWindow)
	if span < historyWindow {
		spanText = " " + shortAge(span) + " of " + shortAge(historyWindow)
	}
	return " top " + strconv.FormatFloat(top, 'g', -1, 64) + "%", spanText
}

// images is the image set of the widget, nil when it draws text.
func (w *Widget) images() *kittygfx.Images { return w.opts.Images }

// syncPictures starts, for every picture the content shows that the
// terminal does not have, a command that rasters and compresses it off
// the event loop. A picture still being made gets none: its return
// starts the next (takePicture). Pictures the content no longer shows are
// deleted. It never runs when a transmit comes back: a sparkline moves
// with the clock, and a new raster at each return would go out without
// end (ADR 0320).
func (w *Widget) syncPictures() tea.Cmd { return w.syncKey(-1) }

// syncKey is syncPictures for image key only, or for all with -1.
func (w *Widget) syncKey(only int) tea.Cmd {
	im := w.images()
	if im == nil || !w.active {
		return nil
	}
	switch {
	case w.pics.width == 0:
		// The first size of a start is no resize.
		w.pics.width = w.width
	case w.width != w.pics.width:
		return w.resized()
	}
	if w.pics.settling || w.width <= 0 {
		return nil
	}
	_, pics := w.layout(w.width)
	var cmds []tea.Cmd
	var shown [ImageKeys]bool
	for _, p := range pics {
		shown[p.key] = true
		if only >= 0 && p.key != only {
			continue
		}
		cw, ch := im.CellSize(p.cols, p.rows)
		rk := p.draw.RasterKey(p.cols, p.rows, cw, ch)
		if _, ok := im.Shown(p.key, p.cols, p.rows); ok && w.pics.asked[p.key] == rk {
			continue
		}
		job, ok := im.Begin(p.key, p.cols, p.rows)
		if !ok {
			continue
		}
		w.pics.asked[p.key] = rk
		draw, widget := detach(p.draw), w
		cmds = append(cmds, func() tea.Msg {
			cw, ch := job.CellSize()
			return pictureMsg{w: widget, pic: job.Encode(draw.Raster(job.Cols(), job.Rows(), cw, ch))}
		})
	}
	if only < 0 {
		for key, on := range shown {
			if !on && im.Holds(key) {
				w.pics.asked[key] = 0
				cmds = append(cmds, raw(im.Forget(key)))
			}
		}
	}
	return tea.Batch(cmds...)
}

// detach copies the samples of a sparkline, so a command can raster it
// while the history goes on.
func detach(p picture) picture {
	if s, ok := p.(chart.Spark); ok {
		s.Points = slices.Clone(s.Points)
		return s
	}
	return p
}

// takePicture takes a picture back on the event loop. One for a size the
// content no longer has, or made before a resize or a release, is
// dropped, so it never shows an old size. A picture asked for while this
// one was made starts the next one.
func (w *Widget) takePicture(m pictureMsg) tea.Cmd {
	im := w.images()
	if im == nil {
		return nil
	}
	job := m.pic.Job()
	key := job.Key()
	cols, rows, ok := w.pictureSize(key)
	if !w.active || w.pics.settling || !ok || cols != job.Cols() || rows != job.Rows() {
		w.forget(key)
		if im.Drop(m.pic) {
			return w.syncKey(key)
		}
		return nil
	}
	cmd, again := im.Finish(m.pic)
	if again {
		w.forget(key)
		return tea.Batch(cmd, w.syncKey(key))
	}
	return cmd
}

// forget clears the raster key of image key.
func (w *Widget) forget(key int) {
	if key >= 0 && key < ImageKeys {
		w.pics.asked[key] = 0
	}
}

// pictureSize is the size the content shows the picture of key at now.
func (w *Widget) pictureSize(key int) (cols, rows int, ok bool) {
	if w.width <= 0 {
		return 0, 0, false
	}
	_, pics := w.layout(w.width)
	for _, p := range pics {
		if p.key == key {
			return p.cols, p.rows, true
		}
	}
	return 0, 0, false
}

// resized deletes the pictures when the panel width changed and holds new
// ones back until the width stays put; the bars are text meanwhile. A
// drag of the terminal edge sends a size per step, and no picture of an
// old size may reach the screen.
func (w *Widget) resized() tea.Cmd {
	im := w.images()
	w.pics.width = w.width
	if im == nil {
		return nil
	}
	w.pics.resizes++
	w.pics.settling = true
	w.pics.asked = [ImageKeys]uint64{}
	seq, widget := w.pics.resizes, w
	tick := w.opts.Tick
	if tick == nil {
		tick = tea.Tick
	}
	return tea.Batch(
		raw(im.Release()),
		tick(resizeSettle, func(time.Time) tea.Msg { return settledMsg{w: widget, seq: seq} }),
	)
}

// settled ends the settling of a resize: the cell size is asked again (a
// new font size comes with a resize), and the pictures of the new width
// go out.
func (w *Widget) settled(m settledMsg) tea.Cmd {
	if m.seq != w.pics.resizes || !w.pics.settling {
		return nil
	}
	w.pics.settling = false
	if w.images() == nil || !w.active {
		return nil
	}
	return tea.Batch(kittygfx.QueryCellSize(), w.syncPictures())
}

// takeTerminal takes a cell size report or the return of a transmit, and
// reports whether it was for this widget: then the screen shows another
// picture. Only a new cell size draws again; a transmit coming back only
// records that the terminal has the picture.
func (w *Widget) takeTerminal(m panelcontent.TerminalMsg) (tea.Cmd, bool) {
	im := w.images()
	if im == nil || !im.Owns(m.Msg) {
		return nil, false
	}
	if !im.Handle(m.Msg) {
		return nil, true
	}
	w.pics.asked = [ImageKeys]uint64{}
	return w.syncPictures(), true
}

// ReleaseImages deletes the pictures from the terminal: the panel leaves
// Info mode. The bars are text until new pictures go out.
func (w *Widget) ReleaseImages() string {
	im := w.images()
	if im == nil {
		return ""
	}
	w.pics.asked = [ImageKeys]uint64{}
	// The next start is no resize.
	w.pics.width, w.pics.settling = 0, false
	return im.Release()
}

// startPictures sends the first pictures when the widget starts. Until
// the terminal has reported its cell size, it asks for it and waits: the
// answer draws them, and a terminal that never answers gets pictures of
// the fallback size at the next tick.
func (w *Widget) startPictures() tea.Cmd {
	im := w.images()
	if im == nil {
		return nil
	}
	if !im.HasCellSize() {
		return kittygfx.QueryCellSize()
	}
	return w.syncPictures()
}

// placePicture replaces the text cells of the picture slot of l by the
// placeholders of its picture, when the terminal has a picture of the
// slot's size; else the text stays.
func (w *Widget) placePicture(l infoLine) string {
	im := w.images()
	s := l.pic
	if im == nil || s == nil || w.pics.settling {
		return l.text
	}
	id, ok := im.Shown(s.key, s.cols, s.rows)
	if !ok {
		return l.text
	}
	return ansi.Truncate(l.text, s.x, "") +
		kittygfx.Color(id) + kittygfx.Cells(s.row, 0, s.cols) + "\x1b[39m" +
		ansi.TruncateLeft(l.text, s.x+s.cols, "")
}

func raw(seq string) tea.Cmd {
	if seq == "" {
		return nil
	}
	return tea.Raw(seq)
}
