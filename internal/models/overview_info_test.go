package models

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

func infoBar(t *testing.T, s InfoSummary, label string) InfoBar {
	t.Helper()
	for _, b := range s.Bars {
		if b.Label == label {
			return b
		}
	}
	t.Fatalf("no %s bar in %+v", label, s.Bars)
	return InfoBar{}
}

// barLines is the text under a bar, one string per line.
func barLines(b InfoBar) []string { return append([]string{b.Detail}, b.More...) }

func withCapacity(n *corev1.Node, cpu, mem string) *corev1.Node {
	n.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
	return n
}

func TestInfoSummary(t *testing.T) {
	t.Parallel()
	pod := func(ns, name, node, mem string) *corev1.Pod {
		return onNode(withPod(overviewRunningPod(ns, name), func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = resources("500m", mem, "", "")
		}), node)
	}
	objs := []crclient.Object{
		withCapacity(readyNode("mba", "4", "10Gi", 100), "4", "12Gi"), withCapacity(readyNode("n2", "4", "10Gi", 100), "4", "12Gi"),
		pod("a", "web", "mba", "1Gi"), pod("a", "api", "n2", "1Gi"), pod("b", "db", "mba", "1Gi"),
	}
	podUse := map[string][]containerUsage{
		"a/web": {{name: "app", cpu: 100, mem: 300 << 20}},
		"a/api": {{name: "app", cpu: 50, mem: 500 << 20}},
		"b/db":  {{name: "app", cpu: 10, mem: 900 << 20}},
	}
	m := &overviewMeasured{
		usageAt:     overviewTestNow,
		nodes:       map[string]usageValues{"mba": {cpu: 1000, mem: 5632 << 20}, "n2": {cpu: 400, mem: 1 << 30}},
		pods:        podUse,
		clusterPods: podUse,
		stats:       map[string]*nodeStats{"mba": {nodeFS: fsUse{used: gib(29), capacity: gib(915)}}},
	}
	cfg := appconfig.DefaultOverview()

	t.Run("the whole scope", func(t *testing.T) {
		t.Parallel()
		r := measuredReport(t, "a", m, objs...)
		s := infoSummary(r, nil, cfg, "")
		if s.Loading || s.Err != "" || !s.UsageAt.Equal(overviewTestNow) {
			t.Fatalf("summary state = %+v", s)
		}
		// The nodes' pods in every namespace count, not only those of a.
		cpu := infoBar(t, s, "CPU")
		want := []string{"pods 0.2/8.0 cores allocatable, 2%", "node 1.4/8.0 cores capacity, 17%", "req 18%, lim 0%"}
		if !cpu.Known || !cpu.Usage || cpu.Percent != 2 || !slices.Equal(barLines(cpu), want) {
			t.Fatalf("cpu = %+v, want %q at 2%%", cpu, want)
		}
		if pods := infoBar(t, s, "Pods"); pods.Detail != "3/200" {
			t.Fatalf("pods = %+v (capacity counts every pod on the nodes)", pods)
		}
		if disk := infoBar(t, s, "Disk"); !disk.Known || disk.Detail != "29/915 Gi" {
			t.Fatalf("disk = %+v", disk)
		}
		names := make([]string, 0)
		for _, c := range s.Top {
			names = append(names, c.Namespace+"/"+c.Name+" "+c.Memory)
		}
		if !slices.Equal(names, []string{"a/api 500Mi", "a/web 300Mi"}) {
			t.Fatalf("top = %v, want the scope's pods by memory", names)
		}
	})
	t.Run("one node", func(t *testing.T) {
		t.Parallel()
		r := measuredReport(t, AllNamespaces, m, objs...)
		s := infoSummary(r, nil, cfg, "mba")
		// The node uses 55% of allocatable, but its pods use 11% and
		// request 20%: not under-requested, the rest runs outside pods.
		mem := infoBar(t, s, "Memory")
		want := []string{"pods 1.2/10.0 Gi allocatable, 11%", "node 5.5/12.0 Gi capacity, 45%", "req 20%, lim 0%"}
		if mem.Flag != "ok" || mem.Health != RowHealthOK || mem.Percent != 11 || !slices.Equal(barLines(mem), want) {
			t.Fatalf("mba memory = %+v, want ok %q", mem, want)
		}
		if got := []string{s.Top[0].Name, s.Top[1].Name}; len(s.Top) != 2 || !slices.Equal(got, []string{"db", "web"}) {
			t.Fatalf("top on mba = %+v", s.Top)
		}
		if n := infoSummary(r, nil, cfg, "gone"); infoBar(t, n, "CPU").Known || !strings.Contains(infoBar(t, n, "CPU").Detail, "gone is not known") {
			t.Fatalf("unknown node = %+v", n.Bars)
		}
	})
	t.Run("pods above their requests", func(t *testing.T) {
		t.Parallel()
		heavy := map[string][]containerUsage{"a/web": {{name: "app", mem: 4 << 30}}, "b/db": {{name: "app", mem: 2 << 30}}}
		r := measuredReport(t, AllNamespaces, &overviewMeasured{usageAt: overviewTestNow, nodes: m.nodes, pods: heavy, clusterPods: heavy}, objs...)
		mem := infoBar(t, infoSummary(r, nil, cfg, "mba"), "Memory")
		if mem.Flag != flagUnderRequested || mem.Health != RowHealthNotReady || mem.Detail != "pods 6.0/10.0 Gi allocatable, 60% (under-req)" {
			t.Fatalf("mba memory = %+v, want under-req at 60%%", mem)
		}
	})
	t.Run("nothing measured", func(t *testing.T) {
		t.Parallel()
		reason := "metrics.k8s.io is not served by this cluster"
		r := measuredReport(t, AllNamespaces, &overviewMeasured{nodesReason: reason, podsReason: reason, clusterPodsReason: reason, statsReason: "forbidden: nodes/proxy"}, objs...)
		s := infoSummary(r, nil, cfg, "")
		cpu := infoBar(t, s, "CPU")
		want := []string{"req 1.5/8.0 cores allocatable, 18%", "lim 0%", "use n/a: " + reason}
		if !cpu.Known || cpu.Usage || cpu.Percent != 18 || !slices.Equal(barLines(cpu), want) {
			t.Fatalf("cpu without usage = %+v, want the requests %q", cpu, want)
		}
		if disk := infoBar(t, s, "Disk"); disk.Known || disk.Detail != "forbidden: nodes/proxy" {
			t.Fatalf("disk = %+v", disk)
		}
		if len(s.Top) != 0 || s.TopNote != "metrics unavailable: "+reason {
			t.Fatalf("top = %+v note %q", s.Top, s.TopNote)
		}
	})
	t.Run("node usage without pod usage", func(t *testing.T) {
		t.Parallel()
		r := measuredReport(t, AllNamespaces, &overviewMeasured{usageAt: overviewTestNow, nodes: m.nodes, podsReason: "loading", clusterPodsReason: "loading"}, objs...)
		mem := infoBar(t, infoSummary(r, nil, cfg, "mba"), "Memory")
		want := []string{"req 2.0/10.0 Gi allocatable, 20%", "lim 0%", "pods use n/a: loading", "node 5.5/12.0 Gi capacity, 45%"}
		if mem.Usage || !slices.Equal(barLines(mem), want) {
			t.Fatalf("mba memory = %+v, want %q", mem, want)
		}
	})
	t.Run("before the first report", func(t *testing.T) {
		t.Parallel()
		if s := infoSummary(nil, nil, cfg, ""); !s.Loading {
			t.Fatalf("summary = %+v, want loading", s)
		}
		if s := infoSummary(nil, errors.New("no cluster"), cfg, ""); s.Loading || s.Err != "no cluster" {
			t.Fatalf("summary = %+v, want the error", s)
		}
	})
}

