package models

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

func measuredReport(t *testing.T, ns string, m *overviewMeasured, objs ...crclient.Object) *overviewReport {
	t.Helper()
	cl := newOverviewFakeClient(objs...)
	in := loadOverviewInput(t.Context(), cl, ns, nil)
	if len(in.missing) > 0 {
		t.Fatalf("fake cache could not list: %v", in.missing)
	}
	return computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, m)
}

func onNode(p *corev1.Pod, node string) *corev1.Pod { p.Spec.NodeName = node; return p }

// TestCapacityFlag checks the order of the capacity flags: a failure beats
// a warning, and among warnings overcommitted beats under-requested beats
// high.
func TestCapacityFlag(t *testing.T) {
	t.Parallel()
	cfg := appconfig.DefaultOverview()
	tests := []struct {
		name          string
		req, lim, use float64
		hasUse        bool
		wantSev       overviewSeverity
		wantFlag      string
	}{
		{name: "idle", req: 10, lim: 20, use: 5, hasUse: true, wantSev: severityOK, wantFlag: "ok"},
		{name: "requests full", req: 96, wantSev: severityFailed, wantFlag: "full"},
		{name: "usage full", req: 50, use: 91, hasUse: true, wantSev: severityFailed, wantFlag: "full"},
		{name: "usage full is ignored without usage", req: 50, use: 91, wantSev: severityOK, wantFlag: "ok"},
		{name: "overcommitted beats under-requested", req: 10, lim: 120, use: 55, hasUse: true, wantSev: severityWarning, wantFlag: "overcommitted"},
		{name: "under-requested: the mba case", req: 10, lim: 64, use: 55, hasUse: true, wantSev: severityWarning, wantFlag: flagUnderRequested},
		{name: "under-requested needs the factor", req: 30, lim: 64, use: 55, hasUse: true, wantSev: severityOK, wantFlag: "ok"},
		{name: "under-requested needs the minimum usage", req: 2, lim: 10, use: 20, hasUse: true, wantSev: severityOK, wantFlag: "ok"},
		{name: "usage high", req: 70, use: 85, hasUse: true, wantSev: severityWarning, wantFlag: "high"},
		{name: "requests high", req: 85, use: 10, hasUse: true, wantSev: severityWarning, wantFlag: "high"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sev, flag := capacityFlag(cfg, tc.req, tc.lim, tc.hasUse, tc.use)
			if sev != tc.wantSev || flag != tc.wantFlag {
				t.Fatalf("capacityFlag(req %v, lim %v, use %v/%v) = %v %q, want %v %q", tc.req, tc.lim, tc.use, tc.hasUse, sev, flag, tc.wantSev, tc.wantFlag)
			}
		})
	}
}

// TestOverviewCapacityUnderRequested reproduces the issue's example: a node
// that uses 55% of its memory while its pods request 10%.
func TestOverviewCapacityUnderRequested(t *testing.T) {
	t.Parallel()
	busy := onNode(withPod(overviewRunningPod("a", "busy"), func(p *corev1.Pod) {
		p.Spec.Containers[0].Resources = resources("100m", "1Gi", "", "")
	}), "mba")
	quiet := onNode(withPod(overviewRunningPod("a", "quiet"), func(p *corev1.Pod) {
		p.Spec.Containers[0].Resources = resources("100m", "1Gi", "", "")
	}), "quiet")
	m := &overviewMeasured{nodes: map[string]usageValues{
		"mba":   {cpu: 400, mem: 5632 << 20}, // 55% of 10Gi
		"quiet": {cpu: 100, mem: 1 << 30},
	}}
	r := measuredReport(t, AllNamespaces, m, readyNode("mba", "10", "10Gi", 110), readyNode("quiet", "10", "10Gi", 110), busy, quiet)
	mem := mustFinding(t, r, "capacity/memory")
	if mem.state != flagUnderRequested || mem.severity != severityWarning {
		t.Fatalf("memory = %q %v, want %s warning", mem.state, mem.severity, flagUnderRequested)
	}
	if mem.value != "use 32% req 10% lim 0%" {
		t.Fatalf("memory value = %q", mem.value)
	}
	if got := targetKeys(mem); !slices.Equal(got, []string{"nodes:mba", "nodes:quiet"}) {
		t.Fatalf("memory targets = %v, want the flagged node first", got)
	}
	if !strings.HasSuffix(mem.targets[0].value, " "+flagUnderRequested) || strings.Contains(mem.targets[1].value, flagUnderRequested) {
		t.Fatalf("target values = %q, %q", mem.targets[0].value, mem.targets[1].value)
	}
	if cpu := mustFinding(t, r, "capacity/cpu"); cpu.state != "ok" {
		t.Fatalf("cpu = %q %q, want ok", cpu.value, cpu.state)
	}
}

