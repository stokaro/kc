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
	// promHistoryRefresh is the shortest time between two reads of one
	// workload's history; a longer step waits for the next step.
	promHistoryRefresh = time.Minute
	// promHistoryRetry spaces reads after a failure.
	promHistoryRetry = 2 * time.Minute
)

// PromHistorySource reads the past usage of the workload under the other
// panel's cursor from a metrics backend.
type PromHistorySource struct {
	// Key identifies the context, backend and workload. A new key cancels
	// the read in flight and drops what was shown.
	Key string
	// Window is how far back the history goes, for the heading.
	Window time.Duration
	// Start begins one read on the UI goroutine without I/O: read runs in a
	// tea.Cmd, cancel stops it.
	Start func() (read func() (promhistory.History, error), cancel func())
}

// promHistoryMsg is the result of one read. It is a BackgroundMsg, so it
// reaches the widget behind a dialog too.
type promHistoryMsg struct {
	w   *Widget
	seq int
	h   promhistory.History
	err error
}

func (promHistoryMsg) WidgetBackground() {}

// promHistoryState is the history of the current workload.
type promHistoryState struct {
	key      string
	seq      int // of the read in flight or last finished
	inflight bool
	cancel   func()
	done     time.Time // when the last read finished
	h        *promhistory.History
	err      string
}

// stopPromHistory cancels the read in flight and forgets the history.
func (w *Widget) stopPromHistory() {
	if w.prom.cancel != nil {
		w.prom.cancel()
	}
	w.prom = promHistoryState{seq: w.prom.seq}
}

// promHistoryCmd starts a read when the workload changed or its history is due
// again. It runs on the UI goroutine after refresh.
func (w *Widget) promHistoryCmd() tea.Cmd {
	src := w.target.PromHistory
	if src == nil || src.Start == nil {
		if w.prom.key != "" {
			w.stopPromHistory()
		}
		return nil
	}
	if src.Key != w.prom.key {
		w.stopPromHistory()
		w.prom.key = src.Key
	}
	if w.prom.inflight || !w.promHistoryDue() {
		return nil
	}
	if w.deps.Paused != nil && w.deps.Paused() {
		return nil
	}
	read, cancel := src.Start()
	w.prom.seq++
	w.prom.inflight, w.prom.cancel = true, cancel
	seq := w.prom.seq
	return func() tea.Msg {
		h, err := read()
		return promHistoryMsg{w: w, seq: seq, h: h, err: err}
	}
}

// promHistoryDue reports whether the history should be read again.
func (w *Widget) promHistoryDue() bool {
	if w.prom.done.IsZero() {
		return true
	}
	wait := promHistoryRetry
	if w.prom.err == "" && w.prom.h != nil {
		wait = max(promHistoryRefresh, w.prom.h.Step)
	}
	return w.opts.Now().Sub(w.prom.done) >= wait
}

// takePromHistory stores a result unless a newer read or another workload
// replaced the one it answers.
func (w *Widget) takePromHistory(m promHistoryMsg) {
	if m.seq != w.prom.seq || !w.prom.inflight {
		return
	}
	if w.prom.cancel != nil {
		w.prom.cancel()
	}
	w.prom.inflight, w.prom.cancel = false, nil
	w.prom.done = w.opts.Now()
	if m.err != nil {
		// Keep the last good history; the line says it is not current.
		w.prom.err = m.err.Error()
		return
	}
	h := m.h
	w.prom.h, w.prom.err = &h, ""
}

// promHistoryLines renders the history block: a heading that names the backend
// and window, a sparkline each for CPU and memory, and what the series
// cover. Nil when no backend is configured for the workload.
func (w *Widget) promHistoryLines(width int) []string {
	src := w.target.PromHistory
	if src == nil || w.prom.key != src.Key {
		return nil
	}
	out := []string{""}
	h := w.prom.h
	// Named apart from the session sparklines under the bars, which are
	// kc's own metrics.k8s.io samples.
	title := " Prometheus history, last " + promDuration(src.Window)
	if h != nil {
		title += " (" + h.Source + ")"
	}
	out = append(out, title)
	if w.prom.err != "" {
		out = append(out, "   unavailable: "+w.prom.err)
	}
	if h == nil {
		if w.prom.err == "" {
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
		promSeriesLine("CPU", *h, h.CPU, width, promCores),
		promSeriesLine("Memory", *h, h.Memory, width, promBytes))
	for _, line := range promCoverage(*h, w.opts.Now()) {
		out = append(out, "   "+line)
	}
	for _, warn := range h.Warnings {
		out = append(out, "   backend: "+warn)
	}
	return out
}

// promSeriesLine is "CPU     ▁▂ ▅█  max 1.50, end 0.75": the sparkline over the
// window, gaps blank, scaled from zero to the largest value.
func promSeriesLine(label string, h promhistory.History, ps []promhistory.Point, width int, format func(float64) string) string {
	head := fmt.Sprintf(" %-*s", labelWidth, label)
	switch {
	case ps == nil:
		return head + "no series"
	case len(ps) == 0:
		// A series whose samples are all NaN or infinite, or that has no
		// values: missing data, not zero usage.
		return head + "no finite samples in this window"
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
	return head + promSparkline(h.Buckets(ps, cells), peak) + tail
}

// promSparkline draws values from zero to peak; NaN is a gap.
func promSparkline(cells []float64, peak float64) string {
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

// promCoverage says what the series cover: the time span, the step and the
// pods tied to the workload on one line; the steps without a sample (of
// the series with more) and the age of the last sample, when it is not at
// the end of the window, on another.
func promCoverage(h promhistory.History, now time.Time) []string {
	span := h.Start.Local().Format("15:04") + "-" + h.End.Local().Format("15:04") + ", step " + promDuration(h.Step)
	if h.PodsKnown {
		span += fmt.Sprintf(", %d pods by owner series", h.Pods)
	}
	var gaps []string
	if missing := max(h.Missing(h.CPU), h.Missing(h.Memory)); missing > 0 {
		gaps = append(gaps, fmt.Sprintf("%d of %d steps without samples", missing, h.Steps()))
	}
	if last := promLastSample(h); !last.IsZero() && last.Before(h.End) {
		gaps = append(gaps, "last sample "+shortAge(now.Sub(last))+" ago")
	}
	if len(gaps) == 0 {
		return []string{span}
	}
	return []string{span, strings.Join(gaps, ", ")}
}

func promLastSample(h promhistory.History) time.Time {
	var last time.Time
	for _, ps := range [][]promhistory.Point{h.CPU, h.Memory} {
		if len(ps) > 0 && ps[len(ps)-1].At.After(last) {
			last = ps[len(ps)-1].At
		}
	}
	return last
}

func promCores(v float64) string {
	if v < 1 {
		return fmt.Sprintf("%.0fm", v*1000)
	}
	return fmt.Sprintf("%.2f", v)
}

func promBytes(v float64) string {
	const mi, gi = 1 << 20, 1 << 30
	switch {
	case v >= gi:
		return fmt.Sprintf("%.1fGi", v/gi)
	default:
		return fmt.Sprintf("%.0fMi", v/mi)
	}
}

// promDuration prints 1h, 90m, 30s.
func promDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}
