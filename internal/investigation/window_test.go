package investigation_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/investigation"
)

var (
	berlin = time.FixedZone("CET", 3600)
	now    = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) // 13:00 in berlin
)

func TestParse(t *testing.T) {
	t.Parallel()
	at := func(h, m int) time.Time { return time.Date(2026, 11, 23, h, m, 0, 0, berlin) }
	tests := []struct {
		name, in string
		want     investigation.Window
		err      string
	}{
		{name: "empty is off", in: "  "},
		{name: "off", in: "OFF"},
		{name: "duration", in: "90m", want: investigation.Window{Last: 90 * time.Minute}},
		{name: "fixed with date", in: "2026-11-23 10:00 .. 2026-11-23 11:30", want: investigation.Window{Start: at(10, 0), End: at(11, 30)}},
		{name: "clock times are today", in: "10:00..11:30:00", want: investigation.Window{Start: at(10, 0), End: at(11, 30)}},
		{name: "open end", in: "10:00 ..", want: investigation.Window{Start: at(10, 0)}},
		{name: "rfc3339 keeps its offset", in: "2026-11-23T09:00:00Z .. 2026-11-23T10:00:00Z", want: investigation.Window{
			Start: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC),
		}},
		{name: "not a duration", in: "soon", err: "use a duration"},
		{name: "negative", in: "-5m", err: "positive"},
		{name: "too long", in: "200h", err: "longest live window is 168h"},
		{name: "end before start", in: "11:00 .. 10:00", err: "end must be after"},
		{name: "start in the future", in: "14:00 ..", err: "in the future"},
		{name: "bad time", in: "yesterday .. 10:00", err: `start: "yesterday" is not a time`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := investigation.Parse(tt.in, now, berlin)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Last != tt.want.Last || !got.Start.Equal(tt.want.Start) || !got.End.Equal(tt.want.End) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			// What the dialog shows again parses to the same window.
			again, err := investigation.Parse(got.Text(berlin), now, berlin)
			if err != nil || again.Last != got.Last || !again.Start.Equal(got.Start) || !again.End.Equal(got.End) {
				t.Fatalf("Text %q parses to %+v, %v", got.Text(berlin), again, err)
			}
		})
	}
}

func TestBoundsAndLabel(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, berlin)
	tests := []struct {
		name      string
		w         investigation.Window
		start     time.Time
		end       time.Time
		live, off bool
		label     string
	}{
		{name: "off", w: investigation.Window{}, off: true},
		{name: "last", w: investigation.Window{Last: time.Hour}, start: now.Add(-time.Hour), live: true, label: "last 1h"},
		{name: "last seconds", w: investigation.Window{Last: 30 * time.Second}, start: now.Add(-30 * time.Second), live: true, label: "last 30s"},
		{name: "fixed in the past", w: investigation.Window{Start: start, End: start.Add(90 * time.Minute)}, start: start, end: start.Add(90 * time.Minute), label: "2026-11-23 10:00:00 .. 11:30:00 +01:00"},
		{name: "open end is live", w: investigation.Window{Start: start}, start: start, live: true, label: "2026-11-23 10:00:00 .. now +01:00"},
		{name: "end in the future is live", w: investigation.Window{Start: start, End: now.Add(time.Hour)}, start: start, live: true, label: "2026-11-23 10:00:00 .. 14:00:00 +01:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, e, ok := tt.w.Bounds(now)
			if ok == tt.off || !s.Equal(tt.start) || !e.Equal(tt.end) {
				t.Fatalf("bounds = %v %v %v", s, e, ok)
			}
			if got := tt.w.Live(now); got != tt.live {
				t.Fatalf("live = %v", got)
			}
			if got := tt.w.Label(berlin); got != tt.label {
				t.Fatalf("label = %q, want %q", got, tt.label)
			}
		})
	}
}
