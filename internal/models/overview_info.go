package models

import (
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/pkg/appconfig"
)

// overviewNodeSummary is the load of one node, kept in the report for the
// Info panel mode.
type overviewNodeSummary struct {
	name            string
	cpu, mem        capacityAmounts // the same figures as the /overview rows
	pods, podsAlloc int64
	disk            fsUse // node and separate image filesystem
	hasDisk         bool
}

// overviewInfoInput is what the Info mode reads from a recompute beyond the
// node and pod summaries: the scope, why usage is not known, and how many
// pods the scope has, so an empty scope is told from missing metrics.
type overviewInfoInput struct {
	namespace        string
	podsReason       string // why the scope's pod usage is not known
	podsMissing      string // why pods cannot be read
	scopedPods       int    // pods of the scope that have not terminated
	scopedPodsOnNode map[string]int
	// capacityReason is why node capacity cannot be summed: nodes or pods
	// in all namespaces cannot be read. Empty when they can.
	capacityReason string
}

func infoInput(rc *ruleContext) overviewInfoInput {
	in := overviewInfoInput{
		namespace:        rc.in.namespace,
		podsReason:       rc.m.podsReason,
		podsMissing:      rc.in.missingReason(ovPodsGVR),
		capacityReason:   rc.in.clusterWideReason(ovNodesGVR, ovPodsGVR),
		scopedPodsOnNode: make(map[string]int),
	}
	rc.scopedPods(func(p *corev1.Pod) {
		if podTerminal(p) {
			return
		}
		in.scopedPods++
		in.scopedPodsOnNode[p.Spec.NodeName]++
	})
	return in
}

// overviewPodUsage is the measured usage of one pod in the scope.
type overviewPodUsage struct {
	namespace, name, node string
	cpu, mem              int64
	owners                []InfoWorkload // the controller chain, nearest first
}