func TestOverviewCapacityUsageReasonInExplain(t *testing.T) {
	t.Parallel()
	m := &overviewMeasured{nodesReason: "metrics.k8s.io is not served by this cluster"}
	r := measuredReport(t, AllNamespaces, m, readyNode("n1", "4", "8Gi", 10))
	f := mustFinding(t, r, "capacity/cpu")
	if !strings.HasPrefix(f.value, "use n/a") || f.unavailable != "" {
		t.Fatalf("cpu = %q unavailable %q, want use n/a and the row still computed", f.value, f.unavailable)
	}
	if !strings.Contains(f.explain, "not served by this cluster") {
		t.Fatalf("explain does not carry the usage reason:\n%s", f.explain)
	}
}

// TestOverviewContainerLimitRules checks the near memory limit and the CPU
// at limit risks: they compare each container with its own limit.
func TestOverviewContainerLimitRules(t *testing.T) {
	t.Parallel()
	twoContainers := func(ns, name string) *corev1.Pod {
		return withPod(overviewRunningPod(ns, name), func(p *corev1.Pod) {
			p.Spec.Containers = []corev1.Container{
				{Name: "app", Resources: resources("", "", "500m", "100Mi")},
				{Name: "proxy", Resources: resources("", "", "100m", "1Gi")},
			}
		})
	}
	sidecar := withPod(overviewRunningPod("a", "sidecar"), func(p *corev1.Pod) {
		p.Spec.InitContainers = []corev1.Container{
			{Name: "log", RestartPolicy: new(corev1.ContainerRestartPolicyAlways), Resources: resources("", "", "", "100Mi")},
			{Name: "setup", Resources: resources("", "", "", "100Mi")},
		}
	})
	objs := []crclient.Object{
		readyNode("n1", "4", "8Gi", 110),
		// app is at 95% of its memory limit; proxy is far below, so the
		// pod sum (about 10%) would hide it.
		twoContainers("a", "near"),
		// app sits at its CPU limit.
		twoContainers("a", "throttled"),
		// Same usage, but no limit: nothing to compare with.
		overviewRunningPod("a", "unlimited"),
		// A sidecar counts; a plain init container that reports usage
		// does not.
		sidecar,
		// Out of the namespace scope.
		twoContainers("b", "near"),
		// Terminated pods do not count.
		withPod(twoContainers("a", "done"), func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }),
	}
	m := &overviewMeasured{pods: map[string][]containerUsage{
		"a/near":      {{name: "app", cpu: 10, mem: 95 << 20}, {name: "proxy", cpu: 1, mem: 10 << 20}},
		"a/throttled": {{name: "app", cpu: 495, mem: 10 << 20}, {name: "proxy", cpu: 1, mem: 10 << 20}},
		"a/unlimited": {{name: "app", cpu: 4000, mem: 8 << 30}},
		"a/sidecar":   {{name: "app", cpu: 1, mem: 1 << 20}, {name: "log", cpu: 1, mem: 99 << 20}, {name: "setup", cpu: 1, mem: 99 << 20}},
		"b/near":      {{name: "app", cpu: 10, mem: 95 << 20}},
		"a/done":      {{name: "app", cpu: 500, mem: 100 << 20}},
	}}
	r := measuredReport(t, "a", m, objs...)

	mem := mustFinding(t, r, "risks/pods-memory-limit")
	if got := targetKeys(mem); !slices.Equal(got, []string{"pods:a/sidecar", "pods:a/near"}) {
		t.Fatalf("near memory limit = %v, want the sidecar (99%%) then near (95%%)", got)
	}
	if mem.targets[1].value != "app 95% of 100Mi" || mem.targets[0].value != "log 99% of 100Mi" {
		t.Fatalf("values = %q, %q", mem.targets[0].value, mem.targets[1].value)
	}
	if mem.severity != severityWarning || mem.value != "2" {
		t.Fatalf("memory row = %q %v", mem.value, mem.severity)
	}
	cpu := mustFinding(t, r, "risks/pods-cpu-limit")
	if got := targetKeys(cpu); !slices.Equal(got, []string{"pods:a/throttled"}) {
		t.Fatalf("at cpu limit = %v, want only throttled", got)
	}
	if cpu.targets[0].value != "app 99% of 500m" {
		t.Fatalf("cpu value = %q", cpu.targets[0].value)
	}

	t.Run("pod usage not known", func(t *testing.T) {
		t.Parallel()
		r := measuredReport(t, "a", &overviewMeasured{podsReason: "metrics.k8s.io: forbidden"}, objs...)
		for _, id := range []string{"risks/pods-memory-limit", "risks/pods-cpu-limit"} {
			f := mustFinding(t, r, id)
			if f.unavailable != "metrics.k8s.io: forbidden" || f.value != "n/a" || f.severity != severityUnknown {
				t.Fatalf("%s = %q %v unavailable %q, want n/a with the reason", id, f.value, f.severity, f.unavailable)
			}
		}
	})
}

