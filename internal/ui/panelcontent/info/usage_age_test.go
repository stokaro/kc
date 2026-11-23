package info

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
)

// TestUsageAge checks that the measurement and the read are told apart:
// a recent read of an old sample stays old.
func TestUsageAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		s    models.InfoSummary
		want string
	}{
		{name: "nothing", want: "usage not measured"},
		{
			name: "current",
			s:    models.InfoSummary{UsageAt: now.Add(-25 * time.Second), UsageWindow: 15 * time.Second, UsageReadAt: now.Add(-3 * time.Second)},
			want: "usage measured 25s ago over 15s, read 3s ago",
		},
		{
			name: "old sample read just now",
			s:    models.InfoSummary{UsageAt: now.Add(-4 * time.Minute), UsageWindow: 15 * time.Second, UsageReadAt: now.Add(-time.Second)},
			want: "usage measured 4m ago: stale, metrics-server returns old samples, read 1s ago",
		},
		{
			name: "no timestamp in the metrics",
			s:    models.InfoSummary{UsageReadAt: now.Add(-5 * time.Second)},
			want: "usage read 5s ago, measurement time not reported",
		},
	}
	for _, tc := range tests {
		if got := ansi.Strip(usageAge(tc.s, now)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestWidgetRecordsByMeasurementTime checks that reading the same sample
// again adds nothing to the history, however often kc reads it.
func TestWidgetRecordsByMeasurementTime(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	measured := h.now.Add(-time.Minute)
	h.summary = models.InfoSummary{UsageAt: measured, UsageReadAt: h.now, Bars: bars(25, 55)}
	h.w.SetFocus(ctx, true)
	_ = h.w.Init(ctx)
	for range 3 {
		h.now = h.now.Add(15 * time.Second)
		h.last().summary.UsageReadAt = h.now
		_, _ = h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen})
	}
	if got := h.w.history[h.w.historyKey()]; got == nil || len(got.cpu.samples) != 1 || !got.cpu.last.Equal(measured) || !got.mem.last.Equal(measured) {
		t.Fatalf("history = %+v, want one sample dated %v", got, measured)
	}
}
