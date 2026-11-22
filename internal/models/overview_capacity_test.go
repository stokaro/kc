package models

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

// TestCapacityJudgementWorst checks the state of a node from its four
// lines: a failure beats a warning, and among warnings overcommitted beats
// under-requested beats high. All amounts are of 100, so they read as
// percent.
func TestCapacityJudgementWorst(t *testing.T) {
	t.Parallel()
	cfg := appconfig.DefaultOverview().Normalized()
	amounts := func(pod, node, req, lim int64) capacityAmounts {
		return capacityAmounts{podUse: pod, nodeUse: node, req: req, lim: lim, alloc: 100, capacity: 100, hasPodUse: pod >= 0, hasNodeUse: node >= 0}
	}
	tests := []struct {
		name     string
		a        capacityAmounts
		wantSev  overviewSeverity
		wantFlag string
	}{
		{name: "idle", a: amounts(5, 10, 10, 20), wantSev: severityOK, wantFlag: "ok"},
		{name: "requests full", a: amounts(-1, -1, 96, 0), wantSev: severityFailed, wantFlag: "full"},
		{name: "pods full", a: amounts(91, 95, 50, 0), wantSev: severityFailed, wantFlag: "full"},
		{name: "node full", a: amounts(10, 91, 50, 0), wantSev: severityFailed, wantFlag: "full"},
		{name: "usage is ignored when not known", a: amounts(-1, -1, 50, 0), wantSev: severityOK, wantFlag: "ok"},
		{name: "overcommitted beats under-requested", a: amounts(55, 60, 10, 120), wantSev: severityWarning, wantFlag: "overcommitted"},
		{name: "pods under-requested", a: amounts(55, 60, 10, 64), wantSev: severityWarning, wantFlag: flagUnderRequested},
		{name: "under-requested beats node high", a: amounts(55, 85, 10, 64), wantSev: severityWarning, wantFlag: flagUnderRequested},
		{name: "system usage alone is not under-requested", a: amounts(5, 55, 10, 64), wantSev: severityOK, wantFlag: "ok"},
		{name: "node usage without pod usage is not under-requested", a: amounts(-1, 55, 10, 64), wantSev: severityOK, wantFlag: "ok"},
		{name: "under-requested needs the factor", a: amounts(55, 60, 30, 64), wantSev: severityOK, wantFlag: "ok"},
		{name: "under-requested needs the minimum usage", a: amounts(20, 20, 2, 10), wantSev: severityOK, wantFlag: "ok"},
		{name: "pods high", a: amounts(85, 85, 70, 0), wantSev: severityWarning, wantFlag: "high"},
		{name: "node high", a: amounts(10, 85, 70, 0), wantSev: severityWarning, wantFlag: "high"},
		{name: "requests high", a: amounts(10, 10, 85, 0), wantSev: severityWarning, wantFlag: "high"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := judgeCapacity(cfg, tc.a).worst(); got.sev != tc.wantSev || got.flag != tc.wantFlag {
				t.Fatalf("worst(%+v) = %v %q, want %v %q", tc.a, got.sev, got.flag, tc.wantSev, tc.wantFlag)
			}
		})
	}
}

// TestCapacityJudgementTruncates checks that /overview truncates as the
// Info mode and kubectl describe node do, and judges the printed number.
func TestCapacityJudgementTruncates(t *testing.T) {
	t.Parallel()
	cfg := appconfig.DefaultOverview().Normalized()
	a := capacityAmounts{podUse: 999, nodeUse: 7999, req: 9499, lim: 10009, alloc: 10000, capacity: 10000, hasPodUse: true, hasNodeUse: true}
	j := judgeCapacity(cfg, a)
	// 94.99% requests print as 94%, below requestsFailPercent (95); 79.99%
	// node usage prints as 79%, below usageWarnPercent (80); 100.09% limits
	// print as 100%, which does not exceed limitsWarnPercent (100).
	if got := j.overviewText(a); got != "pods 9% req 94% lim 100% of alloc, node 79% of cap" {
		t.Fatalf("text = %q", got)
	}
	if w := j.worst(); w.sev != severityWarning || w.flag != "high" {
		t.Fatalf("worst = %+v, want high from the requests at 94%%", w)
	}
}

func overviewTarget1(t *testing.T, f overviewFinding, node string) overviewTarget {
	t.Helper()
	for _, tg := range f.targets {
		if tg.name == node {
			return tg
		}
	}
	t.Fatalf("%s lists no node %s: %+v", f.id, node, f.targets)
	return overviewTarget{}
}

