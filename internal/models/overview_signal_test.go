package models

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

// TestInfoBarSignals checks that each line of a capacity bar carries its own
// state: the bar takes the state of what it shows, the requests and limits
// line that of the worse of the two, and a warning on another line is named
// by the attention mark. All amounts are of 100, so they read as percent.
func TestInfoBarSignals(t *testing.T) {
	t.Parallel()
	cfg := appconfig.DefaultOverview().Normalized()
	amounts := func(pod, node, req, lim int64) capacityAmounts {
		return capacityAmounts{
			podUse: pod, nodeUse: node, req: req, lim: lim, alloc: 100, capacity: 100,
			hasPodUse: pod >= 0, hasNodeUse: node >= 0, podReason: "loading", nodeReason: "no NodeMetrics for node n1 yet",
		}
	}
	tests := []struct {
		name          string
		a             capacityAmounts
		bar, detail   RowHealth
		more          []RowHealth
		attention     RowHealth
		attentionText string
	}{
		{
			name: "modest usage, limits above allocatable", a: amounts(20, 30, 25, 120),
			bar: RowHealthOK, detail: RowHealthOK, more: []RowHealth{RowHealthOK, RowHealthNotReady},
			attention: RowHealthNotReady, attentionText: "! lim",
		},
		{
			name: "high usage", a: amounts(85, 85, 70, 0),
			bar: RowHealthNotReady, detail: RowHealthNotReady, more: []RowHealth{RowHealthNotReady, RowHealthOK},
			attention: RowHealthNotReady,
		},
		{
			name: "high requests, low usage", a: amounts(10, 20, 85, 90),
			bar: RowHealthOK, detail: RowHealthOK, more: []RowHealth{RowHealthOK, RowHealthNotReady},
			attention: RowHealthNotReady, attentionText: "! req",
		},
		{
			name: "full requests", a: amounts(10, 20, 96, 90),
			bar: RowHealthOK, detail: RowHealthOK, more: []RowHealth{RowHealthOK, RowHealthFailed},
			attention: RowHealthFailed, attentionText: "!! req",
		},
		{
			name: "full node, quiet pods", a: amounts(10, 92, 20, 50),
			bar: RowHealthOK, detail: RowHealthOK, more: []RowHealth{RowHealthFailed, RowHealthOK},
			attention: RowHealthFailed, attentionText: "!! node",
		},
		{
			name: "requests bar, overcommitted limits", a: amounts(-1, -1, 85, 120),
			bar: RowHealthNotReady, detail: RowHealthNotReady, more: []RowHealth{RowHealthNotReady, RowHealthUnknown, RowHealthUnknown},
			attention: RowHealthNotReady, attentionText: "! lim",
		},
		{
			name: "pods measured, node not", a: amounts(10, -1, 20, 50),
			bar: RowHealthOK, detail: RowHealthOK, more: []RowHealth{RowHealthUnknown, RowHealthOK},
			attention: RowHealthOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := capacityBar(cfg, "CPU", tc.a, formatCores, "cores")
			if b.Health != tc.bar || b.DetailHealth != tc.detail || !slices.Equal(b.MoreHealth, tc.more) || b.Attention != tc.attention || b.AttentionText != tc.attentionText {
				t.Fatalf("bar %q: health %v detail %v more %v attention %v %q, want %v %v %v %v %q",
					barLines(b), b.Health, b.DetailHealth, b.MoreHealth, b.Attention, b.AttentionText,
					tc.bar, tc.detail, tc.more, tc.attention, tc.attentionText)
			}
			if len(b.MoreHealth) != len(b.More) {
				t.Fatalf("%d line states for %d lines", len(b.MoreHealth), len(b.More))
			}
		})
	}
}

// TestOverviewCapacityOneNodePressured checks a healthy sum over two nodes
// of which one is full: the row says the state comes from one node, names
// the line, and Enter lists that node first.
func TestOverviewCapacityOneNodePressured(t *testing.T) {
	t.Parallel()
	pod := func(name, node, cpu string) *corev1.Pod {
		return onNode(withPod(overviewRunningPod("a", name), func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = resources(cpu, "1Gi", "", "")
		}), node)
	}
	// n1 is listed first but quiet; n2 is the pressured one.
	objs := []crclient.Object{readyNode("n1", "4", "8Gi", 110), readyNode("n2", "4", "8Gi", 110), pod("quiet", "n1", "1"), pod("busy", "n2", "2")}
	use := map[string][]containerUsage{
		"a/quiet": {{name: "app", cpu: 100, mem: 1 << 30}},
		"a/busy":  {{name: "app", cpu: 3700, mem: 1 << 30}},
	}
	m := &overviewMeasured{nodes: map[string]usageValues{"n1": {cpu: 300, mem: 2 << 30}, "n2": {cpu: 3800, mem: 2 << 30}}, pods: use, clusterPods: use}
	r := measuredReport(t, AllNamespaces, m, objs...)
	cpu := mustFinding(t, r, "capacity/cpu")
	if cpu.value != "pods 47% req 37% lim 0% of alloc, node 51% of cap" {
		t.Fatalf("value = %q, want the healthy sum", cpu.value)
	}
	if total := sumCapacity([]capacityAmounts{r.nodes[0].cpu, r.nodes[1].cpu}); judgeCapacity(appconfig.DefaultOverview().Normalized(), total).worst() != infoStateOK {
		t.Fatalf("the sum %+v is not healthy on its own; the case proves nothing", total)
	}
	if cpu.severity != severityFailed || cpu.state != "pods full, 1 of 2 nodes" {
		t.Fatalf("state = %q %v, want pods full on 1 of 2 nodes", cpu.state, cpu.severity)
	}
	if got := targetKeys(cpu); !slices.Equal(got, []string{"nodes:n2", "nodes:n1"}) {
		t.Fatalf("targets = %v, want the pressured node first", got)
	}
	if got := cpu.targets[0].value; got != "pods 92% req 50% lim 0% of alloc, node 95% of cap pods full" {
		t.Fatalf("n2 = %q", got)
	}
	if mem := mustFinding(t, r, "capacity/memory"); mem.state != "ok" {
		t.Fatalf("memory = %q, want ok", mem.state)
	}
}
