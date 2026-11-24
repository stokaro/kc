package models

// White-box: capacityBar and workloadResourceBar are unexported, and a
// report with measured pods for each case would hide the marks under
// fixtures, as in overview_info_workload_text_test.go.

import (
	"slices"
	"testing"

	"github.com/stokaro/kc/pkg/appconfig"
)

// TestCapacityBarMarks: a bar of pod usage marks the requests and the
// limits on its scale; a bar of the requests marks only the limits.
func TestCapacityBarMarks(t *testing.T) {
	t.Parallel()
	cfg := appconfig.DefaultOverview().Normalized()
	used := capacityAmounts{podUse: 500, nodeUse: 900, req: 1200, lim: 1100, alloc: 10000, capacity: 10000, hasPodUse: true, hasNodeUse: true}
	b := capacityBar(cfg, "CPU", used, formatCores, "cores")
	want := []InfoMark{{InfoMarkRequest, 12}, {InfoMarkLimit, 11}}
	if !slices.Equal(b.Marks, want) {
		t.Errorf("marks with pod usage = %v, want %v", b.Marks, want)
	}
	reqs := used
	reqs.hasPodUse = false
	b = capacityBar(cfg, "CPU", reqs, formatCores, "cores")
	want = []InfoMark{{InfoMarkLimit, 11}}
	if b.Percent != 12 || !slices.Equal(b.Marks, want) {
		t.Errorf("requests bar at %v with marks %v, want 12 with %v", b.Percent, b.Marks, want)
	}
	if b := capacityBar(cfg, "CPU", capacityAmounts{}, formatCores, "cores"); b.Marks != nil {
		t.Errorf("a bar of nothing has marks %v", b.Marks)
	}
}

// TestWorkloadBarMarks: the marks follow the base of the bar.
func TestWorkloadBarMarks(t *testing.T) {
	t.Parallel()
	cfg := appconfig.DefaultOverview()
	for _, tc := range []struct {
		name      string
		hasUse    bool
		req, lim  int64
		unlimited bool
		want      []InfoMark
	}{
		{name: "usage of limits", hasUse: true, req: 250, lim: 1000, want: []InfoMark{{InfoMarkRequest, 25}, {InfoMarkLimit, 100}}},
		{name: "usage of limits without requests", hasUse: true, lim: 1000, want: []InfoMark{{InfoMarkLimit, 100}}},
		{name: "usage of requests", hasUse: true, req: 250, unlimited: true, want: []InfoMark{{InfoMarkRequest, 100}}},
		{name: "requests of limits", req: 250, lim: 1000, want: []InfoMark{{InfoMarkLimit, 100}}},
		{name: "no base", hasUse: true, unlimited: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := workloadResourceBar(cfg, "CPU", 1, tc.hasUse, 400, tc.req, tc.lim, tc.unlimited, cfg.CPULimitWarnPercent, formatCores, "")
			if !slices.Equal(b.Marks, tc.want) {
				t.Errorf("marks = %v, want %v", b.Marks, tc.want)
			}
		})
	}
}
