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
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

const (
	// DefaultInterval is how often the widget reads the summary again. The
	// summary is cheap (the overview engine recomputes in the background);
	// reading it also keeps the engine running.
	DefaultInterval = time.Second
	labelWidth      = 7
	// maxBarCells keeps a bar readable at any pane width: its percentage
	// follows it instead of the pane's right edge, and the room after it
	// takes the first line under the bar (ADR 0176).
	maxBarCells = 40
	// minBarCells is the narrowest bar; a narrower pane moves the
	// percentage and the marks to the next line instead of cutting them.
	minBarCells = 4
	// readableBarCells is the shortest bar that keeps the attention marks
	// on its line; below it the marks move to the next line.
	readableBarCells = 10
)

// Summarizer is the data behind the widget: models.OverviewInfo.
type Summarizer interface {
	Summary(focus models.InfoFocus) models.InfoSummary
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
	// Workload narrows the summary to one workload when the other panel's
	// cursor is on a Deployment, StatefulSet, DaemonSet, ReplicaSet, Job or
	// CronJob. It wins over Node.
	Workload *models.InfoWorkload
	// Finding replaces the summary when the other panel's cursor is on an
	// overview finding. Resolve builds it from the overview's latest report,
	// so it is never older than the row it describes.
	Finding *models.FindingPreview
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

	// width and height are the last content size; offset is the first
	// line shown. focus is the namespace/name of the selected top pod, ""
	// for none: a live update keeps it while the pod is listed.
	width, height int
	offset        int
	focus         string
}

// New returns an Info widget.
func New(deps panelcontent.WidgetDeps, opts Options) *Widget {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Widget{deps: deps, opts: opts, history: make(map[string]*history)}
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

// Update handles the widget's own ticks, its size, and the keys and mouse
// events that scroll it and select a top pod.
func (w *Widget) Update(ctx context.Context, msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case tickMsg:
		if m.w != w {
			return nil, false
		}
		if m.gen != w.gen || !w.active {
			return nil, true
		}
		w.refresh(ctx)
		return w.tick(), true
	case tea.WindowSizeMsg:
		w.width, w.height = m.Width, m.Height
		return nil, true
	case tea.KeyMsg:
		return w.handleKey(m.String())
	case panelcontent.MouseMsg:
		return w.handleMouse(m)
	}
	return nil, false
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
	if !sameScope(t, w.target) {
		// Another scope, node or workload starts at the top, unselected.
		w.offset, w.focus = 0, ""
	}
	w.target = t
	if w.src == nil {
		w.summary = models.InfoSummary{}
		w.focus = ""
		return
	}
	w.summary = w.src.Summary(models.InfoFocus{Node: t.Node, Workload: t.Workload})
	if w.focusIndex() < 0 {
		w.focus = ""
	}
	w.record()
}

// sameScope reports whether a and b show the same scope, node, workload
// and overview finding.
func sameScope(a, b Target) bool {
	if a.Key != b.Key || a.Node != b.Node || (a.Workload == nil) != (b.Workload == nil) || findingID(a) != findingID(b) {
		return false
	}
	return a.Workload == nil || *a.Workload == *b.Workload
}

// findingID is the overview finding a target previews, or "".
func findingID(t Target) string {
	if t.Finding == nil {
		return ""
	}
	return t.Finding.ID
}

// historyKey separates the samples of the scope, each node and each
// workload. A workload key carries its full GVR.
func (w *Widget) historyKey() string {
	if wl := w.target.Workload; wl != nil {
		return w.target.Key + "|" + wl.GVR.String() + "|" + wl.Namespace + "/" + wl.Name
	}
	return w.target.Key + "|" + w.target.Node
}

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

// usagePercents returns the CPU and memory bars when both show usage. A
// measurement that is not available adds no sample, never a zero.
func usagePercents(s models.InfoSummary) (cpu, mem usageSample, ok bool) {
	var gotCPU, gotMem bool
	for _, b := range s.Bars {
		if !b.Known || !b.Usage {
			continue
		}
		switch b.Label {
		case "CPU":
			cpu, gotCPU = usageSample{percent: b.Percent, base: b.Base}, true
		case "Memory":
			mem, gotMem = usageSample{percent: b.Percent, base: b.Base}, true
		}
	}
	return cpu, mem, gotCPU && gotMem
}

// View renders the visible part of the bars, sparklines and top pods.
func (w *Widget) View(_ context.Context, frame panelcontent.Frame) tea.View {
	w.width, w.height = frame.Size.Width, frame.Size.Height
	return tea.NewView(strings.Join(w.lines(frame.Size.Width, frame.Size.Height), "\n"))
}

