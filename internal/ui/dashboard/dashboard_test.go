package dashboard_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
	"github.com/stokaro/kc/internal/ui/dashboard"
)

var (
	t0        = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	deployGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	web       = dashboard.Target{Kind: dashboard.KindWorkload, Namespace: "shop", Name: "web", Workload: deployGVR}
)

// fakes are the sources of a test dashboard: a clock, the engine's
// answer, and recorded Prometheus reads.
type fakes struct {
	mu       sync.Mutex
	now      time.Time
	amounts  models.UsageAmounts
	focuses  []models.UsageFocus
	reads    []promhistory.Scope
	ranges   []promhistory.Range
	canceled int
	usage    func(promhistory.Scope, promhistory.Range) (promhistory.Usage, error)
}

func (f *fakes) sources(prom bool) dashboard.Sources {
	src := dashboard.Sources{
		Context: "kind-test",
		Now:     func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		Loc:     time.UTC,
		Tick:    func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil },
		Amounts: func(focus models.UsageFocus, _ int) models.UsageAmounts {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.focuses = append(f.focuses, focus)
			return f.amounts
		},
	}
	if prom {
		src.Backend = "monitoring/prometheus"
		src.Usage = func(s promhistory.Scope, r promhistory.Range) (func() (promhistory.Usage, error), func()) {
			f.mu.Lock()
			f.reads, f.ranges = append(f.reads, s), append(f.ranges, r)
			read := f.usage
			f.mu.Unlock()
			return func() (promhistory.Usage, error) {
					if read == nil {
						return steadyUsage(s, r, 1), nil
					}
					return read(s, r)
				}, func() {
					f.mu.Lock()
					f.canceled++
					f.mu.Unlock()
				}
		}
	}
	return src
}

func (f *fakes) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	if !f.amounts.ReadAt.IsZero() {
		f.amounts.ReadAt = f.now
		f.amounts.At = f.now
	}
}

// steadyUsage is a full-coverage usage of v cores and v GiB at every
// step of r.
func steadyUsage(s promhistory.Scope, r promhistory.Range, v float64) promhistory.Usage {
	u := promhistory.Usage{Scope: s, Asked: r, Range: r, RateWindow: max(5*time.Minute, r.Step), PeakResolution: max(15*time.Second, r.Step/10)}
	for at := r.Start; !at.After(r.End); at = at.Add(r.Step) {
		u.CPU = append(u.CPU, promhistory.Point{At: at, Value: v})
		u.Memory = append(u.Memory, promhistory.Point{At: at, Value: v * (1 << 30)})
		u.MemoryPeak = append(u.MemoryPeak, promhistory.Point{At: at, Value: v * (1 << 30)})
		u.Measured = append(u.Measured, promhistory.Point{At: at, Value: 2})
		u.Expected = append(u.Expected, promhistory.Point{At: at, Value: 2})
	}
	return u
}

func measured() models.UsageAmounts {
	return models.UsageAmounts{
		At: t0, ReadAt: t0, Found: true, UID: "dep-uid", Created: t0.Add(-48 * time.Hour),
		CPU:      models.ResourceAmount{Used: 0.4, UsedKnown: true, Requests: 1, Limits: 2},
		Memory:   models.ResourceAmount{Used: 300 << 20, UsedKnown: true, Requests: 512 << 20, Limits: 1 << 30},
		Measured: 2, Running: 2, Pods: 2,
		Top: []models.UsageConsumer{
			{Namespace: "shop", Name: "web-1", CPU: 0.3, Memory: 200 << 20},
			{Namespace: "shop", Name: "web-2", CPU: 0.1, Memory: 100 << 20},
		},
	}
}

// run feeds cmd's messages back into m until nothing is left.
func run(t *testing.T, m *dashboard.Model, cmd tea.Cmd) dashboard.Action {
	t.Helper()
	var last dashboard.Action
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			next, act := m.Update(msg)
			if act != (dashboard.Action{}) {
				last = act
			}
			queue = append(queue, next)
		}
	}
	return last
}