// TestInfoSummaryReferenceNode checks the CPU and memory rows against the
// numbers of issue #165, where kubectl describe node and kubectl top read
// the same node: capacity 15646208Ki, allocatable 10939904Ki, requests
// 2774Mi (25%), limits 12406Mi (116%), node usage 6144Mi, pods 2396Mi.
func TestInfoSummaryReferenceNode(t *testing.T) {
	t.Parallel()
	node := withCapacity(readyNode("mba", "4", "10939904Ki", 110), "4", "15646208Ki")
	big := onNode(withPod(overviewRunningPod("kube-system", "big"), func(p *corev1.Pod) {
		p.Spec.Containers[0].Resources = resources("590m", "2774Mi", "4500m", "12406Mi")
	}), "mba")
	m := &overviewMeasured{
		usageAt:     overviewTestNow,
		nodes:       map[string]usageValues{"mba": {cpu: 1200, mem: 6144 << 20}},
		pods:        make(map[string][]containerUsage),
		clusterPods: map[string][]containerUsage{"kube-system/big": {{name: "app", cpu: 600, mem: 2396 << 20}}},
	}
	cfg := appconfig.DefaultOverview()
	for _, focus := range []string{"", "mba"} {
		t.Run("node "+focus, func(t *testing.T) {
			t.Parallel()
			s := infoSummary(measuredReport(t, "empty", m, node, big), nil, cfg, focus)
			tests := []struct {
				label string
				pct   float64
				want  []string
			}{
				{"Memory", 22, []string{"pods 2.3/10.4 Gi allocatable, 22%", "node 6.0/14.9 Gi capacity, 40%", "req 25%, lim 116% (overcommitted)"}},
				{"CPU", 15, []string{"pods 0.6/4.0 cores allocatable, 15%", "node 1.2/4.0 cores capacity, 30%", "req 14%, lim 112% (overcommitted)"}},
			}
			for _, tc := range tests {
				b := infoBar(t, s, tc.label)
				// The bar is the pods' usage; overcommitted belongs to the
				// limits, so the bar's word stays ok while its color warns.
				if !b.Usage || b.Percent != tc.pct || b.Flag != "ok" || b.Health != RowHealthNotReady || !slices.Equal(barLines(b), tc.want) {
					t.Errorf("%s = %+v, want %q at %v%%", tc.label, b, tc.want, tc.pct)
				}
			}
			want := "no pods in empty"
			if focus != "" {
				want = "no pods in empty on node mba"
			}
			if len(s.Top) != 0 || s.TopNote != want {
				t.Errorf("top = %+v note %q, want %q", s.Top, s.TopNote, want)
			}
		})
	}
}