// TestOverviewCapacityAgreesWithInfo reads one report as /overview, its
// node list and the Info mode do, on the reference node of issue #165
// (capacity 15646208Ki, allocatable 10939904Ki, requests 2774Mi, limits
// 12406Mi, node usage 6144Mi, pods 2396Mi) next to a busy second node.
func TestOverviewCapacityAgreesWithInfo(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{
		withCapacity(readyNode("mba", "4", "10939904Ki", 110), "4", "15646208Ki"),
		readyNode("n2", "4", "8Gi", 110),
		onNode(withPod(overviewRunningPod("kube-system", "big"), func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = resources("590m", "2774Mi", "4500m", "12406Mi")
		}), "mba"),
		onNode(withPod(overviewRunningPod("a", "web"), func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = resources("1", "1Gi", "", "")
		}), "n2"),
	}
	podUse := map[string][]containerUsage{
		"kube-system/big": {{name: "app", cpu: 600, mem: 2396 << 20}},
		"a/web":           {{name: "app", cpu: 3000, mem: 512 << 20}},
	}
	m := &overviewMeasured{
		usageAt: overviewTestNow,
		nodes:   map[string]usageValues{"mba": {cpu: 1200, mem: 6144 << 20}, "n2": {cpu: 3500, mem: 2 << 30}},
		pods:    podUse, clusterPods: podUse,
	}
	cfg := appconfig.DefaultOverview()
	// The overview of namespace a still covers every pod on the nodes.
	r := measuredReport(t, "a", m, objs...)
	cpu, mem := mustFinding(t, r, "capacity/cpu"), mustFinding(t, r, "capacity/memory")

	t.Run("per node", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			finding   overviewFinding
			node      string
			label     string
			overview  string
			info      []string
			severity  overviewSeverity
			infoColor RowHealth
		}{
			{
				finding: mem, node: "mba", label: "Memory",
				overview: "pods 22% req 25% lim 116% of alloc, node 40% of cap lim overcommitted",
				info:     []string{"pods 2.3/10.4 Gi allocatable, 22%", "node 6.0/14.9 Gi capacity, 40%", "req 25%, lim 116% (overcommitted)"},
				severity: severityWarning, infoColor: RowHealthOK,
			},
			{
				finding: cpu, node: "mba", label: "CPU",
				overview: "pods 15% req 14% lim 112% of alloc, node 30% of cap lim overcommitted",
				info:     []string{"pods 0.6/4.0 cores allocatable, 15%", "node 1.2/4.0 cores capacity, 30%", "req 14%, lim 112% (overcommitted)"},
				severity: severityWarning, infoColor: RowHealthOK,
			},
			{
				// The pods use three times their requests; the node is high.
				finding: cpu, node: "n2", label: "CPU",
				overview: "pods 75% req 25% lim 0% of alloc, node 87% of cap pods under-req",
				info:     []string{"pods 3.0/4.0 cores allocatable, 75% (under-req)", "node 3.5/4.0 cores capacity, 87% (high)", "req 25%, lim 0%"},
				severity: severityWarning, infoColor: RowHealthNotReady,
			},
			{
				finding: mem, node: "n2", label: "Memory",
				overview: "pods 6% req 12% lim 0% of alloc, node 25% of cap",
				info:     []string{"pods 0.5/8.0 Gi allocatable, 6%", "node 2.0/8.0 Gi capacity, 25%", "req 12%, lim 0%"},
				severity: severityOK, infoColor: RowHealthOK,
			},
		}
		for _, tc := range tests {
			tg := overviewTarget1(t, tc.finding, tc.node)
			if tg.value != tc.overview || tg.severity != tc.severity {
				t.Errorf("%s %s in /overview = %q %v, want %q %v", tc.node, tc.label, tg.value, tg.severity, tc.overview, tc.severity)
			}
			b := infoBar(t, infoSummary(r, nil, cfg, tc.node), tc.label)
			if !slices.Equal(barLines(b), tc.info) || b.Health != tc.infoColor {
				t.Errorf("%s %s in Info = %q %v, want %q %v", tc.node, tc.label, barLines(b), b.Health, tc.info, tc.infoColor)
			}
		}
	})
	t.Run("totals", func(t *testing.T) {
		t.Parallel()
		// cpu: pods 3600, requests 1590, limits 4500 of 8000 allocatable;
		// node 4700 of 8000 capacity.
		if want := "pods 45% req 19% lim 56% of alloc, node 58% of cap"; cpu.value != want {
			t.Errorf("cpu row = %q, want %q", cpu.value, want)
		}
		b := infoBar(t, infoSummary(r, nil, cfg, ""), "CPU")
		if want := []string{"pods 3.6/8.0 cores allocatable, 45% (under-req)", "node 4.7/8.0 cores capacity, 58%", "req 19%, lim 56%"}; !slices.Equal(barLines(b), want) {
			t.Errorf("Info CPU = %q, want %q", barLines(b), want)
		}
		// The row's state is its worst node; the most loaded comes first.
		if cpu.severity != severityWarning || cpu.state != "lim overcommitted, 1 of 2 nodes" || !slices.Equal(targetKeys(cpu), []string{"nodes:mba", "nodes:n2"}) {
			t.Errorf("cpu row = %q %v targets %v", cpu.state, cpu.severity, targetKeys(cpu))
		}
	})
	t.Run("F3 names the inputs", func(t *testing.T) {
		t.Parallel()
		for _, want := range []string{"status.allocatable.memory", "status.capacity.memory", "PodMetrics", "NodeMetrics", "truncated", "cannot make the pods look under-requested"} {
			if !strings.Contains(mem.explain, want) {
				t.Errorf("memory explain lacks %q:\n%s", want, mem.explain)
			}
		}
		var cmds []string
		for _, c := range mem.kubectl {
			cmds = append(cmds, strings.Join(c, " "))
		}
		want := []string{
			"describe nodes",
			"top pods -A",
			"get pods -A --field-selector status.phase!=Succeeded,status.phase!=Failed -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,NODE:.spec.nodeName",
			"top nodes",
			"get nodes -o custom-columns=NAME:.metadata.name,CAPACITY:.status.capacity.memory,ALLOCATABLE:.status.allocatable.memory",
		}
		if !slices.Equal(cmds, want) {
			t.Errorf("memory commands = %q, want %q", cmds, want)
		}
	})
}