func open(t *testing.T, f *fakes, prom bool, target dashboard.Target, w, h int) *dashboard.Model {
	t.Helper()
	m := dashboard.New(f.sources(prom), target)
	cmd, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	run(t, m, cmd)
	run(t, m, m.Start())
	return m
}

func key(t *testing.T, m *dashboard.Model, k string) dashboard.Action {
	t.Helper()
	var msg tea.KeyPressMsg
	switch k {
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "left":
		msg = tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		msg = tea.KeyPressMsg{Code: tea.KeyRight}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	default:
		msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
	}
	cmd, act := m.Update(msg)
	if a := run(t, m, cmd); a != (dashboard.Action{}) {
		return a
	}
	return act
}

// screen renders m and checks its size.
func screen(t *testing.T, m *dashboard.Model, w, h int) []string {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != h {
		t.Fatalf("%d lines, want %d", len(lines), h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != w {
			t.Fatalf("line %d is %d cells wide, want %d: %q", i, lw, w, ansi.Strip(l))
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

func contains(lines []string, s string) bool {
	return strings.Contains(strings.Join(lines, "\n"), s)
}

func requireText(t *testing.T, lines []string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !contains(lines, s) {
			t.Errorf("screen lacks %q:\n%s", s, strings.Join(lines, "\n"))
		}
	}
}

func ticks(t *testing.T, f *fakes, m *dashboard.Model, n int, every time.Duration) {
	t.Helper()
	for range n {
		f.advance(every)
		cmd, _ := m.Update(dashboard.TickMsgForTest(m))
		run(t, m, cmd)
	}
}

// TestMetricsAPIOnly: without Prometheus the charts plot the session
// samples and say where they come from; a context without a backend is
// named, never drawn as zero.
func TestMetricsAPIOnly(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, false, web, 80, 24)
	ticks(t, f, m, 4, 15*time.Second)
	lines := screen(t, m, 80, 24)
	requireText(t, lines, "Deployment shop/web", "LIVE 15m", "session: 5 samples from 10:00:00",
		"no Prometheus (C-x m)", "now CPU 0.4 req 1 lim 2 · Memory 300Mi req 512Mi lim 1Gi · 2/2 pods measured",
		"Top pods by memory", "web-1", "Esc back")
	if f.focuses[0].Workload == nil || f.focuses[0].Workload.Name != "web" || f.focuses[0].Workload.GVR != deployGVR {
		t.Errorf("focus = %+v, want the Deployment with its GVR", f.focuses[0])
	}
}

// TestNoMeasurementIsNotZero: a pod set without PodMetrics says so in the
// status line and draws no line at all.
func TestNoMeasurementIsNotZero(t *testing.T) {
	t.Parallel()
	a := measured()
	a.CPU.Used, a.CPU.UsedKnown, a.Memory.Used, a.Memory.UsedKnown, a.Top = 0, false, 0, false, nil
	a.Note = "metrics unavailable: metrics.k8s.io is not served"
	f := &fakes{now: t0, amounts: a}
	m := open(t, f, false, web, 80, 24)
	ticks(t, f, m, 3, 15*time.Second)
	lines := screen(t, m, 80, 24)
	requireText(t, lines, "metrics unavailable: metrics.k8s.io is not served", "no samples yet")
	for _, l := range lines[2:20] {
		if strings.Contains(l, "│") && strings.ContainsAny(l, "⡀⣀⣿▁█") {
			t.Fatalf("a line is drawn without measurements: %q", l)
		}
	}
}

func TestLayoutsAtEverySize(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{80, 24}, {120, 30}, {240, 60}, {100, 14}, {40, 10}} {
		f := &fakes{now: t0, amounts: measured()}
		m := open(t, f, true, web, size[0], size[1])
		lines := screen(t, m, size[0], size[1])
		paged := strings.Contains(lines[size[1]-1], "Tab next widget")
		wantPaged := size[1] < 14
		if paged != wantPaged {
			t.Errorf("%v: paged %v, want %v:\n%s", size, paged, wantPaged, strings.Join(lines, "\n"))
		}
		if !paged {
			requireText(t, lines, "CPU cores", "Memory", "Top pods by memory")
		}
		if size[0] >= 120 {
			// Side by side: both chart titles on one line.
			if !strings.Contains(lines[2], "CPU cores") || !strings.Contains(lines[2], "Memory") {
				t.Errorf("%v: charts not side by side: %q", size, lines[2])
			}
		}
		// Every widget is reachable with Tab.
		seen := make(map[string]bool)
		for range 3 {
			l := screen(t, m, size[0], size[1])
			for _, name := range []string{"▸ CPU cores", "▸ Memory", "▸ Top pods"} {
				if contains(l, name) {
					seen[name] = true
				}
			}
			key(t, m, "tab")
		}
		if len(seen) != 3 {
			t.Errorf("%v: Tab reached %v", size, seen)
		}
	}
}

func TestPrometheusSourceCoverageAndIdentity(t *testing.T) {
	t.Parallel()
	a := measured()
	a.Created = t0.Add(-20 * time.Minute)
	f := &fakes{now: t0, amounts: a}
	f.usage = func(s promhistory.Scope, r promhistory.Range) (promhistory.Usage, error) {
		u := steadyUsage(s, r, 1)
		u.Clipped, u.Range.Start = true, s.Since.Truncate(r.Step).Add(r.Step)
		u.Measured[len(u.Measured)-1].Value = 1
		return u, nil
	}
	m := open(t, f, true, web, 120, 30)
	if len(f.reads) != 1 {
		t.Fatalf("%d reads, want 1", len(f.reads))
	}
	s := f.reads[0]
	if s.Kind != promhistory.ScopeWorkload || s.Workload.Kind != "Deployment" || s.UID != "dep-uid" || !s.Since.Equal(a.Created) {
		t.Errorf("scope = %+v, want the Deployment with its UID and creation", s)
	}
	if r := f.ranges[0]; r.End.After(t0) || t0.Sub(r.Start) < time.Hour || r.Steps() > 2*120 {
		t.Errorf("range %v..%v step %v: want the last hour at the chart's resolution", r.Start, r.End, r.Step)
	}
	lines := screen(t, m, 120, 30)
	requireText(t, lines, "LIVE 1h", "Prometheus monitoring/prometheus", "5m avg", "partial 1/", "created 09:4")
}

func TestPrometheusFailureFallsBackToTheSession(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	f.usage = func(promhistory.Scope, promhistory.Range) (promhistory.Usage, error) {
		return promhistory.Usage{}, errors.New("prometheus monitoring/prometheus: 403 (needs get on services/proxy)\ndetails")
	}
	m := open(t, f, true, web, 120, 30)
	ticks(t, f, m, 2, 10*time.Second)
	lines := screen(t, m, 120, 30)
	requireText(t, lines, "session: 3 samples", "Metrics API: Prometheus failed: 403", "LIVE 15m")
	// A failed read is retried after a pause, not every tick.
	if len(f.reads) != 1 {
		t.Errorf("%d reads within 30s of a failure, want 1", len(f.reads))
	}
	ticks(t, f, m, 1, 30*time.Second)
	if len(f.reads) != 2 {
		t.Errorf("%d reads after 60s, want a retry", len(f.reads))
	}
}

func TestLiveRefreshAndPausedReads(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 80, 24)
	ticks(t, f, m, 5, time.Second)
	if len(f.reads) != 1 {
		t.Fatalf("%d reads in 5s, want 1", len(f.reads))
	}
	ticks(t, f, m, 1, time.Minute)
	if len(f.reads) != 2 {
		t.Fatalf("%d reads after a minute, want a reread", len(f.reads))
	}
	key(t, m, "space")
	ticks(t, f, m, 3, time.Minute)
	if len(f.reads) != 2 {
		t.Errorf("a paused view reread: %d reads", len(f.reads))
	}
	// Zooming a paused view reads the new range once, ending at the pause.
	key(t, m, "+")
	if len(f.reads) != 3 || f.ranges[2].End.After(t0.Add(65*time.Second)) || f.ranges[2].End.Sub(f.ranges[2].Start) > 16*time.Minute {
		t.Errorf("zoom: reads %d, last range %v..%v", len(f.reads), f.ranges[len(f.ranges)-1].Start, f.ranges[len(f.ranges)-1].End)
	}
	requireText(t, screen(t, m, 80, 24), "PAUSED at 10:01:05 15m")
}

