package models

import (
	"fmt"
	"math"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/pkg/appconfig"
)

// overviewNodeSummary is the load of one node, kept in the report for the
// Info panel mode.
type overviewNodeSummary struct {
	name                   string
	cpuUse, cpuReq, cpuLim int64 // millicores
	cpuAlloc, cpuCap       int64
	memUse, memReq, memLim int64 // bytes
	memAlloc, memCap       int64
	hasUse                 bool // node usage from NodeMetrics
	// cpuPodUse and memPodUse sum the PodMetrics of the pods bound to the
	// node, in every namespace; hasPodUse is false when PodMetrics of
	// every namespace cannot be read.
	cpuPodUse, memPodUse int64
	hasPodUse            bool
	pods, podsAlloc      int64
	disk                 fsUse // node and separate image filesystem
	hasDisk              bool
}

// overviewInfoInput is what the Info mode reads from a recompute beyond the
// node and pod summaries: the scope, why usage is not known, and how many
// pods the scope has, so an empty scope is told from missing metrics.
type overviewInfoInput struct {
	namespace         string
	nodesReason       string // why node usage is not known
	podsReason        string // why the scope's pod usage is not known
	clusterPodsReason string // why the pod usage of every namespace is not known
	podsMissing       string // why pods cannot be read
	scopedPods        int    // pods of the scope that have not terminated
	scopedPodsOnNode  map[string]int
}

