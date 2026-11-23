package info

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/promhistory"
)

const (
	// historyRefresh is the shortest time between two reads of one
	// workload's history; a longer step waits for the next step.
	historyRefresh = time.Minute
	// historyRetry spaces reads after a failure.
	historyRetry = 2 * time.Minute
)

// HistorySource reads the past usage of the workload under the other
// panel's cursor from a metrics backend.
type HistorySource struct {
	// Key identifies the context, backend and workload. A new key cancels
	// the read in flight and drops what was shown.
	Key string
	// Window is how far back the history goes, for the heading.
	Window time.Duration
	// Start begins one read on the UI goroutine without I/O: read runs in a
	// tea.Cmd, cancel stops it.
	Start func() (read func() (promhistory.History, error), cancel func())
}

// historyMsg is the result of one read. It is a BackgroundMsg, so it
// reaches the widget behind a dialog too.
type historyMsg struct {
	w   *Widget
	seq int
	h   promhistory.History
	err error
}

func (historyMsg) WidgetBackground() {}

// historyState is the history of the current workload.
type historyState struct {
	key      string
	seq      int // of the read in flight or last finished
	inflight bool
	cancel   func()
	done     time.Time // when the last read finished
	h        *promhistory.History
	err      string
}

// stopHistory cancels the read in flight and forgets the history.
func (w *Widget) stopHistory() {
	if w.hist.cancel != nil {
		w.hist.cancel()
	}
	w.hist = historyState{seq: w.hist.seq}
}

// historyCmd starts a read when the workload changed or its history is due
// again. It runs on the UI goroutine after refresh.
func (w *Widget) historyCmd() tea.Cmd {
	src := w.target.History
	if src == nil || src.Start == nil {
		if w.hist.key != "" {
			w.stopHistory()
		}
		return nil
	}
	if src.Key != w.hist.key {
		w.stopHistory()
		w.hist.key = src.Key
	}
	if w.hist.inflight || !w.historyDue() {
		return nil
	}
	if w.deps.Paused != nil && w.deps.Paused() {
		return nil
	}
	read, cancel := src.Start()
	w.hist.seq++
	w.hist.inflight, w.hist.cancel = true, cancel
	seq := w.hist.seq
	return func() tea.Msg {
		h, err := read()
		return historyMsg{w: w, seq: seq, h: h, err: err}
	}
}

// historyDue reports whether the history should be read again.
func (w *Widget) historyDue() bool {
	if w.hist.done.IsZero() {
		return true
	}
	wait := historyRetry
	if w.hist.err == "" && w.hist.h != nil {
		wait = max(historyRefresh, w.hist.h.Step)
	}
	return w.opts.Now().Sub(w.hist.done) >= wait
}

// takeHistory stores a result unless a newer read or another workload
// replaced the one it answers.
func (w *Widget) takeHistory(m historyMsg) {
	if m.seq != w.hist.seq || !w.hist.inflight {
		return
	}
	if w.hist.cancel != nil {
		w.hist.cancel()
	}
	w.hist.inflight, w.hist.cancel = false, nil
	w.hist.done = w.opts.Now()
	if m.err != nil {
		// Keep the last good history; the line says it is not current.
		w.hist.err = m.err.Error()
		return
	}
	h := m.h
	w.hist.h, w.hist.err = &h, ""
}

// historyLines renders the history block: a heading that names the backend
// and window, a sparkline each for CPU and memory, and what the series
// cover. Nil when no backend is configured for the workload.
func (w *Widget) historyLines(width int) []string {
	src := w.target.History
	if src == nil || w.hist.key != src.Key {
		return nil
	}
	out := []string{""}
	h := w.hist.h
	title := " History, last " + shortDuration(src.Window)
	if h != nil {
		title += " (Prometheus " + h.Source + ")"
	}
	out = append(out, title)
	if w.hist.err != "" {
		out = append(out, "   unavailable: "+w.hist.err)
	}
	if h == nil {
		if w.hist.err == "" {
			out = append(out, "   reading...")
		}
		return out
	}
	switch {
	case h.CPU == nil && h.Memory == nil && h.NoOwners:
		out = append(out,
			"   no kube_pod_owner series in "+h.Target.Namespace+" (kube-state-metrics missing?):",
			"   pods cannot be tied to "+h.Target.String())
		return out
	case h.CPU == nil && h.Memory == nil:
		out = append(out, "   no samples for the pods of "+h.Target.String()+" in this window")
		return out
	}
	out = append(out,
		seriesLine("CPU", *h, h.CPU, width, formatCores),
		seriesLine("Memory", *h, h.Memory, width, formatBytes))
	for _, line := range coverage(*h, w.opts.Now()) {
		out = append(out, "   "+line)
	}
	for _, warn := range h.Warnings {
		out = append(out, "   backend: "+warn)
	}
	return out
}