// lines are the height lines of the content from the scroll offset, which
// they clamp, with the selected top pod highlighted.
func (w *Widget) lines(width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	all := w.content(width)
	w.offset = clampOffset(w.offset, len(all), height)
	end := min(len(all), w.offset+height)
	focus := w.focusIndex()
	out := make([]string, 0, end-w.offset)
	for _, l := range all[w.offset:end] {
		s := l.text
		if focus >= 0 && l.consumer == focus {
			s = uistyles.PanelItemSelectedStyle.Width(width).Render(ansi.Strip(s))
		}
		out = append(out, s)
	}
	return out
}

// infoLine is one line of the content. consumer is the index of the top
// pod it shows, or -1.
type infoLine struct {
	text     string
	consumer int
}

// content is every line at width. Long lines wrap with their indent
// instead of being cut, so no state word, unit or reason is lost.
func (w *Widget) content(width int) []infoLine {
	if width <= 0 {
		return nil
	}
	var out []infoLine
	add := func(s string) { out = append(out, infoLine{text: ansi.Truncate(s, width, ""), consumer: -1}) }
	para := func(indent, s string) {
		for _, part := range wrapText(s, width-len(indent)) {
			add(indent + part)
		}
	}
	if p := w.target.Finding; p != nil {
		for _, line := range findingLines(*p, width) {
			add(line)
		}
		return out
	}
	switch {
	case w.src == nil:
		hint := w.target.Hint
		if hint == "" {
			hint = "No cluster to show."
		}
		para(" ", hint)
		return out
	case w.summary.Err != "":
		para(" ", "Overview unavailable: "+w.summary.Err)
		return out
	case w.summary.Loading:
		add(" Loading...")
		return out
	}
	scope := w.target.Label
	switch {
	case w.target.Workload != nil:
		scope = w.target.Workload.String()
	case w.target.Node != "":
		scope = "node " + w.target.Node
	}
	para(" ", scope)
	if at := w.summary.UsageAt; !at.IsZero() {
		add(fmt.Sprintf(" usage measured %s ago", shortAge(w.opts.Now().Sub(at))))
	} else {
		add(" usage not measured")
	}
	add("")
	h := w.history[w.historyKey()]
	if t := w.summary.BarsTitle; t != "" {
		para(" ", t)
	}
	cells := barCells(w.summary.Bars, width)
	for _, b := range w.summary.Bars {
		for _, line := range barBlock(b, h, w.opts.Now(), width, cells) {
			add(line)
		}
	}
	add("")
	top := w.summary.TopTitle
	if top == "" {
		top = "Top pods by memory"
	}
	para(" ", top)
	if len(w.summary.Top) == 0 {
		para("   ", w.summary.TopNote)
	}
	for i, c := range w.summary.Top {
		for _, line := range consumerLines(c, width) {
			out = append(out, infoLine{text: ansi.Truncate(line, width, ""), consumer: i})
		}
	}
	return out
}

// consumerLines renders a top pod as "  2048Mi   120m  db/postgres-0".
// A long name wraps under itself; in a very narrow pane the whole entry
// wraps.
func consumerLines(c models.InfoConsumer, width int) []string {
	prefix := fmt.Sprintf("   %8s %6s  ", c.Memory, c.CPU)
	name := c.Namespace + "/" + c.Name
	n := width - len(prefix)
	if n < 12 {
		var out []string
		for _, part := range wrapText(strings.TrimSpace(prefix)+"  "+name, width-3) {
			out = append(out, "   "+part)
		}
		return out
	}
	parts := wrapText(name, n)
	out := []string{prefix + parts[0]}
	for _, part := range parts[1:] {
		out = append(out, strings.Repeat(" ", len(prefix))+part)
	}
	return out
}

// wrapText splits plain text into lines of at most width cells, at spaces
// and, for longer words, inside them. Empty text is one empty line.
func wrapText(s string, width int) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(ansi.Wrap(s, max(width, 1), ""), "\n")
}

// detailIndent is the indent of the lines under a bar: under the bar
// itself, or two cells in a pane too narrow for that.
func detailIndent(width int) string {
	if width < labelWidth+1+16 {
		return "  "
	}
	return strings.Repeat(" ", labelWidth+1)
}

// barBlock is one bar with the lines under it, each in its own state, and
// the sparkline of its usage samples in h (nil when none) with the line
// that says what the sparkline covers. When the bar leaves room on its
// line, the first line under it moves up next to it.
func barBlock(b models.InfoBar, h *history, now time.Time, width, cells int) []string {
	indent := detailIndent(width)
	line, spill := barLine(b, width, cells)
	out := []string{line}
	if spill != "" {
		out = append(out, indent+spill)
	}
	para := func(s string, health models.RowHealth) {
		for _, part := range wrapText(s, width-len(indent)) {
			out = append(out, indent+styled(part, health))
		}
	}
	if spill == "" && b.Detail != "" && width-ansi.StringWidth(line) >= 2+ansi.StringWidth(b.Detail) {
		out[0] = line + "  " + styled(b.Detail, b.DetailHealth)
	} else {
		para(b.Detail, b.DetailHealth)
	}
	for i, line := range b.More {
		health := models.RowHealthUnknown
		if i < len(b.MoreHealth) {
			health = b.MoreHealth[i]
		}
		para(line, health)
	}
	if h == nil {
		return out
	}
	var s series
	switch b.Label {
	case "CPU":
		s = h.cpu
	case "Memory":
		s = h.mem
	default:
		return out
	}
	view, ok := s.render(now, width-len(indent)-1)
	if !ok {
		return out
	}
	if view.line != "" {
		out = append(out, indent+view.line)
	}
	para(view.label, models.RowHealthUnknown)
	return out
}