func infoInput(rc *ruleContext) overviewInfoInput {
	in := overviewInfoInput{
		namespace:         rc.in.namespace,
		nodesReason:       rc.m.nodesReason,
		podsReason:        rc.m.podsReason,
		clusterPodsReason: rc.m.clusterPodsReason,
		podsMissing:       rc.in.missingReason(ovPodsGVR),
		scopedPodsOnNode:  make(map[string]int),
	}
	if in.clusterPodsReason == "" && rc.m.clusterPods == nil {
		in.clusterPodsReason = "not measured"
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
	if rc.in.missingReason(ovNodesGVR, ovPodsGVR) != "" {
		return nil
	}
	loads := nodeLoads(rc)
	podUse := nodePodUsage(rc)
	out := make([]overviewNodeSummary, 0, len(loads))
	for _, l := range loads {
		s := overviewNodeSummary{
			name:   l.node.Name,
			cpuUse: l.cpuUse, cpuReq: l.cpuReq, cpuLim: l.cpuLim, cpuAlloc: l.cpuAlloc,
			memUse: l.memUse, memReq: l.memReq, memLim: l.memLim, memAlloc: l.memAlloc,
			cpuCap: l.node.Status.Capacity.Cpu().MilliValue(), memCap: l.node.Status.Capacity.Memory().Value(),
			hasUse: l.hasUse, pods: l.pods, podsAlloc: l.podsAlloc,
		}
		if podUse != nil {
			u := podUse[l.node.Name]
			s.cpuPodUse, s.memPodUse, s.hasPodUse = u.cpu, u.mem, true
		}
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

// nodePodUsage sums the measured usage of the pods bound to each node, in
// every namespace, the way kubectl top pods -A adds up: pods without a
// PodMetrics entry yet count as zero. It is nil when that usage is not known.
func nodePodUsage(rc *ruleContext) map[string]usageValues {
	if rc.m.clusterPodsReason != "" || rc.m.clusterPods == nil {
		return nil
	}
	out := make(map[string]usageValues)
	for i := range rc.in.pods {
		p := &rc.in.pods[i]
		if p.Spec.NodeName == "" || podTerminal(p) {
			continue
		}
		u := out[p.Spec.NodeName]
		for _, c := range rc.m.clusterPods[usageObjectKey(p.Namespace, p.Name)] {
			u.cpu += c.cpu
			u.mem += c.mem
		}
		out[p.Spec.NodeName] = u
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
	// Detail is the first line under the bar: what Percent shows.
	Detail string
	// More holds further lines under Detail.
	More []string
	// Flag is the state word of what Percent shows: ok, high, full,
	// under-req, ... A state of another line (overcommitted limits) is in
	// that line.
	Flag string
	// Health colors the bar like a row of an object list: the worst state
	// of all its lines.
	Health RowHealth
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
	// UsageAt is when CPU and memory usage were measured; zero when not.
	UsageAt time.Time
	Bars    []InfoBar
	// Top lists the pods with the most memory usage, at most TopN.
	Top []InfoConsumer
	// TopNote says why Top is empty.
	TopNote string
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
// Unlike /overview it also reads the PodMetrics of every namespace, so the
// pod usage of a node covers every pod on it whatever the scope.
func NewOverviewInfo(deps Deps, namespace string) *OverviewInfo {
	e := newOverviewEngine(deps, namespace)
	e.measure = func() *overviewMeasured {
		return measuredUsage(deps, namespace, true, e, func() { e.dirty.Store(true) })
	}
	return &OverviewInfo{engine: e}
}

// Close stops the engine.
func (o *OverviewInfo) Close() { o.engine.close() }

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
	out.UsageAt = report.usageAt
	var sum overviewNodeSummary
	found, allUse, allPodUse, anyDisk := false, true, true, false
	for _, n := range report.nodes {
		if node != "" && n.name != node {
			continue
		}
		found = true
		sum.cpuUse, sum.cpuReq, sum.cpuLim, sum.cpuAlloc, sum.cpuCap = sum.cpuUse+n.cpuUse, sum.cpuReq+n.cpuReq, sum.cpuLim+n.cpuLim, sum.cpuAlloc+n.cpuAlloc, sum.cpuCap+n.cpuCap
		sum.memUse, sum.memReq, sum.memLim, sum.memAlloc, sum.memCap = sum.memUse+n.memUse, sum.memReq+n.memReq, sum.memLim+n.memLim, sum.memAlloc+n.memAlloc, sum.memCap+n.memCap
		sum.cpuPodUse, sum.memPodUse = sum.cpuPodUse+n.cpuPodUse, sum.memPodUse+n.memPodUse
		sum.pods, sum.podsAlloc = sum.pods+n.pods, sum.podsAlloc+n.podsAlloc
		allUse = allUse && n.hasUse
		allPodUse = allPodUse && n.hasPodUse
		if n.hasDisk {
			anyDisk = true
			sum.disk.used += n.disk.used
			sum.disk.capacity += n.disk.capacity
		}
	}
	if !found {
		note := "nodes and pods cannot be read"
		if node != "" {
			note = "node " + node + " is not known"
		}
		out.Bars = []InfoBar{{Label: "CPU", Detail: note}, {Label: "Memory", Detail: note}, {Label: "Pods", Detail: note}, {Label: "Disk", Detail: note}}
	} else {
		info := report.info
		cpu := capacityAmounts{
			podUse: sum.cpuPodUse, nodeUse: sum.cpuUse, req: sum.cpuReq, lim: sum.cpuLim, alloc: sum.cpuAlloc, capacity: sum.cpuCap,
			hasPodUse: allPodUse, hasNodeUse: allUse, podReason: info.clusterPodsReason, nodeReason: info.nodesReason,
		}
		mem := cpu
		mem.podUse, mem.nodeUse, mem.req, mem.lim, mem.alloc, mem.capacity = sum.memPodUse, sum.memUse, sum.memReq, sum.memLim, sum.memAlloc, sum.memCap
		out.Bars = []InfoBar{
			capacityBar(cfg, "CPU", cpu, formatCores, "cores"),
			capacityBar(cfg, "Memory", mem, formatGiBFixed, "Gi"),
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

// capacityAmounts are the CPU or memory figures of one node or the sum of
// several.
type capacityAmounts struct {
	podUse, nodeUse, req, lim, alloc, capacity int64
	hasPodUse, hasNodeUse                      bool
	podReason, nodeReason                      string
}

// infoState is the severity and word of one line of a capacity bar.
type infoState struct {
	sev  overviewSeverity
	flag string
}

// suffix returns " (flag)", or "" when the line is ok.
func (s infoState) suffix() string {
	if s.flag == "" || s.flag == "ok" {
		return ""
	}
	return " (" + s.flag + ")"
}

var infoStateOK = infoState{sev: severityOK, flag: "ok"}

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
	reqPct, limPct, podPct := truncPercent(a.req, a.alloc), truncPercent(a.lim, a.alloc), truncPercent(a.podUse, a.alloc)
	reqState := infoStateOK
	switch {
	case reqPct >= float64(cfg.RequestsFailPercent):
		reqState = infoState{severityFailed, "full"}
	case reqPct >= float64(cfg.RequestsWarnPercent):
		reqState = infoState{severityWarning, "high"}
	}
	limState := infoStateOK
	if limPct > float64(cfg.LimitsWarnPercent) {
		limState = infoState{severityWarning, "overcommitted"}
	}

	bar := InfoBar{Label: label, Known: true}
	worst := max(reqState.sev, limState.sev)
	var nodeLine string
	nodeState := infoStateOK
	switch {
	case a.hasNodeUse && a.capacity > 0:
		nodePct := truncPercent(a.nodeUse, a.capacity)
		nodeState = usageState(cfg, nodePct)
		nodeLine = fmt.Sprintf("node %s/%s %s capacity, %s%s", format(a.nodeUse), format(a.capacity), unit, percentText(nodePct), nodeState.suffix())
	case a.hasNodeUse:
		nodeLine = fmt.Sprintf("node %s %s, capacity not reported", format(a.nodeUse), unit)
	default:
		nodeLine = "node use n/a: " + a.nodeReason
	}
	worst = max(worst, nodeState.sev)

	limLine := fmt.Sprintf("lim %s%s", percentText(limPct), limState.suffix())
	if a.hasPodUse {
		podState := usageState(cfg, podPct)
		if podState.sev != severityFailed && underRequested(cfg, podPct, reqPct) {
			podState = infoState{severityWarning, flagUnderRequested}
		}
		bar.Percent, bar.Usage, bar.Flag = podPct, true, podState.flag
		bar.Detail = fmt.Sprintf("pods %s/%s %s allocatable, %s%s", format(a.podUse), format(a.alloc), unit, percentText(podPct), podState.suffix())
		bar.More = []string{nodeLine, fmt.Sprintf("req %s%s, %s", percentText(reqPct), reqState.suffix(), limLine)}
		worst = max(worst, podState.sev)
	} else {
		bar.Percent, bar.Flag = reqPct, reqState.flag
		bar.Detail = fmt.Sprintf("req %s/%s %s allocatable, %s%s", format(a.req), format(a.alloc), unit, percentText(reqPct), reqState.suffix())
		if !a.hasNodeUse && a.nodeReason == a.podReason {
			bar.More = []string{limLine, "use n/a: " + a.podReason}
		} else {
			bar.More = []string{limLine, "pods use n/a: " + a.podReason, nodeLine}
		}
	}
	bar.Health = worst.RowHealth()
	return bar
}

// usageState classifies measured usage with the overview's usage
// thresholds.
func usageState(cfg appconfig.OverviewConfig, pct float64) infoState {
	switch {
	case pct >= float64(cfg.UsageFailPercent):
		return infoState{severityFailed, "full"}
	case pct >= float64(cfg.UsageWarnPercent):
		return infoState{severityWarning, "high"}
	}
	return infoStateOK
}

// truncPercent is v of of in whole percent, truncated as kubectl describe
// node prints it: 25.97% is 25%.
func truncPercent(v, of int64) float64 {
	return math.Floor(percent(v, of))
}

// percentText prints a whole percentage, truncated.
func percentText(pct float64) string {
	return fmt.Sprintf("%.0f%%", math.Floor(pct))
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