func gib(n float64) int64 { return int64(n * (1 << 30)) }

// TestOverviewDiskAndVolumes checks the kubelet stats rows.
func TestOverviewDiskAndVolumes(t *testing.T) {
	t.Parallel()
	nodes := []crclient.Object{readyNode("n1", "4", "8Gi", 10), readyNode("n2", "4", "8Gi", 10), readyNode("n3", "4", "8Gi", 10)}
	m := &overviewMeasured{
		stats: map[string]*nodeStats{
			// Images on the root filesystem: the same capacity is reported
			// twice and counted once.
			"n1": {nodeFS: fsUse{used: gib(29), capacity: gib(915)}, imageFS: fsUse{used: gib(29), capacity: gib(915)},
				volumes: []volumeUse{
					{namespace: "a", claim: "data", used: gib(9), capacity: gib(10)},
					{namespace: "a", claim: "logs", used: gib(1), capacity: gib(10)},
					{namespace: "b", claim: "full", used: gib(5), capacity: gib(5)},
				}},
			// A separate image filesystem that is nearly full.
			"n2": {nodeFS: fsUse{used: gib(10), capacity: gib(100)}, imageFS: fsUse{used: gib(46), capacity: gib(50)},
				volumes: []volumeUse{
					// The same claim mounted by a pod on another node.
					{namespace: "a", claim: "data", used: gib(9), capacity: gib(10)},
				}},
		},
		statsErrors: map[string]string{"n3": "forbidden: nodes/proxy"},
	}
	r := measuredReport(t, AllNamespaces, m, nodes...)
	disk := mustFinding(t, r, "capacity/disk")
	if disk.state != "full" || disk.severity != severityFailed {
		t.Fatalf("disk = %q %v, want full (imagefs of n2 at 92%%)", disk.state, disk.severity)
	}
	if got := targetKeys(disk); !slices.Equal(got, []string{"nodes:n2", "nodes:n1", "nodes:n3"}) {
		t.Fatalf("disk targets = %v, want fullest first, the node without stats last", got)
	}
	if want := "nodefs 3% (29/915Gi)"; disk.targets[1].value != want {
		t.Fatalf("n1 = %q, want %q (one filesystem)", disk.targets[1].value, want)
	}
	if want := "nodefs 10% (10/100Gi) imagefs 92% (46/50Gi) full"; disk.targets[0].value != want {
		t.Fatalf("n2 = %q, want %q", disk.targets[0].value, want)
	}
	if want := "no stats: forbidden: nodes/proxy"; disk.targets[2].value != want {
		t.Fatalf("n3 = %q, want %q", disk.targets[2].value, want)
	}
	if want := "8% (85/1065Gi)"; disk.value != want {
		t.Fatalf("disk value = %q, want %q", disk.value, want)
	}

	vols := mustFinding(t, r, "risks/volumes-full")
	if got := targetKeys(vols); !slices.Equal(got, []string{"persistentvolumeclaims:b/full", "persistentvolumeclaims:a/data"}) {
		t.Fatalf("volumes = %v, want the full claim first, data once, logs not at all", got)
	}
	if vols.severity != severityFailed || vols.targets[0].severity != severityFailed || vols.targets[1].severity != severityWarning {
		t.Fatalf("volume severities = %v / %v %v", vols.severity, vols.targets[0].severity, vols.targets[1].severity)
	}

	t.Run("namespace scope", func(t *testing.T) {
		t.Parallel()
		r := measuredReport(t, "a", m, nodes...)
		if got := targetKeys(mustFinding(t, r, "risks/volumes-full")); !slices.Equal(got, []string{"persistentvolumeclaims:a/data"}) {
			t.Fatalf("volumes in a = %v", got)
		}
	})
	t.Run("no stats", func(t *testing.T) {
		t.Parallel()
		reason := "forbidden: nodes/proxy (get on nodes/proxy is needed for the kubelet stats)"
		r := measuredReport(t, AllNamespaces, &overviewMeasured{statsReason: reason}, nodes...)
		for _, id := range []string{"capacity/disk", "risks/volumes-full"} {
			f := mustFinding(t, r, id)
			if f.unavailable != reason || f.severity != severityUnknown || f.value != "n/a" {
				t.Fatalf("%s = %q %v unavailable %q", id, f.value, f.severity, f.unavailable)
			}
		}
	})
}

