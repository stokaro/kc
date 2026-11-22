// Package info is the Info panel mode, after mc's Info panel (C-x i): bars
// for CPU, memory, pods and disk of what the other panel shows, a sparkline
// of the measured usage, and the pods that use the most memory.
package info

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

const (
	// DefaultInterval is how often the widget reads the summary again. The
	// summary is cheap (the overview engine recomputes in the background);
	// reading it also keeps the engine running.
	DefaultInterval = time.Second
	// historySize is how many usage samples a sparkline keeps: 15 minutes
	// at the 15-second metrics poll.
	historySize = 60
	labelWidth  = 7
)

// Summarizer is the data behind the widget: models.OverviewInfo.
type Summarizer interface {
	Summary(node string) models.InfoSummary
	Close()
}

// Target is what the other panel shows. Resolve builds it on the UI
// goroutine; it must not do I/O.
type Target struct {
	// Key identifies the scope (context and namespace). A new key opens a
	// new Summarizer and starts a new history.
	Key string
	// Label is the scope as the frame shows it.
	Label string
	// Node narrows the summary to one node when the other panel's cursor
	// is on a node.
	Node string
	// Open creates the Summarizer for Key.
	Open func() Summarizer
	// Hint replaces the summary when Open is nil (no cluster yet).
	Hint string
}

// Options configure a widget.
type Options struct {
	Resolve  func(context.Context) Target
	Interval time.Duration
	Now      func() time.Time
}

// tickMsg re-reads the summary. It is a BackgroundMsg, so it reaches the
// widget behind a dialog too.
type tickMsg struct {
	w   *Widget
	gen int
}

func (tickMsg) WidgetBackground() {}

// Widget renders the summary of the other panel's scope.
type Widget struct {
	deps panelcontent.WidgetDeps
	opts Options

	active bool
	gen    int

	target  Target
	src     Summarizer
	summary models.InfoSummary
	history map[string]*history
}

// history holds the usage samples of one scope and node.
type history struct {
	last     time.Time
	cpu, mem []float64
}

func (h *history) add(at time.Time, cpu, mem float64) {
	if !at.After(h.last) {
		return
	}
	h.last = at
	h.cpu = appendBounded(h.cpu, cpu)
	h.mem = appendBounded(h.mem, mem)
}

func appendBounded(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > historySize {
		s = s[len(s)-historySize:]
	}
	return s
}

// New returns an Info widget.
func New(deps panelcontent.WidgetDeps, opts Options) *Widget {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Widget{deps: deps, opts: opts, history: map[string]*history{}}
}

// Init reads the summary at once and starts the ticks.
func (w *Widget) Init(ctx context.Context) tea.Cmd {
	if !w.active {
		return nil
	}
	w.refresh(ctx)
	return w.tick()
}

func (w *Widget) tick() tea.Cmd {
	msg := tickMsg{w: w, gen: w.gen}
	return tea.Tick(w.opts.Interval, func(time.Time) tea.Msg { return msg })
}

// Update handles the widget's own ticks.
func (w *Widget) Update(ctx context.Context, msg tea.Msg) (tea.Cmd, bool) {
	t, ok := msg.(tickMsg)
	if !ok || t.w != w {
		return nil, false
	}
	if t.gen != w.gen || !w.active {
		return nil, true
	}
	w.refresh(ctx)
	return w.tick(), true
}

// SetFocus starts the widget when its panel enters Info mode and stops the
// engine behind it when the panel leaves the mode.
func (w *Widget) SetFocus(_ context.Context, focused bool) {
	if focused == w.active {
		return
	}
	w.active = focused
	w.gen++
	if !focused {
		w.closeSource()
	}
}

// Teardown stops the engine.
func (w *Widget) Teardown(context.Context) {
	w.active = false
	w.gen++
	w.closeSource()
}

func (w *Widget) closeSource() {
	if w.src != nil {
		w.src.Close()
		w.src = nil
	}
	w.target = Target{}
}

// OnSelectionChanged follows the other panel: a new scope or node shows at
// once instead of on the next tick.
func (w *Widget) OnSelectionChanged(ctx context.Context, _ panelcontent.Selection) tea.Cmd {
	if w.active {
		w.refresh(ctx)
	}
	return nil
}

// refresh resolves the target, swaps the source when the scope changed and
// reads the summary.
func (w *Widget) refresh(ctx context.Context) {
	var t Target
	if w.opts.Resolve != nil {
		t = w.opts.Resolve(ctx)
	}
	if t.Key != w.target.Key || w.src == nil {
		if w.src != nil {
			w.src.Close()
			w.src = nil
		}
		if t.Open != nil {
			w.src = t.Open()
		}
	}
	w.target = t
	if w.src == nil {
		w.summary = models.InfoSummary{}
		return
	}
	w.summary = w.src.Summary(t.Node)
	w.record()
}

func (w *Widget) historyKey() string { return w.target.Key + "|" + w.target.Node }

// record adds a sample when the usage was measured again.
func (w *Widget) record() {
	if w.summary.UsageAt.IsZero() {
		return
	}
	cpu, mem, ok := usagePercents(w.summary)
	if !ok {
		return
	}
	h := w.history[w.historyKey()]
	if h == nil {
		h = &history{}
		w.history[w.historyKey()] = h
	}
	h.add(w.summary.UsageAt, cpu, mem)
}

