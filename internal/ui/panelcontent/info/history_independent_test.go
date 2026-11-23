package info

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// TestHistoryRecordsEachResource checks that CPU and memory are recorded
// apart: a resource without a percentage leaves a gap in its own series
// only, never a zero, and the other one keeps its samples.
func TestHistoryRecordsEachResource(t *testing.T) {
	t.Parallel()
	usage := func(label, base string, pct float64) models.InfoBar {
		return models.InfoBar{Label: label, Known: true, Usage: true, Base: base, Percent: pct}
	}
	raw := func(label string) models.InfoBar {
		// A workload without requests or limits: raw usage, no percentage.
		return models.InfoBar{Label: label, Detail: "use 120m, no requests or limits"}
	}
	type step struct {
		cpu, mem models.InfoBar
	}
	tests := []struct {
		name     string
		steps    []step
		cpu, mem []float64 // the recorded values of each series
	}{{
		name:  "memory only",
		steps: []step{{raw("CPU"), usage("Memory", "limits", 50)}, {raw("CPU"), usage("Memory", "limits", 60)}},
		mem:   []float64{50, 60},
	}, {
		name:  "CPU only",
		steps: []step{{usage("CPU", "limits", 20), raw("Memory")}, {usage("CPU", "limits", 30), raw("Memory")}},
		cpu:   []float64{20, 30},
	}, {
		name:  "both",
		steps: []step{{usage("CPU", "limits", 20), usage("Memory", "limits", 50)}},
		cpu:   []float64{20}, mem: []float64{50},
	}, {
		name:  "neither",
		steps: []step{{raw("CPU"), raw("Memory")}},
	}, {
		name: "CPU recovers without touching memory",
		steps: []step{
			{raw("CPU"), usage("Memory", "limits", 50)},
			{usage("CPU", "limits", 20), usage("Memory", "limits", 55)},
		},
		cpu: []float64{20}, mem: []float64{50, 55},
	}, {
		name: "a base change resets only that resource",
		steps: []step{
			{usage("CPU", "limits", 20), usage("Memory", "limits", 50)},
			{usage("CPU", "requests", 40), usage("Memory", "limits", 55)},
		},
		cpu: []float64{40}, mem: []float64{50, 55},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
			var summary models.InfoSummary
			target := Target{Key: "ctx|ns", Label: "ctx", Open: func() Summarizer { return &liveSource{s: &summary} }}
			w := New(panelcontent.WidgetDeps{}, Options{
				Resolve: func(context.Context) Target { return target },
				Now:     func() time.Time { return now },
			})
			w.SetFocus(t.Context(), true)
			for _, st := range tt.steps {
				now = now.Add(pollInterval)
				summary = models.InfoSummary{UsageAt: now, Bars: []models.InfoBar{st.cpu, st.mem}}
				_ = w.OnSelectionChanged(t.Context(), panelcontent.Selection{})
			}
			h := w.history[w.historyKey()]
			if tt.cpu == nil && tt.mem == nil {
				if h != nil {
					t.Fatalf("history = %+v, want none", h)
				}
				return
			}
			if h == nil {
				t.Fatal("nothing recorded")
			}
			values := func(s series) []float64 {
				var out []float64
				for _, sm := range s.samples {
					out = append(out, sm.v)
				}
				return out
			}
			if got := values(h.cpu); !slices.Equal(got, tt.cpu) {
				t.Errorf("CPU = %v, want %v", got, tt.cpu)
			}
			if got := values(h.mem); !slices.Equal(got, tt.mem) {
				t.Errorf("memory = %v, want %v", got, tt.mem)
			}
		})
	}
}

// TestHistoryMemoryOnlyView checks what Info shows for a workload with a
// memory limit and no CPU requests or limits: the CPU bar keeps its raw
// usage text and has no history line, memory has its own.
func TestHistoryMemoryOnlyView(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.summary = models.InfoSummary{UsageAt: h.now, Bars: []models.InfoBar{
		{Label: "CPU", Detail: "use 120m, no requests or limits"},
		{Label: "Memory", Known: true, Usage: true, Base: "limits", Percent: 50, Detail: "pods 512Mi/1.0Gi limits, 50%"},
	}}
	h.w.SetFocus(ctx, true)
	_ = h.w.Init(ctx)
	h.now = h.now.Add(pollInterval)
	h.last().summary.UsageAt = h.now
	_, _ = h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen})
	view := ansi.Strip(strings.Join(h.w.lines(80, 40), "\n"))
	if !strings.Contains(view, "use 120m, no requests or limits") {
		t.Fatalf("the CPU raw usage text is gone:\n%s", view)
	}
	if n := strings.Count(view, "history, "); n != 1 || !strings.Contains(view, "history, % of limits · 2 samples over 15s") {
		t.Fatalf("want one memory history line, got %d:\n%s", n, view)
	}
}