func TestPollNodeStats(t *testing.T) {
	t.Parallel()
	ok := func(context.Context, string) (*nodeStats, error) { return &nodeStats{}, nil }
	forbidden := func(_ context.Context, node string) (*nodeStats, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes/proxy"}, node, errors.New("no"))
	}
	t.Run("too many nodes are skipped without a request", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		res := pollNodeStats(t.Context(), func(context.Context, string) (*nodeStats, error) { calls.Add(1); return &nodeStats{}, nil }, []string{"a", "b", "c"}, 2)
		if calls.Load() != 0 || !strings.Contains(res.reason, "3 nodes, more than statsMaxNodes (2)") {
			t.Fatalf("calls %d reason %q", calls.Load(), res.reason)
		}
	})
	t.Run("forbidden everywhere", func(t *testing.T) {
		t.Parallel()
		res := pollNodeStats(t.Context(), forbidden, []string{"a", "b"}, 10)
		if !strings.HasPrefix(res.reason, "forbidden: nodes/proxy") || len(res.stats) != 0 {
			t.Fatalf("reason %q stats %v", res.reason, res.stats)
		}
	})
	t.Run("one node fails", func(t *testing.T) {
		t.Parallel()
		fetch := func(ctx context.Context, node string) (*nodeStats, error) {
			if node == "b" {
				return nil, errors.New("dial tcp: timeout")
			}
			return ok(ctx, node)
		}
		res := pollNodeStats(t.Context(), fetch, []string{"a", "b"}, 10)
		if res.reason != "" || res.stats["a"] == nil || res.errors["b"] != "dial tcp: timeout" {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("requests in flight are bounded", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		inFlight, peak := 0, 0
		fetch := func(context.Context, string) (*nodeStats, error) {
			mu.Lock()
			inFlight++
			peak = max(peak, inFlight)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return &nodeStats{}, nil
		}
		nodes := make([]string, 20)
		for i := range nodes {
			nodes[i] = fmt.Sprintf("n%d", i)
		}
		res := pollNodeStats(t.Context(), fetch, nodes, 50)
		if len(res.stats) != 20 || peak > overviewStatsInFlight || peak < 2 {
			t.Fatalf("stats %d peak %d, want 20 and 2..%d", len(res.stats), peak, overviewStatsInFlight)
		}
	})
}

const summaryJSON = `{
  "node": {"nodeName": "n1",
    "fs": {"availableBytes": 3, "capacityBytes": 10, "usedBytes": 7},
    "runtime": {"imageFs": {"capacityBytes": 20, "usedBytes": 5}}},
  "pods": [
    {"podRef": {"name": "db-0", "namespace": "a"}, "volume": [
      {"name": "data", "capacityBytes": 100, "usedBytes": 90, "pvcRef": {"name": "data-db-0", "namespace": "a"}},
      {"name": "token", "capacityBytes": 100, "usedBytes": 1},
      {"name": "nocap", "usedBytes": 1, "pvcRef": {"name": "x", "namespace": "a"}}
    ]}
  ]
}`

// TestKubeletStatsFetcher runs the real fetcher against a fake API server:
// it asks the node proxy for the summary and decodes it.
func TestKubeletStatsFetcher(t *testing.T) {
	t.Parallel()
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/nodes/n1/proxy/stats/summary":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(summaryJSON))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"nodes \"n2\" is forbidden"}`))
		}
	}))
	defer srv.Close()
	fetch, err := newKubeletStatsFetcher(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	s, err := fetch(t.Context(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	if s.nodeFS != (fsUse{used: 7, capacity: 10}) || s.imageFS != (fsUse{used: 5, capacity: 20}) {
		t.Fatalf("filesystems = %+v %+v", s.nodeFS, s.imageFS)
	}
	if len(s.volumes) != 1 || s.volumes[0] != (volumeUse{namespace: "a", claim: "data-db-0", used: 90, capacity: 100}) {
		t.Fatalf("volumes = %+v, want only the claim with a capacity", s.volumes)
	}
	if _, err := fetch(t.Context(), "n2"); !apierrors.IsForbidden(err) {
		t.Fatalf("n2 error = %v, want Forbidden", err)
	}
	res := pollNodeStats(t.Context(), fetch, []string{"n2"}, 10)
	if !strings.HasPrefix(res.reason, "forbidden: nodes/proxy") {
		t.Fatalf("reason = %q", res.reason)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(paths, "/api/v1/nodes/n1/proxy/stats/summary") {
		t.Fatalf("paths = %v", paths)
	}
}

func TestBuildContainerUsage(t *testing.T) {
	t.Parallel()
	pm := unstructured.Unstructured{Object: map[string]any{"containers": []any{
		map[string]any{"name": "app", "usage": map[string]any{"cpu": "250m", "memory": "64Mi"}},
		map[string]any{"name": "proxy", "usage": map[string]any{"cpu": "10m", "memory": "8Mi"}},
	}}}
	pm.SetNamespace("a")
	pm.SetName("web")
	got := buildContainerUsage([]unstructured.Unstructured{pm})
	list := got["a/web"]
	slices.SortFunc(list, func(x, y containerUsage) int { return strings.Compare(x.name, y.name) })
	want := []containerUsage{{name: "app", cpu: 250, mem: 64 << 20}, {name: "proxy", cpu: 10, mem: 8 << 20}}
	if !slices.Equal(list, want) {
		t.Fatalf("containers = %+v, want %+v", list, want)
	}
}

// TestOverviewEngineMeasures runs the engine with a metrics poller and a
// stats fetcher: the metrics-only pollers are started in the overview's
// scope, a poll makes the engine recompute, and the kubelet stats reach the
// report.
func TestOverviewEngineMeasures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var mu sync.Mutex
		var fetched []string
		usage := &UsageMetrics{interval: usagePollInterval, now: time.Now, fetch: func(_ context.Context, _ Deps, kind usageKind, ns string, metricsOnly bool) *usageSnapshot {
			mu.Lock()
			fetched = append(fetched, fmt.Sprintf("%s:%s:%v", kind, ns, metricsOnly))
			mu.Unlock()
			if kind == usageNodes {
				return &usageSnapshot{served: true, values: map[string]usageValues{"n1": {cpu: 2000, mem: 1 << 30}}}
			}
			return &usageSnapshot{served: true, containers: map[string][]containerUsage{"a/web": {{name: "app", cpu: 1, mem: 63 << 20}}}}
		}}
		web := onNode(withPod(overviewRunningPod("a", "web"), func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = resources("", "", "", "64Mi")
		}), "n1")
		deps := Deps{Ctx: ctx, Usage: usage, ClusterKey: kccluster.Key{ContextName: "c"}}
		e, _ := newTestOverviewEngine(t, deps, "a", readyNode("n1", "4", "8Gi", 10), web)
		var stats atomic.Int32
		e.statsFetch = func() (statsFetcher, error) {
			return func(context.Context, string) (*nodeStats, error) {
				stats.Add(1)
				return &nodeStats{nodeFS: fsUse{used: gib(50), capacity: gib(100)}}, nil
			}, nil
		}
		defer e.close()

		var r *overviewReport
		for range 10 {
			r, _ = e.snapshot()
			if r != nil {
				if f, ok := r.finding("capacity/disk"); ok && f.unavailable == "" {
					if f, ok := r.finding("risks/pods-memory-limit"); ok && f.value == "1" {
						break
					}
				}
			}
			time.Sleep(time.Second)
			synctest.Wait()
		}
		if got := mustFinding(t, r, "capacity/cpu").value; got != "use 50% req 0% lim 0%" {
			t.Fatalf("cpu = %q", got)
		}
		if got := mustFinding(t, r, "capacity/disk").value; got != "50% (50/100Gi)" {
			t.Fatalf("disk = %q", got)
		}
		if got := targetKeys(mustFinding(t, r, "risks/pods-memory-limit")); !slices.Equal(got, []string{"pods:a/web"}) {
			t.Fatalf("near memory limit = %v", got)
		}
		mu.Lock()
		got := append([]string(nil), fetched...)
		mu.Unlock()
		if !slices.Contains(got, "nodes::true") || !slices.Contains(got, "pods:a:true") {
			t.Fatalf("polls = %v, want metrics-only pollers for nodes and the namespace's pods", got)
		}
		if stats.Load() != 1 {
			t.Fatalf("stats requests = %d, want 1 before the minute is over", stats.Load())
		}
		time.Sleep(overviewStatsEvery)
		_, _ = e.snapshot()
		synctest.Wait()
		if stats.Load() != 2 {
			t.Fatalf("stats requests after a minute = %d, want 2", stats.Load())
		}
	})
}

func TestMeasuredUsageReasons(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		snap  *usageSnapshot
		fresh bool
		want  string
	}{
		{"before the first poll", nil, false, "loading"},
		{"not served", &usageSnapshot{}, true, "metrics.k8s.io is not served by this cluster"},
		{"failed", &usageSnapshot{served: true, err: errors.New("503")}, true, "metrics.k8s.io: 503"},
		{"stale", &usageSnapshot{served: true}, false, "loading"},
		{"fresh", &usageSnapshot{served: true}, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := usageReason(tc.snap, tc.fresh); got != tc.want {
				t.Fatalf("usageReason = %q, want %q", got, tc.want)
			}
		})
	}
	if m := measuredUsage(Deps{}, "", new(int), nil); m.nodesReason != "usage metrics are off" || m.podsReason != "usage metrics are off" {
		t.Fatalf("without a registry = %+v", m)
	}
}