// TestOverviewCapacityUnderRequestedByPods checks that only the pods' own
// usage makes a node under-requested: a node that uses 55% while its pods
// use less than they request is not, in /overview or in the Info mode.
func TestOverviewCapacityUnderRequestedByPods(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{
		withCapacity(readyNode("mba", "10", "10Gi", 110), "10", "12Gi"),
		onNode(withPod(overviewRunningPod("a", "busy"), func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = resources("100m", "1Gi", "", "")
		}), "mba"),
	}
	cfg := appconfig.DefaultOverview()
	tests := []struct {
		name      string
		podMem    int64
		wantState string // the /overview row
		wantFlag  string // the Info bar
		wantText  string
	}{
		{name: "system usage", podMem: 512 << 20, wantState: "ok", wantFlag: "ok", wantText: "pods 5% req 10% lim 0% of alloc, node 55% of cap"},
		{name: "pod usage", podMem: 4 << 30, wantState: "pods " + flagUnderRequested, wantFlag: flagUnderRequested, wantText: "pods 40% req 10% lim 0% of alloc, node 55% of cap"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			use := map[string][]containerUsage{"a/busy": {{name: "app", cpu: 50, mem: tc.podMem}}}
			// 6.6 GiB of 12 GiB capacity: 55%.
			m := &overviewMeasured{nodes: map[string]usageValues{"mba": {cpu: 400, mem: 6800 << 20}}, pods: use, clusterPods: use}
			r := measuredReport(t, AllNamespaces, m, objs...)
			f := mustFinding(t, r, "capacity/memory")
			if f.state != tc.wantState || f.value != tc.wantText {
				t.Errorf("/overview memory = %q %q, want %q %q", f.value, f.state, tc.wantText, tc.wantState)
			}
			if b := infoBar(t, infoSummary(r, nil, cfg, "mba"), "Memory"); b.Flag != tc.wantFlag {
				t.Errorf("Info memory flag = %q, want %q", b.Flag, tc.wantFlag)
			}
		})
	}
}