// seriesLine is "CPU     ▁▂ ▅█  max 1.50, end 0.75": the sparkline over the
// window, gaps blank, scaled from zero to the largest value.
func seriesLine(label string, h promhistory.History, ps []promhistory.Point, width int, format func(float64) string) string {
	head := fmt.Sprintf(" %-*s", labelWidth, label)
	if ps == nil {
		return head + "no series"
	}
	peak, end := math.Inf(-1), ""
	for _, p := range ps {
		peak = math.Max(peak, p.Value)
	}
	if last := ps[len(ps)-1]; last.At.Equal(h.End) {
		end = ", end " + format(last.Value)
	}
	tail := "  max " + format(peak) + end
	cells := width - len([]rune(head)) - len([]rune(tail))
	if cells < 4 {
		return head + strings.TrimSpace(tail)
	}
	return head + scaledSparkline(h.Buckets(ps, cells), peak) + tail
}

// scaledSparkline draws values from zero to peak; NaN is a gap.
func scaledSparkline(cells []float64, peak float64) string {
	var b strings.Builder
	for _, v := range cells {
		if math.IsNaN(v) {
			b.WriteRune(' ')
			continue
		}
		pct := 0.0
		if peak > 0 {
			pct = v / peak * 100
		}
		i := int(math.Round(math.Max(0, math.Min(pct, 100)) / 100 * float64(len(sparks)-1)))
		b.WriteRune(sparks[i])
	}
	return b.String()
}

// coverage says what the series cover: the time span, the step and the
// pods tied to the workload on one line; the steps without a sample (of
// the series with more) and the age of the last sample, when it is not at
// the end of the window, on another.
func coverage(h promhistory.History, now time.Time) []string {
	span := h.Start.Local().Format("15:04") + "-" + h.End.Local().Format("15:04") + ", step " + shortDuration(h.Step)
	if h.PodsKnown {
		span += fmt.Sprintf(", %d pods by owner series", h.Pods)
	}
	var gaps []string
	if missing := max(h.Missing(h.CPU), h.Missing(h.Memory)); missing > 0 {
		gaps = append(gaps, fmt.Sprintf("%d of %d steps without samples", missing, h.Steps()))
	}
	if last := lastSample(h); !last.IsZero() && last.Before(h.End) {
		gaps = append(gaps, "last sample "+shortAge(now.Sub(last))+" ago")
	}
	if len(gaps) == 0 {
		return []string{span}
	}
	return []string{span, strings.Join(gaps, ", ")}
}

func lastSample(h promhistory.History) time.Time {
	var last time.Time
	for _, ps := range [][]promhistory.Point{h.CPU, h.Memory} {
		if len(ps) > 0 && ps[len(ps)-1].At.After(last) {
			last = ps[len(ps)-1].At
		}
	}
	return last
}

func formatCores(v float64) string {
	if v < 1 {
		return fmt.Sprintf("%.0fm", v*1000)
	}
	return fmt.Sprintf("%.2f", v)
}

func formatBytes(v float64) string {
	const mi, gi = 1 << 20, 1 << 30
	switch {
	case v >= gi:
		return fmt.Sprintf("%.1fGi", v/gi)
	default:
		return fmt.Sprintf("%.0fMi", v/mi)
	}
}

// shortDuration prints 1h, 90m, 30s.
func shortDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}
