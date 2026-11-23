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

	"github.com/stokaro/kc/internal/investigation"
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
	win      investigation.Window
}

func (f *fakes) sources(prom bool) dashboard.Sources {
	src := dashboard.Sources{
		Context:   "kind-test",
		Scope:     "shop",
		Now:       func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		Loc:       time.UTC,
		Tick:      func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil },
		Window:    func() investigation.Window { f.mu.Lock(); defer f.mu.Unlock(); return f.win },
		SetWindow: func(w investigation.Window) { f.mu.Lock(); defer f.mu.Unlock(); f.win = w },
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
		Rows: []models.UsageRow{
			podRow("web-1", 0.3, 200<<20, ""),
			podRow("web-2", 0.1, 100<<20, "CrashLoopBackOff"),
		},
	}
}

// podRow is a row of a pod of the Deployment web, using half its
// requests and a quarter of its limits per unit of usage.
func podRow(name string, cpu, mem float64, problem string) models.UsageRow {
	owner := &models.InfoWorkload{GVR: deployGVR, Namespace: "shop", Name: "web"}
	r := models.UsageRow{
		Owner: owner, Namespace: "shop", Name: name, Pods: 1, Ready: 1, Measured: 1, Problem: problem,
		CPU:    models.ResourceAmount{Used: cpu, UsedKnown: true, Requests: 0.5, Limits: 1},
		Memory: models.ResourceAmount{Used: mem, UsedKnown: true, Requests: 256 << 20, Limits: 512 << 20},
	}
	if problem != "" {
		r.Ready, r.Restarts = 0, 7
	}
	return r
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
// named, never drawn as zero. The legend says in words what each line is.
func TestMetricsAPIOnly(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, false, web, 150, 42)
	ticks(t, f, m, 4, 15*time.Second)
	lines := screen(t, m, 150, 42)
	requireText(t, lines, "kind-test › namespace shop › Deployment web", "LIVE · last 15 minutes",
		"Metrics API, 5 samples · 2 of 2 pods measured · no Prometheus (C-x m)",
		"Now:  █ usage 0.4  ┄ request 1 (40% used)  ┄ limit 2",
		"Pods of Deployment web", "web-1", ".. up to namespace shop")
	if f.focuses[0].Workload == nil || f.focuses[0].Workload.Name != "web" || f.focuses[0].Workload.GVR != deployGVR {
		t.Errorf("focus = %+v, want the Deployment with its GVR", f.focuses[0])
	}
}

// TestSessionChartFitsItsData: while the session is shorter than the
// range, a live chart starts at its first sample and its title says so.
func TestSessionChartFitsItsData(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, false, web, 120, 30)
	ticks(t, f, m, 8, 15*time.Second)
	lines := screen(t, m, 120, 30)
	requireText(t, lines, "CPU, cores · 2m of 15m collected so far", "└10:00:00")
	if contains(lines, "no data") {
		t.Errorf("a fitted chart shows no data:\n%s", strings.Join(lines, "\n"))
	}
}

// TestTargetListShowsUsageAgainstRequestsAndHealth: each row has its
// usage, the share of its request, and what is not well.
func TestTargetListShowsUsageAgainstRequestsAndHealth(t *testing.T) {
	t.Parallel()
	a := measured()
	a.Rows[0].Memory.Used = 480 << 20 // near the 512Mi limit
	f := &fakes{now: t0, amounts: a}
	m := open(t, f, false, web, 150, 42)
	lines := screen(t, m, 150, 42)
	requireText(t, lines, "Name", "Ready", "CPU", "of req", "Memory", "Restarts",
		"web-1", "60%", "480Mi", "188%", "web-2", "⚠ CrashLoopBackOff · 7 restarts", "2 pods · 1 ready")
}

