package dashboard_test

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/investigation"
	"github.com/stokaro/kc/internal/ui/dashboard"
)

// within reports whether r covers [from, to] to within one step.
func rangeMatches(t *testing.T, f *fakes, from, to time.Time) {
	t.Helper()
	if len(f.ranges) == 0 {
		t.Fatal("no Prometheus read")
	}
	r := f.ranges[len(f.ranges)-1]
	if r.Start.After(from) || from.Sub(r.Start) > r.Step || r.End.After(to) || to.Sub(r.End) > r.Step {
		t.Fatalf("read %v..%v step %v, want %v..%v", r.Start, r.End, r.Step, from, to)
	}
}

// TestFixedWindowSetsTheRange: a fixed investigation window is the
// charts' range, read once and labeled with its zone; changing the
// window cancels the read of the old one and drops its late result.
func TestFixedWindowSetsTheRange(t *testing.T) {
	t.Parallel()
	start, end := t0.Add(-time.Hour), t0.Add(-30*time.Minute)
	f := &fakes{now: t0, amounts: measured(), win: investigation.Window{Start: start, End: end}}
	m := open(t, f, true, web, 100, 30)
	rangeMatches(t, f, start, end)
	requireText(t, screen(t, m, 100, 30), "WINDOW 2026-11-23 09:00:00 .. 09:30:00 +00:00", "complete")
	ticks(t, f, m, 3, time.Minute)
	if len(f.reads) != 1 {
		t.Errorf("%d reads of a window that ended, want 1", len(f.reads))
	}
	// Live values stay labeled as now, not as the window.
	requireText(t, screen(t, m, 100, 30), "At pause:  █ usage 0.4")

	// A read in flight is canceled when the window changes, and its late
	// result does not replace the read of the new window.
	f.win = investigation.Window{Last: 2 * time.Hour}
	f.advance(time.Minute)
	stale, _ := m.Update(dashboard.TickMsgForTest(m)) // starts the read of the 2h window
	before, reads := f.canceled, len(f.reads)
	f.win = investigation.Window{Last: 3 * time.Hour}
	f.advance(time.Second)
	next, _ := m.Update(dashboard.TickMsgForTest(m))
	if f.canceled != before+1 || len(f.reads) != reads+1 {
		t.Errorf("canceled %d -> %d, reads %d -> %d; want the old read canceled and one new read", before, f.canceled, reads, len(f.reads))
	}
	run(t, m, next)
	run(t, m, stale)
	rangeMatches(t, f, f.now.Add(-3*time.Hour), f.now)
	lines := screen(t, m, 100, 30)
	requireText(t, lines, "LIVE window 3h")
	if contains(lines, "reading") {
		t.Errorf("the late result of the old window left the view waiting:\n%s", lines[2])
	}
}

// TestZoomDetachesAndWAppliesTheRange: zooming leaves the window and the
// title says so; w makes the shown range the window again, live while the
// view is live and fixed while it is paused.
func TestZoomDetachesAndWAppliesTheRange(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured(), win: investigation.Window{Last: 2 * time.Hour}}
	m := open(t, f, true, web, 100, 30)
	requireText(t, screen(t, m, 100, 30), "LIVE window 2h")
	key(t, m, "+")
	requireText(t, screen(t, m, 100, 30), "LIVE 1h ≠ window")
	rangeMatches(t, f, t0.Add(-time.Hour), t0)
	key(t, m, "w")
	if f.win != (investigation.Window{Last: time.Hour}) {
		t.Errorf("w set %+v, want the live 1h", f.win)
	}
	requireText(t, screen(t, m, 100, 30), "LIVE window 1h")

	// The cursor pauses a live window, which then is another range.
	key(t, m, "left")
	requireText(t, screen(t, m, 100, 30), "PAUSED at 10:00:00 · 1h ≠ window")
	key(t, m, "w")
	if !f.win.Start.Equal(t0.Add(-time.Hour)) || !f.win.End.Equal(t0) || f.win.Last != 0 {
		t.Errorf("w while paused set %+v, want 09:00..10:00", f.win)
	}
	requireText(t, screen(t, m, 100, 30), "WINDOW 2026-11-23 09:00:00 .. 10:00:00 +00:00")

	// The cursor on a fixed window keeps it; Space goes live and leaves it.
	key(t, m, "left")
	requireText(t, screen(t, m, 100, 30), "WINDOW 2026-11-23")
	key(t, m, "space")
	requireText(t, screen(t, m, 100, 30), "LIVE 1h ≠ window")

	// Turning the window off keeps the range.
	f.win = investigation.Window{}
	ticks(t, f, m, 1, time.Second)
	if lines := screen(t, m, 100, 30); contains(lines, "≠ window") || !contains(lines, "LIVE 1h") {
		t.Errorf("window off: %q", lines[0])
	}
}

// TestWindowZoneAndDrilldown: the label carries the zone offset of the
// location; a drilldown keeps the window.
func TestWindowZoneAndDrilldown(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("IST", 5*3600+1800)
	f := &fakes{now: t0, amounts: measured(), win: investigation.Window{Start: t0.Add(-time.Hour), End: t0.Add(-30 * time.Minute)}}
	src := f.sources(true)
	src.Loc = zone
	m := dashboard.New(src, web)
	run(t, m, func() tea.Msg { return tea.WindowSizeMsg{Width: 100, Height: 30} })
	run(t, m, m.Start())
	requireText(t, screen(t, m, 100, 30), "WINDOW 2026-11-23 14:30:00 .. 15:00:00 +05:30")
	key(t, m, "enter")
	if f.focuses[len(f.focuses)-1].Pod != "web-1" {
		t.Fatalf("drilldown focus = %+v", f.focuses[len(f.focuses)-1])
	}
	requireText(t, screen(t, m, 100, 30), "Pod web-1", "WINDOW 2026-11-23 14:30:00 .. 15:00:00 +05:30")
}

// TestSessionOutsideRetention: a fixed window before the session history
// says nothing was recorded there, and does not move to now.
func TestSessionOutsideRetention(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: measured()}
	m := open(t, f, false, web, 150, 42)
	ticks(t, f, m, 4, 15*time.Second)
	f.win = investigation.Window{Start: t0.Add(-3 * time.Hour), End: t0.Add(-2 * time.Hour)}
	ticks(t, f, m, 1, time.Second)
	lines := screen(t, m, 150, 42)
	requireText(t, lines, "nothing recorded in this range (kept 10:00:00–10:01:01)", "WINDOW 2026-11-23 07:00:00 .. 08:00:00")
	if contains(lines, "samples") {
		t.Errorf("the window shows samples from outside it:\n%s", lines[0])
	}
}