// TestPauseCursorAndEsc: the cursor pauses and shows values; Enter
// expands; Esc collapses, then removes the cursor, then closes.
func TestPauseCursorAndEsc(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 80, 24)
	key(t, m, "left")
	lines := screen(t, m, 80, 24)
	requireText(t, lines, "PAUSED at 10:00:00", "CPU 1  ·  Memory 1Gi", "(max in the cell)")
	// Paused, the status line keeps the values of the pause.
	f.amounts.CPU.Used = 9
	ticks(t, f, m, 2, 15*time.Second)
	if contains(screen(t, m, 80, 24), "CPU 9") {
		t.Error("a paused dashboard shows new values")
	}
	key(t, m, "enter")
	lines = screen(t, m, 80, 24)
	if contains(lines, "Memory ·") || !contains(lines, "▸ CPU cores") {
		t.Errorf("Enter did not expand the CPU chart:\n%s", strings.Join(lines, "\n"))
	}
	if act := key(t, m, "esc"); act.Close {
		t.Fatal("first Esc closed the dashboard")
	}
	if !contains(screen(t, m, 80, 24), "Memory ·") {
		t.Error("first Esc did not collapse the chart")
	}
	if act := key(t, m, "esc"); act.Close {
		t.Fatal("second Esc closed the dashboard")
	}
	lines = screen(t, m, 80, 24)
	if contains(lines, "(max in the cell)") || !contains(lines, "PAUSED") {
		t.Error("second Esc must remove the cursor and stay paused")
	}
	if act := key(t, m, "esc"); !act.Close {
		t.Error("third Esc must close")
	}
}