// styled renders s like a row of an object list in state h; ok and unknown
// lines stay plain.
func styled(s string, h models.RowHealth) string {
	if style := models.RowHealthStyle(h, nil); style != nil && s != "" {
		return style.Render(s)
	}
	return s
}

// barParts are the label, the percentage with its state word, and the
// attention mark of a bar, as barLine prints them.
func barParts(b models.InfoBar) (label, tail, attention string) {
	label = fmt.Sprintf(" %-*s", labelWidth, b.Label)
	// Truncated, as kubectl describe node prints a percentage.
	tail = fmt.Sprintf(" %3.0f%%", math.Floor(b.Percent))
	if b.Flag != "" && b.Flag != "ok" {
		tail += " " + b.Flag
	}
	if b.AttentionText != "" {
		attention = " " + b.AttentionText
	}
	return label, tail, attention
}

// barCells is the width of every bar at width: at most maxBarCells, and
// narrow enough that each bar's percentage and attention mark fit after
// it. The bars share it so their lengths compare. When the marks would
// leave less than readableBarCells, the marks move to the next line
// instead, and only the percentages must fit.
func barCells(bars []models.InfoBar, width int) int {
	withMarks, withoutMarks := maxBarCells, maxBarCells
	for _, b := range bars {
		if !b.Known {
			continue
		}
		label, tail, attention := barParts(b)
		rest := width - ansi.StringWidth(label) - ansi.StringWidth(tail)
		withoutMarks = min(withoutMarks, rest)
		withMarks = min(withMarks, rest-ansi.StringWidth(attention))
	}
	if withMarks >= readableBarCells {
		return withMarks
	}
	return max(withoutMarks, minBarCells)
}

// barLine renders "CPU     ████▌      55% under-req ! lim" with a bar of
// cells. The bar and its percentage are colored by the state of what the
// bar shows; the attention mark names another line that needs attention,
// in that line's color, so the warning stays readable without color. What
// does not fit after the bar moves to the next line, spill: the mark, or
// in a very narrow pane the percentage too. Nothing is cut.
func barLine(b models.InfoBar, width, cells int) (line, spill string) {
	label, tail, attention := barParts(b)
	if !b.Known {
		return label + "n/a", ""
	}
	lw, tw, aw := ansi.StringWidth(label), ansi.StringWidth(tail), ansi.StringWidth(attention)
	bar := label + styled(renderBar(b.Percent, cells), b.Health)
	switch {
	case lw+cells+tw+aw <= width:
		return bar + styled(tail, b.Health) + styled(attention, b.Attention), ""
	case lw+cells+tw <= width:
		return bar + styled(tail, b.Health), styled(strings.TrimSpace(attention), b.Attention)
	default:
		return bar, styled(strings.TrimSpace(tail), b.Health) + styled(attention, b.Attention)
	}
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

func shortAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// FrameInfo names the scope in the frame. When the content is taller than
// the pane, the frame marks the hidden lines above and below and shows
// which lines are visible; it says which key selects or opens a top pod.
func (w *Widget) FrameInfo(_ context.Context, req panelcontent.FrameInfoRequest) panelcontent.FrameInfo {
	info := panelcontent.FrameInfo{SuppressFooter: true, TopIndicator: "─", BottomIndicator: "─", Breadcrumb: "Info"}
	if w.target.Label != "" {
		info.Breadcrumb = "Info: " + w.target.Label
	}
	if w.src != nil && w.summary.Loading {
		info.HeaderStatus = panelcontent.LoadingStatus
	}
	var status []string
	switch {
	case w.focusIndex() >= 0:
		status = append(status, "Enter opens the pod")
	case len(w.summary.Top) > 0 && w.src != nil && w.target.Finding == nil:
		status = append(status, "Down selects a pod")
	}
	width := req.Width
	if width <= 0 {
		width = w.width
	}
	if total := len(w.content(width)); w.height > 0 && total > w.height {
		offset := clampOffset(w.offset, total, w.height)
		if offset > 0 {
			info.TopIndicator = "^"
		}
		if offset+w.height < total {
			info.BottomIndicator = "v"
		}
		status = append(status, fmt.Sprintf("%d-%d/%d", offset+1, min(offset+w.height, total), total))
	}
	info.FooterStatus = strings.Join(status, " • ")
	return info
}
