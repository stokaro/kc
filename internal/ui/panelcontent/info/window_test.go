package info

import (
	"testing"
	"time"

	"github.com/stokaro/kc/internal/investigation"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// Info keeps 15 minutes of usage samples from this session; a window it
// cannot cover is called incomplete, never drawn as if it were.
func TestWindowCoverage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	sampled := func(from time.Time) *history {
		h := &history{}
		for at := from; !at.After(now); at = at.Add(pollInterval) {
			h.add(at, usageSample{percent: 10, base: "requests"}, usageSample{percent: 20, base: "requests"})
		}
		return h
	}
	tests := []struct {
		name string
		win  investigation.Window
		h    *history
		want string
	}{
		{name: "off", win: investigation.Window{}, h: sampled(now.Add(-5 * time.Minute)), want: ""},
		{name: "no samples", win: investigation.Window{Last: time.Hour}, want: "window last 1h: no usage history yet (incomplete)"},
		{name: "longer than the history", win: investigation.Window{Last: time.Hour}, h: sampled(now.Add(-5 * time.Minute)), want: "window last 1h: usage history from 11:55:00 only (incomplete)"},
		{name: "history older than the sparkline", win: investigation.Window{Last: time.Hour}, h: sampled(now.Add(-40 * time.Minute)), want: "window last 1h: usage history from 11:45:00 only (incomplete)"},
		{name: "covered", win: investigation.Window{Last: 2 * time.Minute}, h: sampled(now.Add(-5 * time.Minute)), want: "window last 2m: covered by the usage history"},
		{name: "ended before the history", win: investigation.Window{Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)}, h: sampled(now.Add(-5 * time.Minute)),
			want: "window 2026-11-23 09:00:00 .. 10:00:00 +00:00: no usage history for it (incomplete)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := New(panelcontent.WidgetDeps{}, Options{
				Now:    func() time.Time { return now },
				Window: func() (investigation.Window, *time.Location) { return tt.win, time.UTC },
			})
			if got := w.windowCoverage(tt.h); got != tt.want {
				t.Fatalf("coverage = %q, want %q", got, tt.want)
			}
		})
	}
}
