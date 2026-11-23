package models

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

// TestMeasuredUsageScopedPodsTiming checks that a one-namespace scope
// dates its pod usage by the scoped PodMetrics, apart from the node and
// cluster pod polls, and keeps that date when those polls are denied.
func TestMeasuredUsageScopedPodsTiming(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		globalDenied bool
	}{
		{name: "global metrics older"},
		{name: "global metrics denied", globalDenied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				read := time.Now()
				global, scoped := read.Add(-time.Minute), read.Add(-10*time.Second)
				forbidden := errors.New("forbidden")
				usage := &UsageMetrics{interval: usagePollInterval, now: time.Now, fetch: func(_ context.Context, _ Deps, kind usageKind, ns string, _ bool) *usageSnapshot {
					switch {
					case kind == usagePods && ns == "a":
						return &usageSnapshot{
							served: true, at: time.Now(),
							values:     map[string]usageValues{"a/web-1": {sampled: scoped}, "a/web-2": {sampled: scoped.Add(-5 * time.Second)}},
							containers: map[string][]containerUsage{"a/web-1": {{name: "app"}}, "a/web-2": {{name: "app"}}},
							sampled:    usageSampleTime{newest: scoped, oldest: scoped.Add(-5 * time.Second), window: 15 * time.Second},
						}
					case tc.globalDenied:
						return &usageSnapshot{served: true, at: time.Now(), err: forbidden}
					case kind == usageNodes:
						return &usageSnapshot{served: true, at: time.Now(), values: map[string]usageValues{"n1": {}}, sampled: usageSampleTime{newest: global}}
					default:
						return &usageSnapshot{served: true, at: time.Now(), containers: map[string][]containerUsage{"x/y": {{name: "app"}}}, sampled: usageSampleTime{newest: global}}
					}
				}}
				deps := Deps{Ctx: ctx, Usage: usage, ClusterKey: kccluster.Key{ContextName: "c"}}
				watcher := new(int)
				_ = measuredUsage(deps, "a", watcher, nil)
				synctest.Wait()
				m := measuredUsage(deps, "a", watcher, nil)
				// The Deployment's pods web-1 and web-2 are dated by their
				// own PodMetrics: the older of the two.
				in := loadOverviewInput(ctx, newOverviewFakeClient(workloadObjects()...), "a", nil)
				s := workloadInfoSummary(computeOverview(in, appconfig.DefaultOverview(), read, m), nil, appconfig.DefaultOverview(), wlDeployment)
				if want := scoped.Add(-5 * time.Second); !s.UsageAt.Equal(want) || !s.UsageReadAt.Equal(read) || s.UsageWindow != 15*time.Second {
					t.Fatalf("workload usage at %v read %v window %v, want %v read %v window 15s", s.UsageAt, s.UsageReadAt, s.UsageWindow, want, read)
				}
				// The capacity rows keep their combined timing.
				want := global
				if tc.globalDenied {
					want = time.Time{}
				}
				if !m.usageAt.Equal(want) {
					t.Fatalf("usageAt = %v, want %v", m.usageAt, want)
				}
				cancel()
				synctest.Wait()
			})
		})
	}
}

// TestWorkloadInfoDatedByItsPods checks that a workload's usage carries
// the time of the PodMetrics it is summed from: the oldest of its pods,
// advancing when they do, whatever the node and cluster polls say.
func TestWorkloadInfoDatedByItsPods(t *testing.T) {
	t.Parallel()
	global := overviewTestNow.Add(-time.Minute)
	read := overviewTestNow
	report := func(web1, web2 time.Time, globalAt time.Time) *overviewReport {
		m := workloadMeasured()
		m.usageAt, m.usageReadAt = globalAt, read.Add(-30*time.Second)
		m.podsTiming = podsTiming{
			sampled: map[string]time.Time{"a/web-1": web1, "a/web-2": web2, "a/db-0": web1},
			newest:  web1, window: 15 * time.Second, readAt: read,
		}
		return measuredReport(t, AllNamespaces, m, workloadObjects()...)
	}
	cfg := appconfig.DefaultOverview()
	t1 := overviewTestNow.Add(-10 * time.Second)

	s := workloadInfoSummary(report(t1, t1.Add(-5*time.Second), global), nil, cfg, wlDeployment)
	if want := t1.Add(-5 * time.Second); !s.UsageAt.Equal(want) || !s.UsageReadAt.Equal(read) || s.UsageWindow != 15*time.Second {
		t.Fatalf("usage at %v read %v window %v, want the oldest pod %v read %v", s.UsageAt, s.UsageReadAt, s.UsageWindow, want, read)
	}
	// The scoped samples advance, the global ones do not.
	t2 := t1.Add(15 * time.Second)
	if next := workloadInfoSummary(report(t2, t2, global), nil, cfg, wlDeployment); !next.UsageAt.Equal(t2) {
		t.Fatalf("usage at %v after the pods advanced, want %v", next.UsageAt, t2)
	}
	// Global metrics denied: the workload is still dated.
	if denied := workloadInfoSummary(report(t1, t1, time.Time{}), nil, cfg, wlDeployment); !denied.UsageAt.Equal(t1) {
		t.Fatalf("usage at %v with global metrics denied, want %v", denied.UsageAt, t1)
	}
	// Pods without their own timestamp fall back to the list's newest.
	m := workloadMeasured()
	m.podsTiming = podsTiming{newest: t1, readAt: read}
	if bare := workloadInfoSummary(measuredReport(t, AllNamespaces, m, workloadObjects()...), nil, cfg, wlDeployment); !bare.UsageAt.Equal(t1) {
		t.Fatalf("usage at %v without pod timestamps, want the list's %v", bare.UsageAt, t1)
	}
}