// TestNoMeasurementIsNotZero: a pod set without PodMetrics says so and
// draws no line at all.
func TestNoMeasurementIsNotZero(t *testing.T) {
	t.Parallel()
	a := measured()
	a.CPU.Used, a.CPU.UsedKnown, a.Memory.Used, a.Memory.UsedKnown, a.Top, a.Rows = 0, false, 0, false, nil, nil
	a.Note = "metrics unavailable: metrics.k8s.io is not served"
	f := &fakes{now: t0, amounts: a}
	m := open(t, f, false, web, 120, 30)
	ticks(t, f, m, 3, 15*time.Second)
	lines := screen(t, m, 120, 30)
	requireText(t, lines, "usage metrics unavailable: metrics.k8s.io is not served", "no samples yet", "no data")
	for _, l := range lines[1:20] {
		if strings.ContainsAny(l, "⡀⣀⣿▁█") && !strings.Contains(l, "usage") {
			t.Fatalf("a line is drawn without measurements: %q", l)
		}
	}
}

func TestLayoutsAtEverySize(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 30}, {150, 42}, {240, 60}, {100, 17}, {60, 30}, {40, 10}} {
		f := &fakes{now: t0, amounts: measured()}
		m := open(t, f, true, web, size[0], size[1])
		lines := screen(t, m, size[0], size[1])
		requireText(t, lines[:1], "Dashboard", "LIVE")
		titles := []string{"CPU, cores", "Memory", "Events", "Pods of Deployment web"}
		shown := 0
		for _, title := range titles {
			if contains(lines, title) {
				shown++
			}
		}
		paged := shown < len(titles)
		wantPaged := size[1] < 24 || size[0] < 60
		if paged != wantPaged {
			t.Errorf("%v: paged %v, want %v:\n%s", size, paged, wantPaged, strings.Join(lines, "\n"))
		}
		if size[0] >= 120 && !strings.Contains(lines[1], "Pods of") {
			t.Errorf("%v: the target list is not on the left: %q", size, lines[1])
		}
		// Every widget is reachable with Tab, and its title shows focused.
		seen := make(map[string]bool)
		for range 4 {
			for _, title := range titles {
				if contains(screen(t, m, size[0], size[1]), title) {
					seen[title] = true
				}
			}
			key(t, m, "tab")
		}
		if len(seen) != 4 {
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
	m := open(t, f, true, web, 150, 42)
	if len(f.reads) != 1 {
		t.Fatalf("%d reads, want 1", len(f.reads))
	}
	s := f.reads[0]
	if s.Kind != promhistory.ScopeWorkload || s.Workload.Kind != "Deployment" || s.UID != "dep-uid" || !s.Since.Equal(a.Created) {
		t.Errorf("scope = %+v, want the Deployment with its UID and creation", s)
	}
	if r := f.ranges[0]; r.End.After(t0) || t0.Sub(r.Start) < time.Hour || r.Steps() > 2*150 {
		t.Errorf("range %v..%v step %v: want the last hour at the chart's resolution", r.Start, r.End, r.Step)
	}
	lines := screen(t, m, 150, 42)
	requireText(t, lines, "LIVE · last 1 hour", "5m avg", "partial 1/", "created 09:4")
}

func TestPrometheusFailureFallsBackToTheSession(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	f.usage = func(promhistory.Scope, promhistory.Range) (promhistory.Usage, error) {
		return promhistory.Usage{}, errors.New("prometheus monitoring/prometheus: 403 (needs get on services/proxy)\ndetails")
	}
	m := open(t, f, true, web, 240, 42)
	ticks(t, f, m, 2, 10*time.Second)
	lines := screen(t, m, 240, 42)
	requireText(t, lines, "3 samples", "Metrics API: Prometheus failed: 403", "LIVE · last 15 minutes")
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
	requireText(t, screen(t, m, 80, 24), "PAUSED at 10:01:05 · 15m")
}

// TestPauseCursorAndEsc: the cursor pauses and shows values; Enter on a
// chart expands it; Esc collapses, then removes the cursor, then closes.
func TestPauseCursorAndEsc(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 120, 30)
	key(t, m, "left")
	lines := screen(t, m, 120, 30)
	requireText(t, lines, "PAUSED at 10:00:00", "█ usage 1 (highest)", "usage 1Gi (highest)")
	// Paused, the legend keeps the values of the pause.
	f.amounts.CPU.Used = 9
	ticks(t, f, m, 2, 15*time.Second)
	if contains(screen(t, m, 120, 30), "usage 9") {
		t.Error("a paused dashboard shows new values")
	}
	key(t, m, "tab")
	key(t, m, "enter")
	lines = screen(t, m, 120, 30)
	if contains(lines, "Memory") || !contains(lines, "CPU, cores") {
		t.Errorf("Enter did not expand the CPU chart:\n%s", strings.Join(lines, "\n"))
	}
	if act := key(t, m, "esc"); act.Close {
		t.Fatal("first Esc closed the dashboard")
	}
	if !contains(screen(t, m, 120, 30), "Memory") {
		t.Error("first Esc did not collapse the chart")
	}
	if act := key(t, m, "esc"); act.Close {
		t.Fatal("second Esc closed the dashboard")
	}
	lines = screen(t, m, 120, 30)
	if contains(lines, "(highest)") || !contains(lines, "PAUSED") {
		t.Error("second Esc must remove the cursor and stay paused")
	}
	if act := key(t, m, "esc"); !act.Close {
		t.Error("third Esc must close")
	}
}