// TestOverviewCapacityMissingSamples checks that pod usage that is not known
// stays n/a: it never falls back to the node usage, and a node none of whose
// pods is measured is not shown as idle.
func TestOverviewCapacityMissingSamples(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{
		readyNode("n1", "4", "8Gi", 110), readyNode("n2", "4", "8Gi", 110), readyNode("n3", "4", "8Gi", 110),
		onNode(overviewRunningPod("a", "web"), "n1"),
		onNode(overviewRunningPod("a", "new"), "n2"),
	}
	cfg := appconfig.DefaultOverview()

	t.Run("a node without pod samples", func(t *testing.T) {
		t.Parallel()
		// n2's only pod has no PodMetrics yet, n3 has no pods, and n3 has no
		// NodeMetrics.
		use := map[string][]containerUsage{"a/web": {{name: "app", cpu: 400, mem: 1 << 30}}}
		m := &overviewMeasured{nodes: map[string]usageValues{"n1": {cpu: 800, mem: 2 << 30}, "n2": {cpu: 2000, mem: 4 << 30}}, pods: use, clusterPods: use}
		r := measuredReport(t, AllNamespaces, m, objs...)
		cpu := mustFinding(t, r, "capacity/cpu")
		want := map[string]string{
			"n1": "pods 10% req 0% lim 0% of alloc, node 20% of cap",
			"n2": "pods n/a req 0% lim 0% of alloc, node 50% of cap",
			"n3": "pods 0% req 0% lim 0% of alloc, node n/a",
		}
		for node, w := range want {
			if got := overviewTarget1(t, cpu, node).value; got != w {
				t.Errorf("%s = %q, want %q", node, got, w)
			}
		}
		if cpu.value != "pods n/a req 0% lim 0% of alloc, node n/a" {
			t.Errorf("total = %q, want n/a while a node is not known", cpu.value)
		}
		n2 := infoBar(t, infoSummary(r, nil, cfg, "n2"), "CPU")
		if n2.Usage || !slices.Contains(barLines(n2), "pods use n/a: no PodMetrics for the pods on node n2 yet") || !slices.Contains(barLines(n2), "node 2.0/4.0 cores capacity, 50%") {
			t.Errorf("Info n2 = %+v, want the requests with pod usage n/a", n2)
		}
		all := infoBar(t, infoSummary(r, nil, cfg, ""), "CPU")
		if all.Usage || !slices.Contains(barLines(all), "pods use n/a: no PodMetrics for the pods on node n2 yet") || !slices.Contains(barLines(all), "node use n/a: no NodeMetrics for node n3 yet") {
			t.Errorf("Info total = %q, want both usages n/a with their node", barLines(all))
		}
	})
	t.Run("no PodMetrics of every namespace", func(t *testing.T) {
		t.Parallel()
		m := &overviewMeasured{
			nodes:       map[string]usageValues{"n1": {cpu: 4000, mem: 8 << 30}},
			podsReason:  "loading",
			clusterPods: nil, clusterPodsReason: "metrics.k8s.io: forbidden",
		}
		r := measuredReport(t, AllNamespaces, m, objs...)
		cpu := mustFinding(t, r, "capacity/cpu")
		n1 := overviewTarget1(t, cpu, "n1")
		// The node is full on its own line; the pods stay n/a.
		if n1.value != "pods n/a req 0% lim 0% of alloc, node 100% of cap node full" || n1.severity != severityFailed {
			t.Errorf("n1 = %q %v", n1.value, n1.severity)
		}
		if !strings.Contains(cpu.explain, "Pod usage is not available: metrics.k8s.io: forbidden.") {
			t.Errorf("explain lacks the pod usage reason:\n%s", cpu.explain)
		}
	})
}

// TestOverviewCapacityThresholds checks that the overview: thresholds move
// /overview and the Info mode together.
func TestOverviewCapacityThresholds(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{readyNode("n1", "4", "8Gi", 110), onNode(withPod(overviewRunningPod("a", "web"), func(p *corev1.Pod) {
		p.Spec.Containers[0].Resources = resources("1", "1Gi", "", "")
	}), "n1")}
	use := map[string][]containerUsage{"a/web": {{name: "app", cpu: 1000, mem: 1 << 30}}}
	m := &overviewMeasured{nodes: map[string]usageValues{"n1": {cpu: 1600, mem: 2 << 30}}, pods: use, clusterPods: use}
	tests := []struct {
		name     string
		cfg      appconfig.OverviewConfig
		wantFlag string
		wantNode string
	}{
		{name: "defaults", cfg: appconfig.DefaultOverview(), wantFlag: "ok", wantNode: "node 1.6/4.0 cores capacity, 40%"},
		{name: "usage warn 40", cfg: appconfig.OverviewConfig{UsageWarnPercent: 40}, wantFlag: "node high", wantNode: "node 1.6/4.0 cores capacity, 40% (high)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cl := newOverviewFakeClient(objs...)
			r := computeOverview(loadOverviewInput(t.Context(), cl, AllNamespaces, nil), tc.cfg, overviewTestNow, m)
			if got := mustFinding(t, r, "capacity/cpu").state; got != tc.wantFlag {
				t.Errorf("/overview cpu = %q, want %q", got, tc.wantFlag)
			}
			b := infoBar(t, infoSummary(r, nil, tc.cfg, ""), "CPU")
			if !slices.Contains(barLines(b), tc.wantNode) {
				t.Errorf("Info cpu = %q, want %q", barLines(b), tc.wantNode)
			}
		})
	}
}
