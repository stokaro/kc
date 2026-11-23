package models

// White-box: measurement times, staleness and coverage live in unexported
// snapshot and capacity types that the overview and Info read.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

func metricsItem(name, ts, window string) unstructured.Unstructured {
	obj := map[string]any{"metadata": map[string]any{"name": name}}
	if ts != "" {
		obj["timestamp"] = ts
	}
	if window != "" {
		obj["window"] = window
	}
	return unstructured.Unstructured{Object: obj}
}

func TestSampleTimes(t *testing.T) {
	t.Parallel()
	st := sampleTimes([]unstructured.Unstructured{
		metricsItem("a", "2026-11-22T12:00:10Z", "15s"),
		metricsItem("b", "2026-11-22T11:58:00Z", "20s"),
		metricsItem("c", "not a time", "15s"),
		metricsItem("d", "", ""),
		metricsItem("e", "2026-11-22T12:00:30Z", "10.5s"),
	})
	if want := time.Date(2026, 11, 22, 12, 0, 30, 0, time.UTC); !st.newest.Equal(want) {
		t.Errorf("newest = %v, want %v", st.newest, want)
	}
	if want := time.Date(2026, 11, 22, 11, 58, 0, 0, time.UTC); !st.oldest.Equal(want) {
		t.Errorf("oldest = %v, want %v", st.oldest, want)
	}
	if st.window != 10500*time.Millisecond {
		t.Errorf("window = %v, want the newest sample's 10.5s", st.window)
	}
	if empty := sampleTimes(nil); !empty.newest.IsZero() || !empty.oldest.IsZero() {
		t.Errorf("no items = %+v", empty)
	}
}

func TestUsageReasonStaleAndRecovered(t *testing.T) {
	t.Parallel()
	read := overviewTestNow
	stale := &usageSnapshot{served: true, at: read, sampled: usageSampleTime{newest: read.Add(-4 * time.Minute)}}
	if got := usageReason(stale, true); !strings.Contains(got, "newest sample was 4m") || !strings.Contains(got, "old when read") {
		t.Fatalf("stale reason = %q", got)
	}
	// A successful read of old samples is still old; a new sample recovers.
	recovered := &usageSnapshot{served: true, at: read, sampled: usageSampleTime{newest: read.Add(-20 * time.Second)}}
	if got := usageReason(recovered, true); got != "" {
		t.Fatalf("recovered reason = %q", got)
	}
	// Forbidden stays forbidden, whatever the samples.
	if got := usageReason(&usageSnapshot{served: true, at: read, err: errors.New("forbidden")}, true); got != "metrics.k8s.io: forbidden" {
		t.Fatalf("forbidden reason = %q", got)
	}
	if timed(&usageSnapshot{served: true, err: errors.New("x"), sampled: stale.sampled, at: read}, "metrics.k8s.io: x") {
		t.Fatal("a failed read carries no measurement time")
	}
}

// TestMeasuredUsageDatesByMeasurement checks that the overview dates usage
// by metrics-server's timestamps, not by the read, and that a stale pod
// list leaves pod usage unknown while its age stays visible.
func TestMeasuredUsageDatesByMeasurement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		start := time.Now()
		usage := &UsageMetrics{interval: usagePollInterval, now: time.Now, fetch: func(_ context.Context, _ Deps, kind usageKind, _ string, _ bool) *usageSnapshot {
			if kind == usageNodes {
				return &usageSnapshot{served: true, values: map[string]usageValues{"n1": {cpu: 1}}, sampled: usageSampleTime{newest: start.Add(-30 * time.Second), window: 15 * time.Second}}
			}
			return &usageSnapshot{served: true, containers: map[string][]containerUsage{"a/web": {{name: "app"}}}, sampled: usageSampleTime{newest: start.Add(-5 * time.Minute)}}
		}}
		deps := Deps{Ctx: ctx, Usage: usage, ClusterKey: kccluster.Key{ContextName: "c"}}
		watcher := new(int)
		_ = measuredUsage(deps, AllNamespaces, watcher, nil)
		synctest.Wait()
		m := measuredUsage(deps, AllNamespaces, watcher, nil)
		if m.nodesReason != "" || m.nodes == nil {
			t.Fatalf("node usage = %q %v", m.nodesReason, m.nodes)
		}
		if !strings.Contains(m.podsReason, "newest sample was 5m") || m.clusterPods != nil {
			t.Fatalf("stale pod usage must be unknown: %q %v", m.podsReason, m.clusterPods)
		}
		if want := start.Add(-5 * time.Minute); !m.usageAt.Equal(want) {
			t.Fatalf("usageAt = %v, want the oldest newest sample %v", m.usageAt, want)
		}
		if !m.usageReadAt.Equal(start) {
			t.Fatalf("usageReadAt = %v, want the read %v", m.usageReadAt, start)
		}
		cancel()
		synctest.Wait()
	})
}