// TestDrilldownUpAndOpen: Enter on a row opens its dashboard, Backspace
// goes back up, and up again past the target the dashboard opened for.
func TestDrilldownUpAndOpen(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 150, 42)
	key(t, m, "tab")
	if act := key(t, m, "o"); act.Open == nil || act.Open.GVR != deployGVR || act.Open.Name != "web" || act.Open.UID != types.UID("dep-uid") {
		t.Errorf("o on the workload = %+v", act.Open)
	}
	key(t, m, "shift+tab")
	key(t, m, "down")
	if act := key(t, m, "o"); act.Open == nil || act.Open.GVR.Resource != "pods" || act.Open.Name != "web-2" {
		t.Errorf("o on a pod row = %+v", act.Open)
	}
	reads := len(f.reads)
	key(t, m, "enter")
	requireText(t, screen(t, m, 150, 42), "namespace shop › Deployment web › pod web-2", ".. up to Deployment shop/web")
	last := f.focuses[len(f.focuses)-1]
	if last.Pod != "web-2" || last.PodNamespace != "shop" {
		t.Errorf("drilldown focus = %+v", last)
	}
	if len(f.reads) != reads+1 || f.reads[len(f.reads)-1].Kind != promhistory.ScopePod {
		t.Errorf("drilldown reads = %+v", f.reads[reads:])
	}
	if _, act := m.Key("backspace"); act.Close {
		t.Fatal("Backspace in a drilldown closed the dashboard")
	}
	lines := screen(t, m, 150, 42)
	requireText(t, lines, "Pods of Deployment web")
	if contains(lines[:1], "pod web-2") {
		t.Error("Backspace did not leave the pod")
	}
	// Up from the target the dashboard opened for: the namespace scope.
	// A terminal that sends ^H for Backspace goes up too.
	m.Key("ctrl+h")
	requireText(t, screen(t, m, 150, 42), "Workloads in shop")
	if last := f.focuses[len(f.focuses)-1]; last != (models.UsageFocus{}) {
		t.Errorf("up from the workload read %+v, want the scope", last)
	}
	if _, act := m.Key("f10"); !act.Close {
		t.Error("F10 must close")
	}
}

