package models

import (
	"fmt"
	"sort"
	"time"

	"github.com/sttts/kc/pkg/appconfig"
	corev1 "k8s.io/api/core/v1"
)

// overviewNodeSummary is the load of one node, kept in the report for the
// Info panel mode.
type overviewNodeSummary struct {
	name                   string
	cpuUse, cpuReq, cpuLim int64 // millicores
	cpuAlloc               int64
	memUse, memReq, memLim int64 // bytes
	memAlloc               int64
	hasUse                 bool
	pods, podsAlloc        int64
	disk                   fsUse // node and separate image filesystem
	hasDisk                bool
}

// overviewPodUsage is the measured usage of one pod in the scope.
type overviewPodUsage struct {
	namespace, name, node string
	cpu, mem              int64
}

// summarizeNodes returns the load of every node, or nil when nodes or pods
// cannot be read.
func summarizeNodes(rc *ruleContext) []overviewNodeSummary {
	if rc.in.missingReason(ovNodesGVR, ovPodsGVR) != "" {
		return nil
	}
	loads := nodeLoads(rc)
	out := make([]overviewNodeSummary, 0, len(loads))
	for _, l := range loads {
		s := overviewNodeSummary{
			name:   l.node.Name,
			cpuUse: l.cpuUse, cpuReq: l.cpuReq, cpuLim: l.cpuLim, cpuAlloc: l.cpuAlloc,
			memUse: l.memUse, memReq: l.memReq, memLim: l.memLim, memAlloc: l.memAlloc,
			hasUse: l.hasUse, pods: l.pods, podsAlloc: l.podsAlloc,
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

// summarizePods returns the measured usage of the pods in scope that have
// not terminated, the most memory first.
func summarizePods(rc *ruleContext) []overviewPodUsage {
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
		u := overviewPodUsage{namespace: p.Namespace, name: p.Name, node: p.Spec.NodeName}
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
	// Percent fills the bar: measured usage of allocatable for CPU and
	// memory (requests when no usage is known), pods of allocatable pods,
	// used bytes of capacity for disk.
	Percent float64
	// Known is false when nothing can be shown; Detail then says why.
	Known bool
	// Detail is the text next to the bar.
	Detail string
	// Flag is the overview's state word: ok, high, full, under-req, ...
	Flag string
	// Health colors the bar like a row of an object list.
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
func NewOverviewInfo(deps Deps, namespace string) *OverviewInfo {
	return &OverviewInfo{engine: newOverviewEngine(deps, namespace)}
}

// Close stops the engine.
func (o *OverviewInfo) Close() { o.engine.close() }

// Summary returns the bars and top consumers of the whole scope, or of node
// when it is not empty. It counts as a read of the engine and does no I/O.
func (o *OverviewInfo) Summary(node string) InfoSummary {
	report, err := o.engine.snapshot()
	return infoSummary(report, err, o.engine.cfg, node)
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
	found, allUse, anyDisk := false, true, false
	for _, n := range report.nodes {
		if node != "" && n.name != node {
			continue
		}
		found = true
		sum.cpuUse, sum.cpuReq, sum.cpuLim, sum.cpuAlloc = sum.cpuUse+n.cpuUse, sum.cpuReq+n.cpuReq, sum.cpuLim+n.cpuLim, sum.cpuAlloc+n.cpuAlloc
		sum.memUse, sum.memReq, sum.memLim, sum.memAlloc = sum.memUse+n.memUse, sum.memReq+n.memReq, sum.memLim+n.memLim, sum.memAlloc+n.memAlloc
		sum.pods, sum.podsAlloc = sum.pods+n.pods, sum.podsAlloc+n.podsAlloc
		allUse = allUse && n.hasUse
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
		out.Bars = []InfoBar{
			resourceBar(cfg, "CPU", allUse, sum.cpuUse, sum.cpuReq, sum.cpuLim, sum.cpuAlloc, func(v int64) string { return fmt.Sprintf("%.1f", float64(v)/1000) }, "cores"),
			resourceBar(cfg, "Memory", allUse, sum.memUse, sum.memReq, sum.memLim, sum.memAlloc, formatGiB, "Gi"),
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
		out.TopNote = "no pod usage"
		if f, ok := report.finding("risks/pods-memory-limit"); ok && f.unavailable != "" {
			out.TopNote = "pod usage: " + f.unavailable
		}
	}
	return out
}

func resourceBar(cfg appconfig.OverviewConfig, label string, hasUse bool, use, req, lim, alloc int64, format func(int64) string, unit string) InfoBar {
	if alloc <= 0 {
		return InfoBar{Label: label, Detail: "no allocatable " + label}
	}
	usePct, reqPct, limPct := percent(use, alloc), percent(req, alloc), percent(lim, alloc)
	sev, flag := capacityFlag(cfg, reqPct, limPct, hasUse, usePct)
	bar := InfoBar{Label: label, Known: true, Flag: flag, Health: sev.RowHealth()}
	if hasUse {
		bar.Percent = usePct
		bar.Detail = fmt.Sprintf("use %s/%s %s, req %.0f%%, lim %.0f%%", format(use), format(alloc), unit, reqPct, limPct)
	} else {
		bar.Percent = reqPct
		bar.Detail = fmt.Sprintf("req %s/%s %s, lim %.0f%%, use n/a", format(req), format(alloc), unit, limPct)
	}
	return bar
}

func podsBar(cfg appconfig.OverviewConfig, pods, alloc int64) InfoBar {
	if alloc <= 0 {
		return InfoBar{Label: "Pods", Detail: "no allocatable pods"}
	}
	pct := percent(pods, alloc)
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