func TestMergeUsageColumnsStaleObject(t *testing.T) {
	t.Parallel()
	read := overviewTestNow
	values := map[string]usageValues{
		"ns/a": {cpu: 250, mem: 100 << 20, cpuReq: 500, memReq: 200 << 20, cpuLim: 1000, memLim: 400 << 20, sampled: read.Add(-10 * time.Second)},
		"ns/b": {cpu: 250, mem: 100 << 20, cpuReq: 500, memReq: 200 << 20, cpuLim: 1000, memLim: 400 << 20, sampled: read.Add(-10 * time.Minute)},
	}
	got := cellsOf(mergeUsageColumns(podRowList(), usagePods, "ns", &usageSnapshot{served: true, at: read, values: values}, true))
	if got[0][2] != "250m" {
		t.Errorf("a current sample shows: %v", got[0])
	}
	for _, c := range got[1][2:] {
		if c != usageNA {
			t.Fatalf("a sample ten minutes old must be n/a, got %v", got[1])
		}
	}
}

func TestBuildUsageValuesKeepsSampleTime(t *testing.T) {
	t.Parallel()
	item := metricsItem("web", "2026-11-22T12:00:10Z", "15s")
	item.SetNamespace("a")
	item.Object["containers"] = []any{map[string]any{"name": "app", "usage": map[string]any{"cpu": "10m", "memory": "1Mi"}}}
	v := buildUsageValues(usagePods, []unstructured.Unstructured{item}, nil)["a/web"]
	if want := time.Date(2026, 11, 22, 12, 0, 10, 0, time.UTC); !v.sampled.Equal(want) || v.cpu != 10 {
		t.Fatalf("values = %+v", v)
	}
}

// TestPartialPodCoverage checks that pods without PodMetrics make the pod
// usage a partial figure, never a complete one.
func TestPartialPodCoverage(t *testing.T) {
	t.Parallel()
	pod := func(ns, name string) *corev1.Pod {
		return onNode(withPod(overviewRunningPod(ns, name), func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = resources("500m", "1Gi", "", "")
		}), "mba")
	}
	objs := []crclient.Object{readyNode("mba", "4", "10Gi", 100), pod("a", "web"), pod("a", "api"), pod("b", "db")}
	partial := map[string][]containerUsage{"a/web": {{name: "app", cpu: 100, mem: 1 << 30}}}
	m := &overviewMeasured{usageAt: overviewTestNow, nodes: map[string]usageValues{"mba": {cpu: 1000, mem: 2 << 30}}, pods: partial, clusterPods: partial}
	r := measuredReport(t, AllNamespaces, m, objs...)
	s := infoSummary(r, nil, appconfig.DefaultOverview(), "")
	if mem := infoBar(t, s, "Memory"); !strings.HasSuffix(mem.Detail, ", 1 of 3 pods measured") {
		t.Fatalf("memory detail = %q", mem.Detail)
	}
	if got := mustFinding(t, r, "capacity/memory").value; !strings.Contains(got, "(1/3 measured)") {
		t.Fatalf("capacity row = %q", got)
	}
	full := map[string][]containerUsage{"a/web": {{name: "app"}}, "a/api": {{name: "app"}}, "b/db": {{name: "app"}}}
	r = measuredReport(t, AllNamespaces, &overviewMeasured{usageAt: overviewTestNow, nodes: m.nodes, pods: full, clusterPods: full}, objs...)
	if mem := infoBar(t, infoSummary(r, nil, appconfig.DefaultOverview(), ""), "Memory"); strings.Contains(mem.Detail, "measured") {
		t.Fatalf("complete coverage must not be marked: %q", mem.Detail)
	}
}