// TestMenusAndFunctionKeys: the F-key bar follows the state and the Range
// menu sets the range.
func TestMenusAndFunctionKeys(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, false, web, 150, 42)
	keys := m.FunctionKeys()
	if len(keys) != 10 || keys[0].Label != "Help" || keys[2].Label != "Pause" || keys[9].Label != "Close" {
		t.Fatalf("F-keys = %+v", keys)
	}
	m.Key("f3")
	if keys := m.FunctionKeys(); keys[2].Label != "Live" {
		t.Errorf("F3 paused, yet F3 says %q", keys[2].Label)
	}
	var ran []string
	menus := m.Menus(func(k string) tea.Cmd { ran = append(ran, k); return nil })
	if menus[dashboard.RangeMenu].Title != "&Range" {
		t.Fatalf("menu %d is %q, want Range", dashboard.RangeMenu, menus[dashboard.RangeMenu].Title)
	}
	for _, it := range menus[dashboard.RangeMenu].Items {
		if it.Text() == "Last 1 hour" {
			it.Run()
		}
	}
	if len(ran) != 1 {
		t.Fatalf("ran %v", ran)
	}
	m.Key(ran[0])
	requireText(t, screen(t, m, 150, 42)[:1], "· 1h")
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
	m := open(t, f, false, web, 150, 42)
	ticks(t, f, m, 2, 15*time.Second)
	key(t, m, "enter")
	m.Stop()
	// Same context: the drilldown and the session history come back.
	m = m.Resume(f.sources(false), web)
	run(t, m, m.Start())
	requireText(t, screen(t, m, 150, 42), "› pod web-1")
	key(t, m, "esc")
	requireText(t, screen(t, m, 150, 42), "3 samples")
	// Another context starts at the target again.
	m.Stop()
	other := f.sources(false)
	other.Context, other.Scope = "kind-other", models.AllNamespaces
	m = m.Resume(other, dashboard.Target{Kind: dashboard.KindScope, Namespace: models.AllNamespaces})
	run(t, m, m.Start())
	lines := screen(t, m, 150, 42)
	requireText(t, lines, "kind-other › all namespaces")
	if contains(lines, "pod web-1") {
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
	m := open(t, f, true, web, 150, 42)
	screen(t, m, 150, 42)
	// The CPU chart's plot ends at the right border, three rows down.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 140, Y: 5})
	lines := screen(t, m, 150, 42)
	requireText(t, lines, "PAUSED", "(highest)")
	// The wheel zooms.
	cmd, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 140, Y: 5})
	run(t, m, cmd)
	requireText(t, screen(t, m, 150, 42), "15m")
	// Clicking a row selects it; clicking it again opens its dashboard.
	var row int
	for i, l := range screen(t, m, 150, 42) {
		if strings.Contains(l, "web-2") {
			row = i
		}
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: row})
	cmd, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: row})
	run(t, m, cmd)
	requireText(t, screen(t, m, 150, 42), "› pod web-2")
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
	if strings.ContainsAny(strings.Join(lines, ""), "⣀⣤⣶⣿") || m.FunctionKeys()[5].Label != "Braille" {
		t.Error("block mode still draws braille")
	}
}

// TestChartsShareTheCellGrid: the CPU and memory charts have labels of
// different widths, yet their plots end at the same column and are as
// wide, so one cursor cell is one time in both.
func TestChartsShareTheCellGrid(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, true, web, 150, 42)
	var axes []int
	for _, l := range screen(t, m, 150, 42) {
		if i := strings.IndexRune(l, '└'); i >= 0 && strings.Contains(l, "10:00") {
			axes = append(axes, len([]rune(l[:i])))
		}
	}
	if len(axes) != 2 || axes[0] != axes[1] {
		t.Fatalf("time axes start at columns %v, want two at the same column", axes)
	}
}

// TestLaneUnderSideBySideCharts: with the charts side by side, the Events
// lane is on the time axis of the left chart, so a mark of 09:58 is under
// the end of the CPU chart, not under the memory chart.
func TestLaneUnderSideBySideCharts(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: withEvents()}
	m := open(t, f, true, web, 100, 30)
	lines := screen(t, m, 100, 30)
	if !strings.HasPrefix(lines[1], "┌─ CPU") {
		t.Fatalf("want the CPU chart on the left: %q", lines[1])
	}
	cpuEnd := strings.Index(lines[1], "┐┌")
	if cpuEnd < 0 {
		t.Fatalf("no two charts side by side: %q", lines[1])
	}
	cpuEnd = len([]rune(lines[1][:cpuEnd]))
	marks, _ := lane(t, lines)
	before, _, ok := strings.CutLast(marks, "▲")
	if !ok {
		t.Fatalf("lane %q has no warning", marks)
	}
	if col := len([]rune(before)); col > cpuEnd || col < cpuEnd-5 {
		t.Errorf("the 09:58 warning is at column %d, want just left of the CPU chart's end at %d:\n%s", col, cpuEnd, strings.Join(lines, "\n"))
	}
}