func TestDrilldownAndOpen(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 80, 24)
	if act := key(t, m, "o"); act.Open == nil || act.Open.GVR != deployGVR || act.Open.Name != "web" || act.Open.UID != types.UID("dep-uid") {
		t.Errorf("o on the workload = %+v", act.Open)
	}
	key(t, m, "tab")
	key(t, m, "tab")
	key(t, m, "down")
	if act := key(t, m, "o"); act.Open == nil || act.Open.GVR.Resource != "pods" || act.Open.Name != "web-2" {
		t.Errorf("o on a consumer = %+v", act.Open)
	}
	reads := len(f.reads)
	key(t, m, "enter")
	lines := screen(t, m, 80, 24)
	requireText(t, lines, "Deployment shop/web > pod shop/web-2")
	last := f.focuses[len(f.focuses)-1]
	if last.Pod != "web-2" || last.PodNamespace != "shop" {
		t.Errorf("drilldown focus = %+v", last)
	}
	if len(f.reads) != reads+1 || f.reads[len(f.reads)-1].Kind != promhistory.ScopePod {
		t.Errorf("drilldown reads = %+v", f.reads[reads:])
	}
	if act := key(t, m, "esc"); act.Close {
		t.Fatal("Esc in a drilldown closed the dashboard")
	}
	requireText(t, screen(t, m, 80, 24), "Deployment shop/web │")
}

// TestWaitsForTheIdentity: a workload's history starts at its creation,
// so no read starts while the engine is loading; a late result of a read
// that was replaced is dropped; Stop cancels a read in flight.
func TestWaitsForTheIdentity(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: models.UsageAmounts{Loading: true}}
	release := make(chan struct{})
	f.usage = func(s promhistory.Scope, r promhistory.Range) (promhistory.Usage, error) {
		<-release
		return steadyUsage(s, r, 1), nil
	}
	m := dashboard.New(f.sources(true), web)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	start := m.Start()
	if len(f.reads) != 0 {
		t.Fatalf("read before the identity was known: %+v", f.reads)
	}
	_ = start
	f.amounts = measured()
	cmd, _ := m.Update(dashboard.TickMsgForTest(m))
	if len(f.reads) != 1 || f.reads[0].UID != "dep-uid" {
		t.Fatalf("reads = %+v, want one with the UID", f.reads)
	}
	m.Stop()
	if f.canceled != 1 {
		t.Errorf("Stop canceled %d reads, want 1", f.canceled)
	}
	close(release)
	run(t, m, cmd)
	if contains(screen(t, m, 80, 24), "5m avg") {
		t.Error("a result that arrived after Stop was shown")
	}
}

