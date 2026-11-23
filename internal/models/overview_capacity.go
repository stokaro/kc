package models

import (
	"fmt"
	"math"

	"github.com/stokaro/kc/pkg/appconfig"
)

// The CPU and memory capacity of a node, or of a sum of nodes, is judged in
// one place so the /overview rows, their node lists, F3 and the Info panel
// mode cannot drift apart (ADR 0174):
//
//	pods  PodMetrics of the pods bound to the node, every namespace, of allocatable
//	req   requests of those pods, of allocatable
//	lim   limits of those pods, of allocatable
//	node  NodeMetrics of the node, system daemons included, of capacity
//
// Percentages are truncated, as kubectl describe node prints them, and the
// states compare those printed numbers with the overview thresholds.

// capacityAmounts are the CPU (millicores) or memory (bytes) figures of one
// node or the sum of several.
type capacityAmounts struct {
	podUse, nodeUse, req, lim, alloc, capacity int64
	// hasPodUse is false when the pod usage of the node is not known: the
	// PodMetrics of every namespace cannot be read, or none of the node's
	// pods has PodMetrics. hasNodeUse is false without NodeMetrics for it.
	hasPodUse, hasNodeUse bool
	podReason, nodeReason string // why the usage is not known
	// podsMeasured of podsEligible running pods have PodMetrics. podUse
	// is their sum, so with fewer measured it is a lower bound, not a
	// complete figure.
	podsMeasured, podsEligible int
}

// podCoverage is ", 7 of 10 pods measured" when pod usage is known for some
// pods only, else "".
func (a capacityAmounts) podCoverage() string {
	if !a.hasPodUse || a.podsMeasured >= a.podsEligible {
		return ""
	}
	return fmt.Sprintf(", %d of %d pods measured", a.podsMeasured, a.podsEligible)
}

// sumCapacity adds up the amounts of several nodes. A usage is known only
// when it is known for every node; the reason is the first node's that is
// not. Without nodes nothing is known.
func sumCapacity(nodes []capacityAmounts) capacityAmounts {
	sum := capacityAmounts{hasPodUse: len(nodes) > 0, hasNodeUse: len(nodes) > 0}
	for _, n := range nodes {
		sum.podUse += n.podUse
		sum.nodeUse += n.nodeUse
		sum.req += n.req
		sum.lim += n.lim
		sum.alloc += n.alloc
		sum.capacity += n.capacity
		sum.podsMeasured += n.podsMeasured
		sum.podsEligible += n.podsEligible
		if !n.hasPodUse {
			sum.hasPodUse = false
			if sum.podReason == "" {
				sum.podReason = n.podReason
			}
		}
		if !n.hasNodeUse {
			sum.hasNodeUse = false
			if sum.nodeReason == "" {
				sum.nodeReason = n.nodeReason
			}
		}
	}
	return sum
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

// flagUnderRequested is the state of pods whose measured usage is well
// above what they request.
const flagUnderRequested = "under-req"

// capacityJudgement is the truncated percentages and the state of each line
// of a capacityAmounts.
type capacityJudgement struct {
	podPct, nodePct, reqPct, limPct float64
	pod, node, req, lim             infoState
	// nodeKnown is true when the node usage and its capacity are both known,
	// so nodePct means something.
	nodeKnown bool
}

// judgeCapacity classifies a with the overview thresholds: requests full or
// high, limits overcommitted, pod usage full, under-req or high, node usage
// full or high. Unknown usage is ok here; the text says n/a.
func judgeCapacity(cfg appconfig.OverviewConfig, a capacityAmounts) capacityJudgement {
	j := capacityJudgement{
		reqPct: truncPercent(a.req, a.alloc), limPct: truncPercent(a.lim, a.alloc),
		req: infoStateOK, lim: infoStateOK, pod: infoStateOK, node: infoStateOK,
	}
	switch {
	case j.reqPct >= float64(cfg.RequestsFailPercent):
		j.req = infoState{severityFailed, "full"}
	case j.reqPct >= float64(cfg.RequestsWarnPercent):
		j.req = infoState{severityWarning, "high"}
	}
	if j.limPct > float64(cfg.LimitsWarnPercent) {
		j.lim = infoState{severityWarning, "overcommitted"}
	}
	if a.hasPodUse {
		j.podPct = truncPercent(a.podUse, a.alloc)
		j.pod = usageState(cfg, j.podPct)
		if j.pod.sev != severityFailed && underRequested(cfg, j.podPct, j.reqPct) {
			j.pod = infoState{severityWarning, flagUnderRequested}
		}
	}
	if a.hasNodeUse && a.capacity > 0 {
		j.nodeKnown = true
		j.nodePct = truncPercent(a.nodeUse, a.capacity)
		j.node = usageState(cfg, j.nodePct)
	}
	return j
}

// worst is the state of the whole: the highest severity of the lines, named
// full for a failure and, among warnings, overcommitted before under-req
// before high.
func (j capacityJudgement) worst() infoState {
	sev := max(j.req.sev, j.lim.sev, j.pod.sev, j.node.sev)
	switch {
	case sev == severityOK:
		return infoStateOK
	case sev == severityFailed:
		return infoState{severityFailed, "full"}
	case j.lim.sev == sev:
		return j.lim
	case j.pod.sev == sev && j.pod.flag == flagUnderRequested:
		return j.pod
	}
	return infoState{sev, "high"}
}

// worstSignal is worst together with the line that causes it: pods, node,
// req or lim, or "" when every line is ok. Among lines in the same state the
// usage comes first: lim, pods, node, req.
func (j capacityJudgement) worstSignal() (infoState, string) {
	st := j.worst()
	if st.sev == severityOK {
		return st, ""
	}
	for _, line := range []struct {
		name  string
		state infoState
	}{{"lim", j.lim}, {"pods", j.pod}, {"node", j.node}, {"req", j.req}} {
		if line.state == st {
			return st, line.name
		}
	}
	return st, ""
}

// signalState names a state with its line: "lim overcommitted", "node full".
func signalState(st infoState, signal string) string {
	if signal == "" {
		return st.flag
	}
	return signal + " " + st.flag
}

// rank orders nodes by load: the largest of the percentages.
func (j capacityJudgement) rank() float64 {
	return max(j.reqPct, j.limPct, j.podPct, j.nodePct)
}

// overviewText is the value of an /overview capacity row and of each node
// under it: "pods 22% req 25% lim 116% of alloc, node 40% of cap".
func (j capacityJudgement) overviewText(a capacityAmounts) string {
	pods := "pods n/a"
	if a.hasPodUse {
		pods = "pods " + percentText(j.podPct)
		if a.podsMeasured < a.podsEligible {
			pods += fmt.Sprintf(" (%d/%d measured)", a.podsMeasured, a.podsEligible)
		}
	}
	node := "node n/a"
	switch {
	case j.nodeKnown:
		node = "node " + percentText(j.nodePct) + " of cap"
	case a.hasNodeUse:
		node = "node: no capacity"
	}
	return fmt.Sprintf("%s req %s lim %s of alloc, %s", pods, percentText(j.reqPct), percentText(j.limPct), node)
}

// underRequested reports whether the usage is at least underRequestedFactor
// times the requests and large enough to matter.
func underRequested(cfg appconfig.OverviewConfig, usePct, reqPct float64) bool {
	return usePct >= float64(cfg.UnderRequestedMinPercent) && usePct >= cfg.UnderRequestedFactor*reqPct
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