// TestInfoTopNote checks that the note under "Top pods by memory" tells an
// empty scope from missing metrics.
func TestInfoTopNote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		info overviewInfoInput
		node string
		want string
	}{
		{name: "empty namespace", info: overviewInfoInput{namespace: "empty"}, want: "no pods in empty"},
		{name: "empty cluster", info: overviewInfoInput{namespace: AllNamespaces}, want: "no pods"},
		{name: "pods elsewhere", info: overviewInfoInput{namespace: "a", scopedPods: 2, scopedPodsOnNode: map[string]int{"n2": 2}}, node: "mba", want: "no pods in a on node mba"},
		{name: "metrics not served", info: overviewInfoInput{namespace: "a", scopedPods: 2, podsReason: "metrics.k8s.io is not served by this cluster"}, want: "metrics unavailable: metrics.k8s.io is not served by this cluster"},
		{name: "pods not measured yet", info: overviewInfoInput{namespace: "a", scopedPods: 1}, want: "metrics unavailable: no PodMetrics for these pods yet"},
		{name: "pods cannot be read", info: overviewInfoInput{namespace: "a", podsMissing: "pods: forbidden"}, want: "pods cannot be read: pods: forbidden"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := topNote(tc.info, tc.node); got != tc.want {
				t.Fatalf("topNote = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInfoTopNoteFromReport checks the note end to end: a namespace with
// pods and no metrics versus a namespace without pods.
func TestInfoTopNoteFromReport(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{readyNode("mba", "4", "8Gi", 10), onNode(overviewRunningPod("a", "web"), "mba")}
	cfg := appconfig.DefaultOverview()
	noMetrics := &overviewMeasured{nodesReason: "loading", podsReason: "metrics.k8s.io: forbidden", clusterPodsReason: "metrics.k8s.io: forbidden"}
	if got := infoSummary(measuredReport(t, "a", noMetrics, objs...), nil, cfg, "").TopNote; got != "metrics unavailable: metrics.k8s.io: forbidden" {
		t.Errorf("namespace with pods: note %q", got)
	}
	if got := infoSummary(measuredReport(t, "b", noMetrics, objs...), nil, cfg, "").TopNote; got != "no pods in b" {
		t.Errorf("namespace without pods: note %q", got)
	}
}

// TestCapacityBarTruncates checks the kubectl describe node rounding: a
// share is truncated, and the states compare the printed number.
func TestCapacityBarTruncates(t *testing.T) {
	t.Parallel()
	cfg := appconfig.DefaultOverview().Normalized()
	a := capacityAmounts{podUse: 999, nodeUse: 999, req: 9599, lim: 10009, alloc: 10000, capacity: 10000, hasPodUse: true, hasNodeUse: true}
	b := capacityBar(cfg, "CPU", a, formatCores, "cores")
	// 95.99% requests print as 95%, which reaches requestsFailPercent;
	// 100.09% limits print as 100%, which does not exceed 100.
	want := []string{"pods 1.0/10.0 cores allocatable, 9%", "node 1.0/10.0 cores capacity, 9%", "req 95% (full), lim 100%"}
	if !slices.Equal(barLines(b), want) || b.Health != RowHealthFailed {
		t.Fatalf("bar = %+v, want %q", b, want)
	}
}

// TestMeasuredUsageClusterPods checks which PodMetrics a node's pod usage
// comes from: the Info engine of one namespace polls every namespace too,
// an all-namespaces scope reuses its one poll, and /overview polls only its
// scope.
func TestMeasuredUsageClusterPods(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		namespace   string
		wantPolls   []string
		wantCluster string // the pod key clusterPods holds
	}{
		{name: "one namespace", namespace: "a", wantPolls: []string{"nodes:", "pods:", "pods:a"}, wantCluster: "kube-system/big"},
		{name: "every namespace", namespace: AllNamespaces, wantPolls: []string{"nodes:", "pods:"}, wantCluster: "kube-system/big"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				var mu sync.Mutex
				polls := make(map[string]bool)
				usage := &UsageMetrics{interval: usagePollInterval, now: time.Now, fetch: func(_ context.Context, _ Deps, kind usageKind, ns string, _ bool) *usageSnapshot {
					mu.Lock()
					polls[fmt.Sprintf("%s:%s", kind, ns)] = true
					mu.Unlock()
					if kind == usageNodes {
						return &usageSnapshot{served: true, at: time.Now(), values: map[string]usageValues{"n1": {}}}
					}
					key := "a/web"
					if ns == AllNamespaces {
						key = "kube-system/big"
					}
					return &usageSnapshot{served: true, at: time.Now(), containers: map[string][]containerUsage{key: {{name: "app"}}}}
				}}
				deps := Deps{Ctx: t.Context(), Usage: usage}
				watcher := new(int)
				_ = measuredUsage(deps, tc.namespace, watcher, nil)
				synctest.Wait()
				m := measuredUsage(deps, tc.namespace, watcher, nil)
				mu.Lock()
				got := make([]string, 0, len(polls))
				for p := range polls {
					got = append(got, p)
				}
				mu.Unlock()
				slices.Sort(got)
				if !slices.Equal(got, tc.wantPolls) {
					t.Errorf("polls = %v, want %v", got, tc.wantPolls)
				}
				if _, ok := m.clusterPods[tc.wantCluster]; !ok || m.clusterPodsReason != "" {
					t.Errorf("clusterPods = %v (%q), want %s", m.clusterPods, m.clusterPodsReason, tc.wantCluster)
				}
			})
		})
	}
}

// TestOverviewInfoStartsAndCloses checks the handle the Info mode holds: a
// read starts its engine, Close stops it.
func TestOverviewInfoStartsAndCloses(t *testing.T) {
	t.Parallel()
	info := NewOverviewInfo(Deps{Ctx: t.Context()}, AllNamespaces)
	if info.engine.isRunning() {
		t.Fatal("the engine runs before the first read")
	}
	s := info.Summary(InfoFocus{})
	// Without a cluster the engine fails to connect and says so.
	for i := 0; i < 100 && s.Err == ""; i++ {
		s = info.Summary(InfoFocus{})
		if s.Err == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if s.Err != "no cluster" {
		t.Fatalf("summary = %+v, want the connect error", s)
	}
	info.Close()
	if info.engine.isRunning() {
		t.Fatal("the engine runs after Close")
	}
}