func TestResume(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, false, web, 80, 24)
	ticks(t, f, m, 2, 15*time.Second)
	key(t, m, "tab")
	key(t, m, "tab")
	key(t, m, "enter")
	m.Stop()
	// Same context: the drilldown and the session history come back.
	m = m.Resume(f.sources(false), web)
	run(t, m, m.Start())
	requireText(t, screen(t, m, 80, 24), "> pod shop/web-1")
	key(t, m, "esc")
	requireText(t, screen(t, m, 80, 24), "3 samples")
	// Another context starts at the target again.
	m.Stop()
	other := f.sources(false)
	other.Context = "kind-other"
	m = m.Resume(other, dashboard.Target{Kind: dashboard.KindScope, Namespace: models.AllNamespaces})
	run(t, m, m.Start())
	lines := screen(t, m, 80, 24)
	requireText(t, lines, "all namespaces │ kind-other")
	if contains(lines, "pod shop/web-1") {
		t.Error("a dashboard of another context kept the drilldown")
	}
}

func TestStaleTickIsDropped(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, false, web, 80, 24)
	old := dashboard.TickMsgForTest(m)
	m.Stop()
	n := len(f.focuses)
	cmd, _ := m.Update(old)
	if cmd != nil || len(f.focuses) != n {
		t.Error("a tick of a closed dashboard still read the engine")
	}
	if !dashboard.IsMsg(old) || dashboard.IsMsg(tea.KeyPressMsg{}) {
		t.Error("IsMsg does not tell dashboard messages apart")
	}
}

func TestMouse(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 80, 24)
	screen(t, m, 80, 24)
	// Rows 2..8 are the CPU chart; click its plot.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 40, Y: 5})
	lines := screen(t, m, 80, 24)
	requireText(t, lines, "PAUSED", "(max in the cell)")
	// The wheel zooms.
	cmd, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 40, Y: 5})
	run(t, m, cmd)
	requireText(t, screen(t, m, 80, 24), "15m")
	// Clicking a consumer selects it; clicking it again opens its dashboard.
	var topRow int
	for i, l := range screen(t, m, 80, 24) {
		if strings.Contains(l, "web-2") {
			topRow = i
		}
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: topRow})
	cmd, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: topRow})
	run(t, m, cmd)
	requireText(t, screen(t, m, 80, 24), "> pod shop/web-2")
}

func TestBlocksToggle(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 120, 30)
	if !strings.ContainsAny(strings.Join(screen(t, m, 120, 30), ""), "⣀⣤⣶⣿") {
		t.Fatal("braille mode draws no braille")
	}
	key(t, m, "b")
	lines := screen(t, m, 120, 30)
	if strings.ContainsAny(strings.Join(lines, ""), "⣀⣤⣶⣿") || !contains(lines, "b braille") {
		t.Error("block mode still draws braille")
	}
}

// TestChartsShareTheCellGrid: the CPU and memory charts have labels of
// different widths, yet their plots start at the same column, so one
// cursor cell is one time in both.
func TestChartsShareTheCellGrid(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 80, 24)
	var axes []int
	for _, l := range screen(t, m, 80, 24) {
		if i := strings.IndexRune(l, '└'); i >= 0 {
			axes = append(axes, len([]rune(l[:i])))
		}
	}
	if len(axes) != 2 || axes[0] != axes[1] {
		t.Fatalf("time axes start at columns %v, want two at the same column", axes)
	}
}
