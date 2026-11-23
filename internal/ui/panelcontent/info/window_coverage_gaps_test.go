package info

import (
	"testing"
	"time"

	"github.com/stokaro/kc/internal/investigation"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// TestWindowCoverageGaps checks that "covered" needs samples across the
// whole window for both metrics: a beginning, no gap longer than gapAfter
// and a recent end.
func TestWindowCoverageGaps(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	// polled is a series sampled every pollInterval in [from, to].
	polled := func(from, to time.Time) series {
		var s series
		for at := from; !at.After(to); at = at.Add(pollInterval) {
			s.samples = append(s.samples, sample{at: at, v: 10})
		}
		return s
	}
	at := func(hms string) time.Time {
		t, err := time.Parse("15:04:05", hms)
		if err != nil {
			panic(err)
		}
		return time.Date(2026, 11, 23, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	}
	points := func(ts ...string) series {
		var s series
		for _, hms := range ts {
			s.samples = append(s.samples, sample{at: at(hms), v: 10})
		}
		return s
	}
	both := func(s series) *history { return &history{cpu: s, mem: s} }
	tests := []struct {
		name string
		last time.Duration
		h    *history
		want string
	}{{
		name: "no sample inside the window",
		last: 2 * time.Minute, h: both(points("11:48:00", "11:49:00")),
		want: "window last 2m: no session usage history for it (incomplete)",
	}, {
		name: "a long gap before the only recent sample",
		last: 10 * time.Minute, h: both(points("11:48:00", "11:59:50")),
		want: "window last 10m: session usage history from 11:59:50 only (incomplete)",
	}, {
		name: "an internal gap",
		last: 10 * time.Minute, h: both(series{samples: append(polled(at("11:49:00"), at("11:53:00")).samples, polled(at("11:56:00"), now).samples...)}),
		want: "window last 10m: session usage history 1 gap (incomplete)",
	}, {
		name: "a stale end",
		last: 5 * time.Minute, h: both(polled(at("11:50:00"), at("11:58:00"))),
		want: "window last 5m: session usage history stale since 11:58:00 (incomplete)",
	}, {
		name: "one missed poll is not a gap",
		last: 5 * time.Minute, h: both(points("11:54:50", "11:55:20", "11:55:50", "11:56:20", "11:56:50", "11:57:20", "11:57:50", "11:58:20", "11:58:50", "11:59:20", "11:59:50")),
		want: "window last 5m: covered by the session usage history",
	}, {
		name: "memory restarted after a base change",
		last: 5 * time.Minute, h: &history{cpu: polled(at("11:50:00"), now), mem: polled(at("11:58:00"), now)},
		want: "window last 5m: session usage history: CPU covered, memory from 11:58:00 only (incomplete)",
	}, {
		name: "the longer CPU history does not hide missing memory",
		last: 5 * time.Minute, h: &history{cpu: polled(at("11:50:00"), now)},
		want: "window last 5m: session usage history: CPU covered, memory no samples (incomplete)",
	}, {
		name: "continuous",
		last: 5 * time.Minute, h: both(polled(at("11:54:30"), now)),
		want: "window last 5m: covered by the session usage history",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := New(panelcontent.WidgetDeps{}, Options{
				Now:    func() time.Time { return now },
				Window: func() (investigation.Window, *time.Location) { return investigation.Window{Last: tt.last}, time.UTC },
			})
			if got := w.windowCoverage(tt.h); got != tt.want {
				t.Fatalf("coverage = %q, want %q", got, tt.want)
			}
		})
	}
}