// usagePercents returns the CPU and memory bars when both show usage.
func usagePercents(s models.InfoSummary) (cpu, mem float64, ok bool) {
	var gotCPU, gotMem bool
	for _, b := range s.Bars {
		if !b.Known || !strings.HasPrefix(b.Detail, "use ") {
			continue
		}
		switch b.Label {
		case "CPU":
			cpu, gotCPU = b.Percent, true
		case "Memory":
			mem, gotMem = b.Percent, true
		}
	}
	return cpu, mem, gotCPU && gotMem
}

// View renders the bars, sparklines and top consumers.
func (w *Widget) View(_ context.Context, frame panelcontent.Frame) tea.View {
	return tea.NewView(strings.Join(w.lines(frame.Size.Width, frame.Size.Height), "\n"))
}

func (w *Widget) lines(width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	var out []string
	add := func(s string) { out = append(out, ansi.Truncate(s, width, "")) }
	switch {
	case w.src == nil:
		hint := w.target.Hint
		if hint == "" {
			hint = "No cluster to show."
		}
		add(" " + hint)
		return clip(out, height)
	case w.summary.Err != "":
		add(" Overview unavailable: " + w.summary.Err)
		return clip(out, height)
	case w.summary.Loading:
		add(" Loading...")
		return clip(out, height)
	}
	scope := w.target.Label
	if w.target.Node != "" {
		scope = "node " + w.target.Node
	}
	add(" " + scope)
	if at := w.summary.UsageAt; !at.IsZero() {
		add(fmt.Sprintf(" usage measured %s ago", shortAge(w.opts.Now().Sub(at))))
	} else {
		add(" usage not measured")
	}
	add("")
	h := w.history[w.historyKey()]
	for _, b := range w.summary.Bars {
		add(barLine(b, width))
		add(strings.Repeat(" ", labelWidth+1) + b.Detail)
		if h != nil {
			var samples []float64
			switch b.Label {
			case "CPU":
				samples = h.cpu
			case "Memory":
				samples = h.mem
			}
			if len(samples) > 1 {
				add(strings.Repeat(" ", labelWidth+1) + sparkline(samples, width-labelWidth-2))
			}
		}
	}
	add("")
	add(" Top pods by memory")
	if len(w.summary.Top) == 0 {
		add("   " + w.summary.TopNote)
	}
	for _, c := range w.summary.Top {
		add(fmt.Sprintf("   %8s %6s  %s/%s", c.Memory, c.CPU, c.Namespace, c.Name))
	}
	return clip(out, height)
}

func clip(lines []string, height int) []string {
	if len(lines) > height {
		return lines[:height]
	}
	return lines
}

// barLine renders "CPU     ████▌      55% under-req", colored like a row of
// an object list in the same state.
func barLine(b models.InfoBar, width int) string {
	label := fmt.Sprintf(" %-*s", labelWidth, b.Label)
	if !b.Known {
		return label + "n/a"
	}
	tail := fmt.Sprintf(" %3.0f%%", b.Percent)
	if b.Flag != "" && b.Flag != "ok" {
		tail += " " + b.Flag
	}
	n := width - ansi.StringWidth(label) - ansi.StringWidth(tail)
	if n < 4 {
		n = 4
	}
	bar := renderBar(b.Percent, n)
	style := models.RowHealthStyle(b.Health, nil)
	if style != nil {
		bar = style.Render(bar)
		tail = style.Render(tail)
	}
	return label + bar + tail
}

// eighths are the partial block characters, from one eighth to seven.
var eighths = []rune("▏▎▍▌▋▊▉")

// renderBar fills width cells to pct percent (capped at 100) in eighths of
// a cell, padding the rest with a light shade.
func renderBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	pct = math.Max(0, math.Min(pct, 100))
	units := int(math.Round(pct / 100 * float64(width*8)))
	full, part := units/8, units%8
	var b strings.Builder
	b.WriteString(strings.Repeat("█", full))
	cells := full
	if part > 0 {
		b.WriteRune(eighths[part-1])
		cells++
	}
	b.WriteString(strings.Repeat("░", width-cells))
	return b.String()
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the last width samples (percent, 0 to 100).
func sparkline(samples []float64, width int) string {
	if width <= 0 || len(samples) == 0 {
		return ""
	}
	if len(samples) > width {
		samples = samples[len(samples)-width:]
	}
	var b strings.Builder
	for _, v := range samples {
		v = math.Max(0, math.Min(v, 100))
		i := int(math.Round(v / 100 * float64(len(sparks)-1)))
		b.WriteRune(sparks[i])
	}
	return b.String()
}

func shortAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// FrameInfo names the scope in the frame.
func (w *Widget) FrameInfo(context.Context, panelcontent.FrameInfoRequest) panelcontent.FrameInfo {
	info := panelcontent.FrameInfo{SuppressFooter: true, TopIndicator: "─", BottomIndicator: "─", Breadcrumb: "Info"}
	if w.target.Label != "" {
		info.Breadcrumb = "Info: " + w.target.Label
	}
	if w.src != nil && w.summary.Loading {
		info.HeaderStatus = panelcontent.LoadingStatus
	}
	return info
}