// summarizeNodes returns the load of every node, or nil when nodes or pods
// cannot be read.
func summarizeNodes(rc *ruleContext) []overviewNodeSummary {
	if rc.in.clusterWideReason(ovNodesGVR, ovPodsGVR) != "" {
		return nil
	}
	loads := nodeLoads(rc)
	out := make([]overviewNodeSummary, 0, len(loads))
	for _, l := range loads {
		s := overviewNodeSummary{name: l.node.Name, cpu: l.cpu, mem: l.mem, pods: l.pods, podsAlloc: l.podsAlloc}
		if st := rc.m.stats[l.node.Name]; st != nil {
			for _, fs := range []fsUse{st.nodeFS, separateImageFS(st)} {
				if fs.known() {
					s.disk.used += fs.used
					s.disk.capacity += fs.capacity
					s.hasDisk = true
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// summarizePods returns the measured usage of the pods in scope that have
// not terminated, the most memory first. byPod holds each pod's workloads.
func summarizePods(rc *ruleContext, byPod map[string][]InfoWorkload) []overviewPodUsage {
	if rc.m.pods == nil {
		return nil
	}
	var out []overviewPodUsage
	rc.scopedPods(func(p *corev1.Pod) {
		if podTerminal(p) {
			return
		}
		used, ok := rc.m.pods[usageObjectKey(p.Namespace, p.Name)]
		if !ok {
			return
		}
		u := overviewPodUsage{namespace: p.Namespace, name: p.Name, node: p.Spec.NodeName, owners: byPod[usageObjectKey(p.Namespace, p.Name)]}
		for _, c := range used {
			u.cpu += c.cpu
			u.mem += c.mem
		}
		out = append(out, u)
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].mem != out[j].mem {
			return out[i].mem > out[j].mem
		}
		return out[i].namespace+"/"+out[i].name < out[j].namespace+"/"+out[j].name
	})
	return out
}

// InfoBar is one bar of the Info panel mode.
type InfoBar struct {
	// Label names the resource: CPU, Memory, Pods, Disk.
	Label string
	// Percent fills the bar: the pods' measured usage of allocatable for
	// CPU and memory (requests when no pod usage is known), pods of
	// allocatable pods, used bytes of capacity for disk.
	Percent float64
	// Known is false when nothing can be shown; Detail then says why.
	Known bool
	// Usage is true when Percent is measured usage, which the sparkline
	// records.
	Usage bool
	// Base names what a usage Percent is a share of ("allocatable",
	// "limits", "requests"), for the history label. A history restarts
	// when the base changes, since the percentages no longer compare.
	Base string
	// Detail is the first line under the bar: what Percent shows.
	Detail string
	// More holds further lines under Detail.
	More []string
	// Flag is the state word of what Percent shows: ok, high, full,
	// under-req, ... A state of another line (overcommitted limits) is in
	// that line.
	Flag string
	// Health colors the bar and its percentage like a row of an object
	// list: the state of what Percent shows, so modest usage keeps a normal
	// bar while the limits are overcommitted.
	Health RowHealth
	// DetailHealth and MoreHealth color the lines under the bar, each by
	// its own state; MoreHealth is parallel to More and may be shorter.
	DetailHealth RowHealth
	MoreHealth   []RowHealth
	// Attention is the worst state of all lines. When another line than the
	// one the bar shows causes it, AttentionText names that line ("! lim",
	// "!! node"; one mark for a warning, two for a failure) and follows the
	// percentage.
	Attention     RowHealth
	AttentionText string
}

// InfoConsumer is one of the pods that use the most memory.
type InfoConsumer struct {
	Namespace, Name string
	CPU, Memory     string
}

// InfoSummary is what the Info panel mode shows for a scope or a node.
type InfoSummary struct {
	// Loading is true before the first report.
	Loading bool
	// Err is why the overview cannot run.
	Err string
	// Node is the node the summary is about; empty for the whole scope.
	Node string
	// Workload is the workload the summary is about, if any.
	Workload *InfoWorkload
	// UsageAt is when metrics-server measured the CPU and memory usage
	// (the metrics' own timestamp), UsageWindow the window it measured
	// over, UsageReadAt when kc read it. UsageAt is zero when the metrics
	// carry no timestamp, UsageReadAt when no usage was read.
	UsageAt     time.Time
	UsageWindow time.Duration
	UsageReadAt time.Time
	Bars        []InfoBar
	// Top lists the pods with the most memory usage, at most TopN.
	Top []InfoConsumer
	// TopNote says why Top is empty.
	TopNote string
	// BarsTitle and TopTitle name what the bars and the top pods cover. They
	// differ in a namespace scope: the bars sum every node of the cluster,
	// the top pods are those of the namespace.
	BarsTitle, TopTitle string
	// TopNamespace is the one namespace every top pod is in, which
	// TopTitle names; empty when they may come from several.
	TopNamespace string
}

// infoTitles names the scope of the bars and of the top pods: the cluster
// or one node, and the namespace scope (on that node).
func infoTitles(namespace, node string) (bars, top string) {
	where := "in " + namespaceScopeText(namespace)
	if node == "" {
		return "Cluster capacity (all nodes, all namespaces)", "Top pods by memory " + where
	}
	return "Node " + node + " (all its pods, all namespaces)", "Top pods by memory " + where + " on node " + node
}

// InfoTopN is how many consumers the Info mode lists.
const InfoTopN = 5

// OverviewInfo serves the Info panel mode from an overview engine of its
// own, so the mode keeps the same data rules as /overview: the engine runs
// while Summary is called, and stops when the mode is left (Close).
type OverviewInfo struct {
	engine *overviewEngine
}

// NewOverviewInfo returns the Info source for deps' cluster in namespace
// (AllNamespaces for the whole cluster). It starts nothing until Summary.
func NewOverviewInfo(deps Deps, namespace string) *OverviewInfo {
	return &OverviewInfo{engine: newOverviewEngine(deps, namespace)}
}

// Close stops the engine.
func (o *OverviewInfo) Close() { o.engine.close() }

// Reread recomputes the summary on the engine's next tick instead of after
// the next event; it reads the shared cache.
func (o *OverviewInfo) Reread() { o.engine.reread() }

// InfoFocus narrows the Info summary: to one workload, else to one node,
// else the whole scope.
type InfoFocus struct {
	Node     string
	Workload *InfoWorkload
}

// Summary returns the bars and top consumers of what focus names. It counts
// as a read of the engine and does no I/O.
func (o *OverviewInfo) Summary(focus InfoFocus) InfoSummary {
	report, err := o.engine.snapshot()
	if focus.Workload != nil {
		return workloadInfoSummary(report, err, o.engine.cfg, *focus.Workload)
	}
	return infoSummary(report, err, o.engine.cfg, focus.Node)
}

func infoSummary(report *overviewReport, err error, cfgIn appconfig.OverviewConfig, node string) InfoSummary {
	cfg := cfgIn.Normalized()
	out := InfoSummary{Node: node}
	if report == nil {
		out.Loading = err == nil
		if err != nil {
			out.Err = err.Error()
		}
		return out
	}
	out.UsageAt, out.UsageWindow, out.UsageReadAt = report.usageAt, report.usageWindow, report.usageReadAt
	out.BarsTitle, out.TopTitle = infoTitles(report.info.namespace, node)
	out.TopNamespace = report.info.namespace
	var sum overviewNodeSummary
	var cpus, mems []capacityAmounts
	anyDisk := false
	for _, n := range report.nodes {
		if node != "" && n.name != node {
			continue
		}
		cpus, mems = append(cpus, n.cpu), append(mems, n.mem)
		sum.pods, sum.podsAlloc = sum.pods+n.pods, sum.podsAlloc+n.podsAlloc
		if n.hasDisk {
			anyDisk = true
			sum.disk.used += n.disk.used
			sum.disk.capacity += n.disk.capacity
		}
	}
	if len(cpus) == 0 {
		note := "nodes and pods cannot be read"
		if r := report.info.capacityReason; r != "" {
			note = "capacity needs every node and pod: " + r
		}
		if node != "" {
			note = "node " + node + " is not known"
		}
		out.Bars = []InfoBar{{Label: "CPU", Detail: note}, {Label: "Memory", Detail: note}, {Label: "Pods", Detail: note}, {Label: "Disk", Detail: note}}
	} else {
		out.Bars = []InfoBar{
			capacityBar(cfg, "CPU", sumCapacity(cpus), formatCores, "cores"),
			capacityBar(cfg, "Memory", sumCapacity(mems), formatGiBFixed, "Gi"),
			podsBar(cfg, sum.pods, sum.podsAlloc),
			diskBar(cfg, anyDisk, sum.disk, diskNote(report)),
		}
	}
	for _, p := range report.pods {
		if node != "" && p.node != node {
			continue
		}
		if len(out.Top) == InfoTopN {
			break
		}
		out.Top = append(out.Top, InfoConsumer{Namespace: p.namespace, Name: p.name, CPU: formatMilliCPU(p.cpu), Memory: formatMemory(p.mem)})
	}
	if len(out.Top) == 0 {
		out.TopNote = topNote(report.info, node)
	}
	return out
}

// topNote says why no pod is listed by memory: pods cannot be read, the
// scope has no pods, or their usage is not measured.
func topNote(info overviewInfoInput, node string) string {
	if info.podsMissing != "" {
		return "pods cannot be read: " + info.podsMissing
	}
	pods := info.scopedPods
	if node != "" {
		pods = info.scopedPodsOnNode[node]
	}
	if pods == 0 {
		where := ""
		if info.namespace != AllNamespaces {
			where = " in " + info.namespace
		}
		if node != "" {
			where += " on node " + node
		}
		return "no pods" + where
	}
	reason := info.podsReason
	if reason == "" {
		reason = "no PodMetrics for these pods yet"
	}
	return "metrics unavailable: " + reason
}

// capacityBar shows CPU or memory on one base per line:
//
//	pods 2.3/10.4 Gi allocatable, 22%    PodMetrics of the pods on the nodes
//	node 6.0/14.9 Gi capacity, 40%       NodeMetrics, system daemons included
//	req 25%, lim 116% (overcommitted)    of allocatable, as kubectl describe node
//
// The bar shows the first line; without pod usage it shows the requests.
// Requests, limits and pod usage share allocatable as their base, so they
// compare with each other; node usage includes what runs outside pods and
// compares with capacity. Every percentage is truncated, as kubectl
// describe node prints it, and the states compare those printed numbers
// with the overview thresholds: requests full or high, limits
// overcommitted, pod usage full, under-req or high, node usage full or high.
func capacityBar(cfg appconfig.OverviewConfig, label string, a capacityAmounts, format func(int64) string, unit string) InfoBar {
	if a.alloc <= 0 {
		return InfoBar{Label: label, Detail: "no allocatable " + label}
	}
	j := judgeCapacity(cfg, a)
	bar := InfoBar{Label: label, Known: true}
	nodeLine, nodeHealth := "", j.node.sev.RowHealth()
	switch {
	case j.nodeKnown:
		nodeLine = fmt.Sprintf("node %s/%s %s capacity, %s%s", format(a.nodeUse), format(a.capacity), unit, percentText(j.nodePct), j.node.suffix())
	case a.hasNodeUse:
		nodeLine = fmt.Sprintf("node %s %s, capacity not reported", format(a.nodeUse), unit)
	default:
		nodeLine, nodeHealth = "node use n/a: "+a.nodeReason, RowHealthUnknown
	}
	limLine := fmt.Sprintf("lim %s%s", percentText(j.limPct), j.lim.suffix())
	if a.hasPodUse {
		bar.Percent, bar.Usage, bar.Flag, bar.Base = j.podPct, true, j.pod.flag, "allocatable"
		bar.Health = j.pod.sev.RowHealth()
		bar.Detail = fmt.Sprintf("pods %s/%s %s allocatable, %s%s%s", format(a.podUse), format(a.alloc), unit, percentText(j.podPct), j.pod.suffix(), a.podCoverage())
		bar.DetailHealth = bar.Health
		bar.More = []string{nodeLine, fmt.Sprintf("req %s%s, %s", percentText(j.reqPct), j.req.suffix(), limLine)}
		bar.MoreHealth = []RowHealth{nodeHealth, max(j.req.sev, j.lim.sev).RowHealth()}
		return withAttention(bar, j, "pods")
	}
	bar.Percent, bar.Flag = j.reqPct, j.req.flag
	bar.Health = j.req.sev.RowHealth()
	bar.Detail = fmt.Sprintf("req %s/%s %s allocatable, %s%s", format(a.req), format(a.alloc), unit, percentText(j.reqPct), j.req.suffix())
	bar.DetailHealth = bar.Health
	if !a.hasNodeUse && a.nodeReason == a.podReason {
		bar.More = []string{limLine, "use n/a: " + a.podReason}
		bar.MoreHealth = []RowHealth{j.lim.sev.RowHealth(), RowHealthUnknown}
	} else {
		bar.More = []string{limLine, "pods use n/a: " + a.podReason, nodeLine}
		bar.MoreHealth = []RowHealth{j.lim.sev.RowHealth(), RowHealthUnknown, nodeHealth}
	}
	return withAttention(bar, j, "req")
}

// withAttention adds the worst state of all lines to bar, and names the line
// that causes it when that is not the line the bar shows (shown).
func withAttention(bar InfoBar, j capacityJudgement, shown string) InfoBar {
	st, signal := j.worstSignal()
	bar.Attention = st.sev.RowHealth()
	if signal != "" && signal != shown {
		mark := "!"
		if st.sev == severityFailed {
			mark = "!!"
		}
		bar.AttentionText = mark + " " + signal
	}
	return bar
}

// formatCores prints millicores as cores with one decimal.
func formatCores(milli int64) string {
	return fmt.Sprintf("%.1f", float64(milli)/1000)
}

// formatGiBFixed prints bytes in GiB with one decimal at every size, so
// both sides of "6.0/10.4" have the same precision.
func formatGiBFixed(b int64) string {
	return fmt.Sprintf("%.1f", float64(b)/(1<<30))
}

func podsBar(cfg appconfig.OverviewConfig, pods, alloc int64) InfoBar {
	if alloc <= 0 {
		return InfoBar{Label: "Pods", Detail: "no allocatable pods"}
	}
	pct := truncPercent(pods, alloc)
	sev, flag := severityOK, "ok"
	if pct >= float64(cfg.PodsWarnPercent) {
		sev, flag = severityWarning, "high"
	}
	return InfoBar{Label: "Pods", Known: true, Percent: pct, Detail: fmt.Sprintf("%d/%d", pods, alloc), Flag: flag, Health: sev.RowHealth()}
}

// diskNote says why the report has no disk use.
func diskNote(report *overviewReport) string {
	if f, ok := report.finding("capacity/disk"); ok && f.unavailable != "" {
		return f.unavailable
	}
	return "no kubelet stats"
}

func diskBar(cfg appconfig.OverviewConfig, known bool, u fsUse, note string) InfoBar {
	if !known || !u.known() {
		return InfoBar{Label: "Disk", Detail: note}
	}
	pct := u.percent()
	sev, flag := severityOK, "ok"
	switch {
	case pct >= float64(cfg.DiskFailPercent):
		sev, flag = severityFailed, "full"
	case pct >= float64(cfg.DiskWarnPercent):
		sev, flag = severityWarning, "high"
	}
	return InfoBar{Label: "Disk", Known: true, Percent: pct, Detail: fmt.Sprintf("%s/%s Gi", formatGiB(u.used), formatGiB(u.capacity)), Flag: flag, Health: sev.RowHealth()}
}
