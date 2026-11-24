package models

// White-box: workloadResourceBar is unexported, and building a report with
// measured pods for each wording would hide the text under fixtures.

import (
	"testing"

	"github.com/stokaro/kc/pkg/appconfig"
)

// TestWorkloadResourceBarNamesThePods: a workload bar sums the use of its
// pods, so its text says how many it sums; read alone, "use 11Mi" was taken
// for one pod's use and did not match the 5Mi rows below it.
func TestWorkloadResourceBarNamesThePods(t *testing.T) {
	t.Parallel()
	cfg := appconfig.DefaultOverview()
	const mi = 1 << 20
	for _, tc := range []struct {
		name      string
		pods      int64
		req, lim  int64
		unlimited bool
		want      string
	}{
		{name: "requests", pods: 2, req: 32 * mi, unlimited: true, want: "2 pods use 11Mi of req 32Mi, no limit"},
		{name: "limits", pods: 2, req: 32 * mi, lim: 64 * mi, want: "2 pods use 11Mi of lim 64Mi, req 32Mi"},
		{name: "one pod", pods: 1, req: 32 * mi, unlimited: true, want: "1 pod uses 11Mi of req 32Mi, no limit"},
		{name: "neither", pods: 3, unlimited: true, want: "3 pods use 11Mi, no requests or limits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Two pods of 5.6Mi each: the rows say 5Mi, the exact sum 11Mi.
			use := int64(2 * 56 * mi / 10)
			b := workloadResourceBar(cfg, "Memory", tc.pods, true, use, tc.req, tc.lim, tc.unlimited, cfg.MemoryLimitWarnPercent, formatMemory, "")
			if b.Detail != tc.want {
				t.Fatalf("Detail = %q, want %q", b.Detail, tc.want)
			}
		})
	}
}
