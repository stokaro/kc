package models

import (
	"cmp"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// UsageFocus narrows UsageAmounts: to one pod, else one workload, else one
// node, else the whole scope.
type UsageFocus struct {
	Node     string
	Workload *InfoWorkload
	// PodNamespace and Pod name one pod.
	PodNamespace, Pod string
}

// ResourceAmount is one resource of UsageAmounts in its unit: cores for
// CPU, bytes for memory.
type ResourceAmount struct {
	// Used is the summed PodMetrics usage of the measured pods; UsedKnown
	// is false when no pod of the set is measured.
	Used      float64
	UsedKnown bool
	// Requests and Limits sum the pods that have not terminated.
	// LimitsPartial is true when one of them has no limit for the
	// resource, so Limits is no ceiling.
	Requests, Limits float64
	LimitsPartial    bool
	// Capacity is the allocatable of the nodes: of every node for the
	// all-namespaces scope, of the node for a node. Zero otherwise.
	Capacity float64
}

// UsageConsumer is one measured pod of UsageAmounts.
type UsageConsumer struct {
	Namespace, Name, Node string
	CPU, Memory           float64 // cores, bytes
}

// UsageAmounts is one Metrics API measurement of a scope, node, workload
// or pod, in numbers a chart can plot.
type UsageAmounts struct {
	// Loading is true before the first report; Err is why the engine
	// cannot run.
	Loading bool
	Err     string
	// At is when the usage was measured (metrics-server's timestamp, else
	// when kc read it); ReadAt is when kc read it, zero when no usage was
	// read. A new ReadAt is a new sample.
	At, ReadAt  time.Time
	CPU, Memory ResourceAmount
	// Measured of Running pods have PodMetrics; Used sums the measured
	// ones. Pods counts the pods that have not terminated.
	Measured, Running, Pods int
	// Note says why the usage is not known when it is not.
	Note string
	// Found is false when the focused object is not in the engine's lists
	// (deleted, or its kind cannot be read). UID and Created identify the
	// focused workload or pod; they are zero for a node or the scope.
	Found   bool
	UID     types.UID
	Created time.Time
	// Top lists the measured pods by memory, most first, at most the n
	// asked for.
	Top []UsageConsumer
}

// overviewPodAmount is what UsageAmounts needs of one pod that has not
// terminated.
type overviewPodAmount struct {
	namespace, name, node      string
	uid                        types.UID
	created                    time.Time
	owners                     []InfoWorkload
	running, measured          bool
	cpu, mem                   int64 // usage: millicores, bytes
	cpuReq, cpuLim             int64 // millicores
	memReq, memLim             int64 // bytes
	cpuUnlimited, memUnlimited bool
}

// objectStamp is the identity of a listed workload.
type objectStamp struct {
	uid     types.UID
	created time.Time
}

// overviewAmounts is the input of UsageAmounts, kept with each report.
type overviewAmounts struct {
	pods      []overviewPodAmount
	workloads map[InfoWorkload]objectStamp
}

func summarizeAmounts(rc *ruleContext, byPod map[string][]InfoWorkload) overviewAmounts {
	out := overviewAmounts{workloads: make(map[InfoWorkload]objectStamp)}
	if rc.in.missingReason(ovPodsGVR) != "" {
		return out
	}
	for gvr, objs := range newWorkloadIndex(rc).objects {
		for _, om := range objs {
			w := InfoWorkload{GVR: gvr, Namespace: om.Namespace, Name: om.Name}
			out.workloads[w] = objectStamp{uid: om.UID, created: om.CreationTimestamp.Time}
		}
	}
	rc.scopedPods(func(p *corev1.Pod) {
		if podTerminal(p) {
			return
		}
		key := usageObjectKey(p.Namespace, p.Name)
		req, lim := podRequestsAndLimits(p)
		a := overviewPodAmount{
			namespace: p.Namespace, name: p.Name, node: p.Spec.NodeName,
			uid: p.UID, created: p.CreationTimestamp.Time, owners: byPod[key],
			running: p.Status.Phase == corev1.PodRunning,
			cpuReq:  req.Cpu().MilliValue(), cpuLim: lim.Cpu().MilliValue(),
			memReq: req.Memory().Value(), memLim: lim.Memory().Value(),
			cpuUnlimited: podUnlimited(p, corev1.ResourceCPU),
			memUnlimited: podUnlimited(p, corev1.ResourceMemory),
		}
		if used, ok := rc.m.pods[key]; ok {
			a.measured = true
			for _, c := range used {
				a.cpu += c.cpu
				a.mem += c.mem
			}
		}
		out.pods = append(out.pods, a)
	})
	slices.SortStableFunc(out.pods, func(x, y overviewPodAmount) int {
		if c := cmp.Compare(y.mem, x.mem); c != 0 {
			return c
		}
		return cmp.Compare(x.namespace+"/"+x.name, y.namespace+"/"+y.name)
	})
	return out
}

// Amounts returns the usage, requests, limits and capacity of what focus
// names, with at most topN consumers. It counts as a read of the engine
// and does no I/O.
func (o *OverviewInfo) Amounts(focus UsageFocus, topN int) UsageAmounts {
	report, err := o.engine.snapshot()
	return usageAmounts(report, err, focus, topN)
}

func usageAmounts(report *overviewReport, err error, focus UsageFocus, topN int) UsageAmounts {
	var out UsageAmounts
	if report == nil {
		out.Loading = err == nil
		if err != nil {
			out.Err = err.Error()
		}
		return out
	}
	out.ReadAt = report.usageReadAt
	out.At = cmp.Or(report.usageAt, report.usageReadAt)
	if r := report.info.podsMissing; r != "" {
		out.Note = "pods cannot be read: " + r
		return out
	}
	in := focusPods(report, focus, &out)
	// Sum in millicores and bytes, then convert once: 100m and 200m make
	// exactly 0.3 cores.
	var cpuUse, cpuReq, cpuLim, memUse, memReq, memLim int64
	for i := range report.amounts.pods {
		p := &report.amounts.pods[i]
		if !in(p) {
			continue
		}
		out.Pods++
		cpuReq, cpuLim, memReq, memLim = cpuReq+p.cpuReq, cpuLim+p.cpuLim, memReq+p.memReq, memLim+p.memLim
		out.CPU.LimitsPartial = out.CPU.LimitsPartial || p.cpuUnlimited
		out.Memory.LimitsPartial = out.Memory.LimitsPartial || p.memUnlimited
		if p.running {
			out.Running++
		}
		if !p.measured {
			continue
		}
		if p.running {
			out.Measured++
		}
		cpuUse, memUse = cpuUse+p.cpu, memUse+p.mem
		out.CPU.UsedKnown, out.Memory.UsedKnown = true, true
		if len(out.Top) < topN {
			out.Top = append(out.Top, UsageConsumer{Namespace: p.namespace, Name: p.name, Node: p.node, CPU: cores(p.cpu), Memory: float64(p.mem)})
		}
	}
	out.CPU.Used, out.CPU.Requests, out.CPU.Limits = cores(cpuUse), cores(cpuReq), cores(cpuLim)
	out.Memory.Used, out.Memory.Requests, out.Memory.Limits = float64(memUse), float64(memReq), float64(memLim)
	if !out.CPU.UsedKnown {
		switch {
		case out.Pods == 0:
			out.Note = "no pods that have not terminated"
		case report.info.podsReason != "":
			out.Note = "metrics unavailable: " + report.info.podsReason
		default:
			out.Note = "no PodMetrics for these pods yet"
		}
	}
	return out
}

// focusPods is the filter of the pods in focus. It sets what out says
// about the focus itself: whether it was found, its identity and, for a
// node or all namespaces, the allocatable.
func focusPods(report *overviewReport, focus UsageFocus, out *UsageAmounts) func(*overviewPodAmount) bool {
	in := func(*overviewPodAmount) bool { return true }
	out.Found = true
	switch {
	case focus.Pod != "":
		out.Found = false
		in = func(p *overviewPodAmount) bool { return p.namespace == focus.PodNamespace && p.name == focus.Pod }
		for i := range report.amounts.pods {
			if p := &report.amounts.pods[i]; in(p) {
				out.Found, out.UID, out.Created = true, p.uid, p.created
			}
		}
	case focus.Workload != nil:
		w := *focus.Workload
		in = func(p *overviewPodAmount) bool { return slices.Contains(p.owners, w) }
		stamp, ok := report.amounts.workloads[w]
		out.Found, out.UID, out.Created = ok, stamp.uid, stamp.created
	case focus.Node != "":
		in = func(p *overviewPodAmount) bool { return p.node == focus.Node }
		out.Found = false
		for _, n := range report.nodes {
			if n.name == focus.Node {
				out.Found = true
				out.CPU.Capacity, out.Memory.Capacity = cores(n.cpu.alloc), float64(n.mem.alloc)
			}
		}
	case report.info.namespace == AllNamespaces:
		var cpu, mem int64
		for _, n := range report.nodes {
			cpu, mem = cpu+n.cpu.alloc, mem+n.mem.alloc
		}
		out.CPU.Capacity, out.Memory.Capacity = cores(cpu), float64(mem)
	}
	return in
}

// cores converts millicores.
func cores(milli int64) float64 { return float64(milli) / 1000 }
