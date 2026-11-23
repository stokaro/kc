package models

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/stokaro/kc/pkg/appconfig"
)

// overviewSeverity orders findings: a row takes the worst severity of the
// objects behind it.
type overviewSeverity int

const (
	severityUnknown overviewSeverity = iota // the source is not available
	severityOK
	severityWarning
	severityFailed
)

func (s overviewSeverity) String() string {
	switch s {
	case severityOK:
		return "ok"
	case severityWarning:
		return "warning"
	case severityFailed:
		return "failed"
	}
	return "n/a"
}

// Overview sections, in display order.
const (
	overviewCapacity = "Capacity"
	overviewHealth   = "Health"
	overviewRisks    = "Risks"
	overviewActivity = "Activity"
)

var overviewSections = []string{overviewCapacity, overviewHealth, overviewRisks, overviewActivity}

// overviewTarget is one object behind a finding.
type overviewTarget struct {
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	value     string // why the object is listed
	severity  overviewSeverity
	// rank orders targets of the same severity: higher first, then by
	// namespace and name.
	rank float64
	// involved is the object an event is about; Enter on the event opens it.
	involved *corev1.ObjectReference
	// uid, since and starting are the target's lifecycle
	// (overview_lifecycle.go): its UID, when the server dated its state,
	// and whether it is inside the startup grace.
	uid      types.UID
	since    time.Time
	starting bool
}

// overviewFinding is one row of the overview.
type overviewFinding struct {
	id       string // stable row ID, "<section>/<rule>"
	section  string
	title    string // "/pods crash looping"
	value    string
	state    string // severity word or a flag such as "overcommitted"
	severity overviewSeverity
	// unavailable says why the value cannot be computed; the row is dim.
	unavailable string
	// partial says which of a row's sources cannot be read while the value
	// is computed from the others (ADR 0182); partialGVRs names them.
	partial     string
	partialGVRs []schema.GroupVersionResource
	explain     string     // how the value is computed (F3)
	kubectl     [][]string // kubectl arguments that show the inputs (F3)
	// supplies says what each kubectl command gives the calculation; it is
	// parallel to kubectl and required when there is more than one.
	// Arguments may hold placeholders that a target resolves (<node>,
	// <workload>); see overview_commands.go.
	supplies []string
	targets  []overviewTarget

	// notApplicable says why there is nothing to check (no objects of the
	// kind in scope, an add-on not installed). It is not a pass.
	notApplicable string
}

// overviewReport is the result of one recompute.
type overviewReport struct {
	at      time.Time
	usageAt time.Time // when the usage was measured; zero when unknown
	// usageWindow and usageReadAt go with usageAt; see overviewMeasured.
	usageWindow time.Duration
	usageReadAt time.Time
	// podsTiming dates the pod usage of the scope, which the workload
	// summaries are made of; see overviewMeasured.
	podsTiming podsTiming
	findings   []overviewFinding
	// nodes, pods and workloads feed the Info panel mode.
	nodes     []overviewNodeSummary
	pods      []overviewPodUsage
	workloads map[InfoWorkload]*overviewWorkloadSummary
	// workloadsReason says why no pod can be grouped (pods cannot be
	// read); replicaSetsReason why pods cannot be grouped by Deployment.
	workloadsReason, replicaSetsReason string
	// info is what the Info mode needs beyond the summaries.
	info overviewInfoInput
}

// finding returns the finding with id, if any.
func (r *overviewReport) finding(id string) (overviewFinding, bool) {
	if r == nil {
		return overviewFinding{}, false
	}
	for _, f := range r.findings {
		if f.id == id {
			return f, true
		}
	}
	return overviewFinding{}, false
}

// computeOverview applies every rule to in. It only reads in and m; a nil m
// means nothing was measured.
func computeOverview(in *overviewInput, cfg appconfig.OverviewConfig, now time.Time, m *overviewMeasured) *overviewReport {
	return computeOverviewWith(in, cfg, now, m, nil)
}

// computeOverviewWith is computeOverview with what the session saw in the
// previous report, for the rules with hysteresis. A nil life knows nothing.
func computeOverviewWith(in *overviewInput, cfg appconfig.OverviewConfig, now time.Time, m *overviewMeasured, life *overviewLifecycle) *overviewReport {
	cfg = cfg.Normalized()
	if m == nil {
		m = &overviewMeasured{nodesReason: "not measured", podsReason: "not measured", clusterPodsReason: "not measured", statsReason: "not measured"}
	}
	rc := ruleContext{in: in, cfg: cfg, now: now, m: m, life: life}
	report := &overviewReport{at: now, usageAt: m.usageAt, usageWindow: m.usageWindow, usageReadAt: m.usageReadAt, podsTiming: m.podsTiming}
	for _, rule := range overviewRules {
		report.findings = append(report.findings, rule(&rc))
	}
	addGraceNotes(&rc, report.findings)
	report.nodes = summarizeNodes(&rc)
	var byPod map[string][]InfoWorkload
	report.workloads, byPod = summarizeWorkloads(&rc)
	report.workloadsReason = rc.in.missingReason(ovPodsGVR)
	report.replicaSetsReason = rc.in.missingReason(ovReplicaSetsGVR)
	report.pods = summarizePods(&rc, byPod)
	report.info = infoInput(&rc)
	return report
}

type ruleContext struct {
	in  *overviewInput
	cfg appconfig.OverviewConfig
	now time.Time
	m   *overviewMeasured
	// life is the session's view of the previous report; nil in tests
	// that compute one report.
	life *overviewLifecycle
}

// within reports whether t lies inside the configured window before now.
func (rc *ruleContext) within(t time.Time) bool {
	return !t.IsZero() && rc.now.Sub(t) <= rc.cfg.Window.Duration
}

// windowLabel prints the window the way it is configured: 1h, 30m, 90s.
func (rc *ruleContext) windowLabel() string {
	d := rc.cfg.Window.Duration
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// nsArgs returns the namespace arguments for kubectl in the overview scope.
func (rc *ruleContext) nsArgs() []string {
	return namespaceArgs(rc.in.namespace, true)
}

type overviewRule func(*ruleContext) overviewFinding

var overviewRules = []overviewRule{
	capacityCPURule,
	capacityMemoryRule,
	capacityPodsRule,
	capacityDiskRule,
	nodesNotReadyRule,
	nodesPressureRule,
	nodesCordonedRule,
	podsNotReadyRule,
	podsPendingRule,
	podsWaitingRule("health/pods-crashloop", "/pods crash looping", severityFailed, "CrashLoopBackOff"),
	podsWaitingRule("health/pods-image", "/pods image pull errors", severityFailed, "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ErrImageNeverPull"),
	podsWaitingRule("health/pods-config", "/pods config errors", severityFailed, "CreateContainerConfigError", "CreateContainerError", "RunContainerError"),
	podsOOMKilledRule,
	podsRestartingRule,
	workloadsUnavailableRule,
	workloadsNotUpdatedRule,
	jobsFailedRule,
	cronJobsLastRunFailedRule,
	cronJobsSuspendedRule,
	pvcsRule,
	pvsRule,
	pdbsBlockingRule,
	podsBestEffortRule,
	podsNoMemoryLimitRule,
	podsNearMemoryLimitRule,
	podsAtCPULimitRule,
	volumesNearFullRule,
	singleReplicaRule,
	quotasRule,
	hpasRule,
	certificatesRule,
	warningEventsRule,
	podsRestartedRule,
}

// countFinding builds a row whose value is the number of targets and whose
// severity is the worst target's.
func countFinding(rc *ruleContext, f overviewFinding, needs []schema.GroupVersionResource, targets func() []overviewTarget) overviewFinding {
	if reason := optionalNotServed(rc.in, needs); reason != "" {
		return notApplicable(f, reason)
	}
	if reason := rc.in.missingReason(needs...); reason != "" {
		f.unavailable = reason
		f.value = "n/a"
		f.severity = severityUnknown
		f.state = severityUnknown.String()
		return f
	}
	if reason := noSubjectsReason(rc.in, needs); reason != "" {
		return notApplicable(f, reason)
	}
	f.targets = sortTargets(targets())
	f.value = fmt.Sprintf("%d", len(f.targets))
	f.severity = severityOK
	for _, t := range f.targets {
		if t.severity > f.severity {
			f.severity = t.severity
		}
	}
	f.state = f.severity.String()
	if f.severity == severityOK && anyStarting(f.targets) {
		f.state = "starting"
	}
	return f
}

// partialCountFinding is countFinding for a row whose kinds each add
// targets of their own, such as Deployments, StatefulSets and DaemonSets:
// it counts the kinds that can be read and says which cannot. needs are
// required as in countFinding; the row is n/a only when no kind of anyOf
// can be read.
func partialCountFinding(rc *ruleContext, f overviewFinding, needs, anyOf []schema.GroupVersionResource, targets func() []overviewTarget) overviewFinding {
	var reasons []string
	var missing, readable []schema.GroupVersionResource
	for _, gvr := range anyOf {
		if reason := rc.in.missingReason(gvr); reason != "" {
			reasons = append(reasons, reason)
			missing = append(missing, gvr)
		} else {
			readable = append(readable, gvr)
		}
	}
	if len(missing) == len(anyOf) {
		return countFinding(rc, f, append(slices.Clone(needs), anyOf...), targets)
	}
	f = countFinding(rc, f, append(slices.Clone(needs), readable...), targets)
	if len(missing) > 0 && f.unavailable == "" {
		// No object of the readable kinds is no "nothing to check": the
		// kinds that cannot be read may hold some (ADR 0172).
		stillApplicable(&f)
		f.partial, f.partialGVRs = strings.Join(reasons, "; "), missing
		f.value += " (partial)"
	}
	return f
}

func sortTargets(ts []overviewTarget) []overviewTarget {
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].severity != ts[j].severity {
			return ts[i].severity > ts[j].severity
		}
		if ts[i].rank != ts[j].rank {
			return ts[i].rank > ts[j].rank
		}
		if ts[i].namespace != ts[j].namespace {
			return ts[i].namespace < ts[j].namespace
		}
		if ts[i].name != ts[j].name {
			return ts[i].name < ts[j].name
		}
		return ts[i].gvr.String() < ts[j].gvr.String()
	})
	return ts
}

func objTarget(gvr schema.GroupVersionResource, m metav1.ObjectMeta, value string, sev overviewSeverity) overviewTarget {
	return overviewTarget{gvr: gvr, namespace: m.Namespace, name: m.Name, value: value, severity: sev, uid: m.UID}
}

func kubectlGet(resource string, extra ...string) []string {
	return append([]string{"get", resource}, extra...)
}

// --- Capacity ---------------------------------------------------------------

// nodeLoad is what the pods on one node ask for and use.
type nodeLoad struct {
	node            *corev1.Node
	cpu, mem        capacityAmounts // millicores, bytes
	pods, podsAlloc int64
}

// nodeLoads sums the requests and limits of the pods bound to each node, as
// kubectl describe node does: terminated pods do not count, init
// containers and overhead count the way the scheduler counts them. It adds
// the node's usage (NodeMetrics) and the usage of those pods (PodMetrics of
// every namespace) the way kubectl top pods -A adds up: a pod without
// PodMetrics yet counts as zero, but a node none of whose pods is measured
// has no pod usage, so a node that metrics-server does not reach is not
// shown as idle.
func nodeLoads(rc *ruleContext) []*nodeLoad {
	byName := make(map[string]*nodeLoad, len(rc.in.nodes))
	loads := make([]*nodeLoad, 0, len(rc.in.nodes))
	for i := range rc.in.nodes {
		n := &rc.in.nodes[i]
		l := &nodeLoad{
			node:      n,
			cpu:       capacityAmounts{alloc: n.Status.Allocatable.Cpu().MilliValue(), capacity: n.Status.Capacity.Cpu().MilliValue()},
			mem:       capacityAmounts{alloc: n.Status.Allocatable.Memory().Value(), capacity: n.Status.Capacity.Memory().Value()},
			podsAlloc: n.Status.Allocatable.Pods().Value(),
		}
		byName[n.Name] = l
		loads = append(loads, l)
	}
	podsReason := clusterPodsReason(rc.m)
	measured := make(map[string]int, len(loads))
	for i := range rc.in.pods {
		p := &rc.in.pods[i]
		if p.Spec.NodeName == "" || podTerminal(p) {
			continue
		}
		l, ok := byName[p.Spec.NodeName]
		if !ok {
			continue
		}
		req, lim := podRequestsAndLimits(p)
		l.cpu.req += req.Cpu().MilliValue()
		l.mem.req += req.Memory().Value()
		l.cpu.lim += lim.Cpu().MilliValue()
		l.mem.lim += lim.Memory().Value()
		l.pods++
		if used, ok := rc.m.clusterPods[usageObjectKey(p.Namespace, p.Name)]; ok && podsReason == "" {
			measured[p.Spec.NodeName]++
			for _, c := range used {
				l.cpu.podUse += c.cpu
				l.mem.podUse += c.mem
			}
		}
	}
	for _, l := range loads {
		name := l.node.Name
		l.cpu.podsMeasured, l.cpu.podsEligible = measured[name], int(l.pods)
		l.mem.podsMeasured, l.mem.podsEligible = measured[name], int(l.pods)
		switch {
		case podsReason != "":
			l.cpu.podReason, l.mem.podReason = podsReason, podsReason
		case l.pods > 0 && measured[name] == 0:
			reason := "no PodMetrics for the pods on node " + name + " yet"
			l.cpu.podReason, l.mem.podReason = reason, reason
		default:
			l.cpu.hasPodUse, l.mem.hasPodUse = true, true
		}
		u, found := rc.m.nodes[name]
		switch {
		case rc.m.nodesReason != "":
			l.cpu.nodeReason, l.mem.nodeReason = rc.m.nodesReason, rc.m.nodesReason
		case !found:
			reason := "no NodeMetrics for node " + name + " yet"
			l.cpu.nodeReason, l.mem.nodeReason = reason, reason
		default:
			l.cpu.nodeUse, l.mem.nodeUse = u.cpu, u.mem
			l.cpu.hasNodeUse, l.mem.hasNodeUse = true, true
		}
	}
	return loads
}

func percent(v, of int64) float64 {
	if of <= 0 {
		return 0
	}
	return float64(v) * 100 / float64(of)
}

// clusterPodsReason says why the pod usage of every namespace is not known,
// or "" when it is.
func clusterPodsReason(m *overviewMeasured) string {
	if m.clusterPodsReason == "" && m.clusterPods == nil {
		return "not measured"
	}
	return m.clusterPodsReason
}

// capacityUsageNote is what F3 adds when node or pod usage is not known.
func capacityUsageNote(m *overviewMeasured) string {
	pods, nodes := clusterPodsReason(m), m.nodesReason
	switch {
	case nodes == pods:
		return usageNote(pods)
	case nodes == "":
		return "\n\nPod usage is not available: " + pods + "."
	case pods == "":
		return "\n\nNode usage is not available: " + nodes + "."
	}
	return "\n\nPod usage is not available: " + pods + ".\nNode usage is not available: " + nodes + "."
}

func capacityResourceRule(rc *ruleContext, id, title, resource string, pick func(*nodeLoad) capacityAmounts) overviewFinding {
	f := overviewFinding{
		id: id, section: overviewCapacity, title: title,
		explain: fmt.Sprintf(`Each node is judged on four figures, and this row adds them up over the
nodes. It always covers every node and every pod on it, whatever the
namespace scope. The Info panel mode shows the same figures.

  pods  measured %[1]s usage of the pods bound to the node that have not
        terminated, in every namespace (metrics.k8s.io PodMetrics; memory
        is the working set), of status.allocatable.%[1]s. A pod without
        PodMetrics yet counts as zero, as in kubectl top pods; a node none
        of whose pods is measured is n/a.
  req   %[1]s requests of those pods, counted the way the scheduler counts
        them (the larger of the init containers and the sum of the app
        containers, plus sidecars and pod overhead), of
        status.allocatable.%[1]s, as kubectl describe node prints them.
  lim   %[1]s limits of those pods, counted the same way, of allocatable.
  node  measured %[1]s usage of the node itself (metrics.k8s.io NodeMetrics:
        pods, system daemons, kubelet and runtime), of
        status.capacity.%[1]s. kubectl top nodes prints the same usage
        against allocatable instead.

Usage is read every 15 seconds while the overview is open. The total is
n/a when it is not known for every node. Percentages are truncated, as
kubectl describe node prints them, and the states compare the printed
numbers.

A node is "full" when its requests reach requestsFailPercent (%[2]d%%), or
its pods or the node reach usageFailPercent (%[5]d%%); "overcommitted" when
its limits exceed limitsWarnPercent (%[3]d%%); "under-req" when its pods
use at least underRequestedFactor (%.1[6]f) times what they request and at
least underRequestedMinPercent (%[7]d%%), so the scheduler thinks the node
is emptier than it is; "high" when its pods or the node reach
usageWarnPercent (%[8]d%%) or its requests reach requestsWarnPercent
(%[4]d%%). What runs outside pods counts only on the node line, so it
cannot make the pods look under-requested.

The value adds up the nodes; the state is that of the worst node, with the
line that causes it and how many nodes share it ("node full, 1 of 2
nodes"), so a healthy sum can carry the state of one node. Enter lists the
nodes, the worst first, each with its own figures and state.`, resource, rc.cfg.RequestsFailPercent, rc.cfg.LimitsWarnPercent, rc.cfg.RequestsWarnPercent,
			rc.cfg.UsageFailPercent, rc.cfg.UnderRequestedFactor, rc.cfg.UnderRequestedMinPercent, rc.cfg.UsageWarnPercent) + capacityUsageNote(rc.m),
		kubectl: [][]string{
			{"describe", "nodes"},
			{"top", "pods", "-A"},
			{"get", "pods", "-A", "--field-selector", "status.phase!=Succeeded,status.phase!=Failed", "-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,NODE:.spec.nodeName"},
			{"top", "nodes"},
			{"get", "nodes", "-o", "custom-columns=NAME:.metadata.name,CAPACITY:.status.capacity." + resource + ",ALLOCATABLE:.status.allocatable." + resource},
		},
		supplies: []string{
			"req and lim: the requests and limits of the pods on each node, against allocatable",
			"pods: the measured usage of every pod, in every namespace (a recent window, not this instant)",
			"pods: the node each pod that has not terminated is bound to",
			"node: the measured usage of each node (kubectl top prints it against allocatable, kc against capacity)",
			"the capacity and allocatable of each node",
		},
	}
	if reason := rc.in.clusterWideReason(ovNodesGVR, ovPodsGVR); reason != "" {
		f.unavailable, f.value, f.state = reason, "n/a", severityUnknown.String()
		return f
	}
	loads := nodeLoads(rc)
	all := make([]capacityAmounts, 0, len(loads))
	states := make(map[string]string, len(loads)) // node name: "lim overcommitted"
	for _, l := range loads {
		a := pick(l)
		all = append(all, a)
		j := judgeCapacity(rc.cfg, a)
		st, signal := j.worstSignal()
		value := j.overviewText(a)
		if st.sev != severityOK {
			states[l.node.Name] = signalState(st, signal)
			value += " " + states[l.node.Name]
		}
		f.targets = append(f.targets, overviewTarget{
			gvr: ovNodesGVR, name: l.node.Name, severity: st.sev, value: value, rank: j.rank(),
		})
	}
	f.targets = sortTargets(f.targets)
	total := sumCapacity(all)
	f.value = judgeCapacity(rc.cfg, total).overviewText(total)
	f.severity, f.state = capacityRowState(f.targets, states)
	return f
}

// capacityRowState is the state of a capacity row: the state of its first
// node (the targets are sorted, the worst first), the line that causes it
// and how many nodes are in that same state, "node full, 1 of 2 nodes".
// The row's value is the sum over the nodes, which can look healthy while
// one node is not; the state says that it comes from nodes, and Enter
// lists them in this order.
func capacityRowState(targets []overviewTarget, states map[string]string) (overviewSeverity, string) {
	if len(targets) == 0 || targets[0].severity == severityOK {
		return severityOK, "ok"
	}
	first := states[targets[0].name]
	n := 0
	for _, t := range targets {
		if t.severity == targets[0].severity && states[t.name] == first {
			n++
		}
	}
	if len(targets) == 1 {
		return targets[0].severity, first
	}
	return targets[0].severity, fmt.Sprintf("%s, %d of %d nodes", first, n, len(targets))
}

func flagSuffix(flag string) string {
	if flag == "ok" {
		return ""
	}
	return " " + flag
}

func capacityCPURule(rc *ruleContext) overviewFinding {
	return capacityResourceRule(rc, "capacity/cpu", "/nodes cpu", "cpu", func(l *nodeLoad) capacityAmounts { return l.cpu })
}

func capacityMemoryRule(rc *ruleContext) overviewFinding {
	return capacityResourceRule(rc, "capacity/memory", "/nodes memory", "memory", func(l *nodeLoad) capacityAmounts { return l.mem })
}

func capacityPodsRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "capacity/pods", section: overviewCapacity, title: "/nodes pods",
		explain: fmt.Sprintf(`The pods bound to each node that have not terminated, against the node's
status.allocatable.pods, summed over all nodes. A node is "high" when it
reaches podsWarnPercent (%d%%). Enter lists the nodes, the fullest first.`, rc.cfg.PodsWarnPercent),
		kubectl: [][]string{
			{"get", "pods", "-A", "--field-selector", "status.phase!=Succeeded,status.phase!=Failed", "-o", "custom-columns=NODE:.spec.nodeName", "--no-headers"},
			{"get", "nodes", "-o", "custom-columns=NAME:.metadata.name,PODS:.status.allocatable.pods"},
		},
		supplies: []string{
			"the node of each pod that has not terminated, in every namespace",
			"the allocatable pods of each node",
		},
	}
	if reason := rc.in.clusterWideReason(ovNodesGVR, ovPodsGVR); reason != "" {
		f.unavailable, f.value, f.state = reason, "n/a", severityUnknown.String()
		return f
	}
	var pods, alloc int64
	worst, worstFlag := severityOK, "ok"
	for _, l := range nodeLoads(rc) {
		pods, alloc = pods+l.pods, alloc+l.podsAlloc
		pct := percent(l.pods, l.podsAlloc)
		sev, flag := severityOK, "ok"
		if pct >= float64(rc.cfg.PodsWarnPercent) {
			sev, flag = severityWarning, "high"
		}
		if sev > worst {
			worst, worstFlag = sev, flag
		}
		f.targets = append(f.targets, overviewTarget{
			gvr: ovNodesGVR, name: l.node.Name, severity: sev, rank: pct,
			value: fmt.Sprintf("%d/%d (%.0f%%)%s", l.pods, l.podsAlloc, pct, flagSuffix(flag)),
		})
	}
	f.targets = sortTargets(f.targets)
	f.value = fmt.Sprintf("%d/%d", pods, alloc)
	f.severity, f.state = worst, worstFlag
	return f
}

// --- Health: nodes ----------------------------------------------------------

func nodeCondition(n *corev1.Node, t corev1.NodeConditionType) *corev1.NodeCondition {
	for i := range n.Status.Conditions {
		if n.Status.Conditions[i].Type == t {
			return &n.Status.Conditions[i]
		}
	}
	return nil
}

func nodesNotReadyRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/nodes-not-ready", section: overviewHealth, title: "/nodes not ready",
		explain: `Nodes whose Ready condition is not True (False, Unknown or missing).
Nodes are cluster-scoped, so this row covers every node in any namespace scope.`,
		kubectl: [][]string{kubectlGet("nodes")},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovNodesGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.nodes {
			n := &rc.in.nodes[i]
			c := nodeCondition(n, corev1.NodeReady)
			if c != nil && c.Status == corev1.ConditionTrue {
				continue
			}
			value := "Ready missing"
			if c != nil {
				value = fmt.Sprintf("Ready=%s %s", c.Status, c.Reason)
			}
			out = append(out, objTarget(ovNodesGVR, n.ObjectMeta, strings.TrimSpace(value), severityFailed))
		}
		return out
	})
}

func nodesPressureRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/nodes-pressure", section: overviewHealth, title: "/nodes under pressure",
		explain: `Nodes with MemoryPressure, DiskPressure, PIDPressure or NetworkUnavailable
set to True. The kubelet evicts pods from a node under pressure.`,
		kubectl: [][]string{{"describe", "nodes"}},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovNodesGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.nodes {
			n := &rc.in.nodes[i]
			var set []string
			for _, t := range []corev1.NodeConditionType{corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable} {
				if c := nodeCondition(n, t); c != nil && c.Status == corev1.ConditionTrue {
					set = append(set, string(t))
				}
			}
			if len(set) > 0 {
				out = append(out, objTarget(ovNodesGVR, n.ObjectMeta, strings.Join(set, ", "), severityWarning))
			}
		}
		return out
	})
}

func nodesCordonedRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/nodes-cordoned", section: overviewHealth, title: "/nodes cordoned",
		explain: `Nodes with spec.unschedulable set (kubectl cordon). New pods are not
scheduled there.`,
		kubectl: [][]string{kubectlGet("nodes", "--field-selector", "spec.unschedulable=true")},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovNodesGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.nodes {
			n := &rc.in.nodes[i]
			if n.Spec.Unschedulable {
				out = append(out, objTarget(ovNodesGVR, n.ObjectMeta, "SchedulingDisabled", severityWarning))
			}
		}
		return out
	})
}

// --- Health: pods -----------------------------------------------------------

func podTerminal(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

func podCondition(p *corev1.Pod, t corev1.PodConditionType) *corev1.PodCondition {
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == t {
			return &p.Status.Conditions[i]
		}
	}
	return nil
}

// podStatuses returns the init and app container statuses of p.
func podStatuses(p *corev1.Pod) []corev1.ContainerStatus {
	out := make([]corev1.ContainerStatus, 0, len(p.Status.InitContainerStatuses)+len(p.Status.ContainerStatuses))
	out = append(out, p.Status.InitContainerStatuses...)
	return append(out, p.Status.ContainerStatuses...)
}

// scopedPods calls fn for every pod in the namespace scope.
func (rc *ruleContext) scopedPods(fn func(*corev1.Pod)) {
	for i := range rc.in.pods {
		p := &rc.in.pods[i]
		if rc.in.inScope(p.Namespace) {
			fn(p)
		}
	}
}

func podsNotReadyRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/pods-not-ready", section: overviewHealth, title: "/pods not ready",
		explain: `Running pods whose Ready condition is not True. Services do not send them
traffic.`,
		kubectl: [][]string{kubectlGet("pods", append(rc.nsArgs(), "--field-selector", "status.phase=Running")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
		var out []overviewTarget
		rc.scopedPods(func(p *corev1.Pod) {
			if p.Status.Phase != corev1.PodRunning {
				return
			}
			if c := podCondition(p, corev1.PodReady); c != nil && c.Status == corev1.ConditionTrue {
				return
			}
			ready := 0
			for _, s := range p.Status.ContainerStatuses {
				if s.Ready {
					ready++
				}
			}
			since, explicit := rc.podNotReadySince(p)
			out = append(out, rc.graced(objTarget(ovPodsGVR, p.ObjectMeta, fmt.Sprintf("%d/%d ready", ready, len(p.Spec.Containers)), severityWarning), since, explicit))
		})
		return out
	})
}

func podsPendingRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/pods-pending", section: overviewHealth, title: "/pods pending",
		explain: `Pods in phase Pending. The value shows the PodScheduled=False reason
(Unschedulable fails the row) or the reason a container is waiting.`,
		kubectl: [][]string{kubectlGet("pods", append(rc.nsArgs(), "--field-selector", "status.phase=Pending")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
		var out []overviewTarget
		rc.scopedPods(func(p *corev1.Pod) {
			if p.Status.Phase != corev1.PodPending {
				return
			}
			value, sev, explicit := "Pending", severityWarning, false
			if c := podCondition(p, corev1.PodScheduled); c != nil && c.Status == corev1.ConditionFalse {
				value = c.Reason
				if c.Reason == corev1.PodReasonUnschedulable {
					sev = severityFailed
				}
			} else {
				for _, s := range podStatuses(p) {
					if s.State.Waiting != nil && s.State.Waiting.Reason != "" {
						value = fmt.Sprintf("%s (%s)", s.State.Waiting.Reason, s.Name)
						explicit = waitingFailure(s.State.Waiting.Reason)
						break
					}
				}
			}
			out = append(out, rc.graced(objTarget(ovPodsGVR, p.ObjectMeta, value, sev), p.CreationTimestamp.Time, explicit || sev == severityFailed))
		})
		return out
	})
}

// podsWaitingRule lists pods with a container waiting for one of reasons.
func podsWaitingRule(id, title string, sev overviewSeverity, reasons ...string) overviewRule {
	return func(rc *ruleContext) overviewFinding {
		f := overviewFinding{
			id: id, section: overviewHealth, title: title,
			explain: fmt.Sprintf(`Pods with an init or app container whose state.waiting.reason is one
of: %s.`, strings.Join(reasons, ", ")),
			kubectl: [][]string{kubectlGet("pods", append(rc.nsArgs(), "-o",
				"custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,WAITING:.status.containerStatuses[*].state.waiting.reason,INIT:.status.initContainerStatuses[*].state.waiting.reason")...)},
		}
		return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
			var out []overviewTarget
			rc.scopedPods(func(p *corev1.Pod) {
				for _, s := range podStatuses(p) {
					if s.State.Waiting == nil {
						continue
					}
					for _, r := range reasons {
						if s.State.Waiting.Reason == r {
							out = append(out, objTarget(ovPodsGVR, p.ObjectMeta, fmt.Sprintf("%s (%s)", r, s.Name), sev))
							return
						}
					}
				}
			})
			return out
		})
	}
}

// lastTermination returns the most recent termination of a container.
func lastTermination(s corev1.ContainerStatus) *corev1.ContainerStateTerminated {
	if s.State.Terminated != nil {
		return s.State.Terminated
	}
	return s.LastTerminationState.Terminated
}

func podsOOMKilledRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/pods-oomkilled", section: overviewHealth, title: "/pods OOMKilled (" + rc.windowLabel() + ")",
		explain: fmt.Sprintf(`Pods with a container whose current or last termination has reason
OOMKilled and finished within the window (%s). The container used more
memory than its limit, or the node ran out of memory.`, rc.windowLabel()),
		kubectl: [][]string{kubectlGet("pods", append(rc.nsArgs(), "-o",
			"custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,LAST:.status.containerStatuses[*].lastState.terminated.reason,AT:.status.containerStatuses[*].lastState.terminated.finishedAt")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
		var out []overviewTarget
		rc.scopedPods(func(p *corev1.Pod) {
			for _, s := range podStatuses(p) {
				t := lastTermination(s)
				if t == nil || t.Reason != "OOMKilled" || !rc.within(t.FinishedAt.Time) {
					continue
				}
				out = append(out, overviewTarget{
					gvr: ovPodsGVR, namespace: p.Namespace, name: p.Name, severity: severityWarning,
					value: fmt.Sprintf("OOMKilled (%s) %s ago", s.Name, shortDuration(rc.now.Sub(t.FinishedAt.Time))),
					rank:  float64(t.FinishedAt.Unix()),
				})
				return
			}
		})
		return out
	})
}

func podsRestartingRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/pods-restarting", section: overviewHealth, title: "/pods restarting",
		explain: fmt.Sprintf(`Pods with a container that restarted at least restartThreshold (%d) times
and whose last restart finished within the window (%s). The API keeps only
the restart count and the last termination, so this is an estimate of
"restarts in the last %s", not an exact count.`, rc.cfg.RestartThreshold, rc.windowLabel(), rc.windowLabel()),
		kubectl: [][]string{kubectlGet("pods", append(rc.nsArgs(), "-o",
			"custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,RESTARTS:.status.containerStatuses[*].restartCount,LAST:.status.containerStatuses[*].lastState.terminated.finishedAt")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
		var out []overviewTarget
		rc.scopedPods(func(p *corev1.Pod) {
			best := int32(0)
			var bestStatus corev1.ContainerStatus
			for _, s := range podStatuses(p) {
				t := s.LastTerminationState.Terminated
				if int(s.RestartCount) < rc.cfg.RestartThreshold || t == nil || !rc.within(t.FinishedAt.Time) {
					continue
				}
				if s.RestartCount > best {
					best, bestStatus = s.RestartCount, s
				}
			}
			if best == 0 {
				return
			}
			t := bestStatus.LastTerminationState.Terminated
			out = append(out, overviewTarget{
				gvr: ovPodsGVR, namespace: p.Namespace, name: p.Name, severity: severityWarning, rank: float64(best),
				value: fmt.Sprintf("%d restarts (%s), last %s ago", best, bestStatus.Name, shortDuration(rc.now.Sub(t.FinishedAt.Time))),
			})
		})
		return out
	})
}

// --- Health: workloads ------------------------------------------------------

func replicasOf(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

func availabilitySeverity(available int32) overviewSeverity {
	if available == 0 {
		return severityFailed
	}
	return severityWarning
}

func workloadsUnavailableRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/workloads-unavailable", section: overviewHealth, title: "/workloads unavailable",
		explain: `Deployments and StatefulSets with fewer available replicas than
spec.replicas, and DaemonSets with fewer available pods than
desiredNumberScheduled. None available fails the row.`,
		kubectl: [][]string{
			kubectlGet("deployments,statefulsets,daemonsets", rc.nsArgs()...),
		},
	}
	kinds := []schema.GroupVersionResource{ovDeploymentsGVR, ovStatefulSetsGVR, ovDaemonSetsGVR}
	return partialCountFinding(rc, f, nil, kinds, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.deployments {
			d := &rc.in.deployments[i]
			want := replicasOf(d.Spec.Replicas)
			if want > 0 && d.Status.AvailableReplicas < want {
				t := objTarget(ovDeploymentsGVR, d.ObjectMeta, fmt.Sprintf("available %d/%d", d.Status.AvailableReplicas, want), availabilitySeverity(d.Status.AvailableReplicas))
				out = append(out, rc.graced(t, deploymentUnavailableSince(d), deploymentFailed(d)))
			}
		}
		for i := range rc.in.statefulSets {
			s := &rc.in.statefulSets[i]
			want := replicasOf(s.Spec.Replicas)
			if want > 0 && s.Status.AvailableReplicas < want {
				out = append(out, objTarget(ovStatefulSetsGVR, s.ObjectMeta, fmt.Sprintf("available %d/%d", s.Status.AvailableReplicas, want), availabilitySeverity(s.Status.AvailableReplicas)))
			}
		}
		for i := range rc.in.daemonSets {
			d := &rc.in.daemonSets[i]
			want := d.Status.DesiredNumberScheduled
			if want > 0 && d.Status.NumberAvailable < want {
				out = append(out, objTarget(ovDaemonSetsGVR, d.ObjectMeta, fmt.Sprintf("available %d/%d", d.Status.NumberAvailable, want), availabilitySeverity(d.Status.NumberAvailable)))
			}
		}
		return out
	})
}

func deploymentCondition(d *appsv1.Deployment, t appsv1.DeploymentConditionType) *appsv1.DeploymentCondition {
	for i := range d.Status.Conditions {
		if d.Status.Conditions[i].Type == t {
			return &d.Status.Conditions[i]
		}
	}
	return nil
}

func workloadsNotUpdatedRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/workloads-rollout", section: overviewHealth, title: "/workloads rolling out",
		explain: `Deployments, StatefulSets (RollingUpdate) and DaemonSets with replicas not
yet on the current template, and Deployments whose Progressing condition is
False (for example ProgressDeadlineExceeded), which fails the row.`,
		kubectl: [][]string{
			{"get", "deployments,statefulsets,daemonsets"},
			{"rollout", "status", placeholderWorkload},
		},
		supplies: []string{
			"the updated and desired replicas of each workload",
			"the rollout of one workload",
		},
	}
	f.kubectl[0] = append(f.kubectl[0], rc.nsArgs()...)
	kinds := []schema.GroupVersionResource{ovDeploymentsGVR, ovStatefulSetsGVR, ovDaemonSetsGVR}
	return partialCountFinding(rc, f, nil, kinds, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.deployments {
			d := &rc.in.deployments[i]
			if c := deploymentCondition(d, appsv1.DeploymentProgressing); c != nil && c.Status == corev1.ConditionFalse {
				out = append(out, objTarget(ovDeploymentsGVR, d.ObjectMeta, "Progressing=False "+c.Reason, severityFailed))
				continue
			}
			want := replicasOf(d.Spec.Replicas)
			if d.Status.UpdatedReplicas < want {
				out = append(out, rc.graced(objTarget(ovDeploymentsGVR, d.ObjectMeta, fmt.Sprintf("updated %d/%d", d.Status.UpdatedReplicas, want), severityWarning), rc.deploymentRolloutSince(d), false))
			}
		}
		for i := range rc.in.statefulSets {
			s := &rc.in.statefulSets[i]
			if s.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
				continue
			}
			want := replicasOf(s.Spec.Replicas)
			if s.Status.UpdatedReplicas < want {
				out = append(out, objTarget(ovStatefulSetsGVR, s.ObjectMeta, fmt.Sprintf("updated %d/%d", s.Status.UpdatedReplicas, want), severityWarning))
			}
		}
		for i := range rc.in.daemonSets {
			d := &rc.in.daemonSets[i]
			if d.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
				continue
			}
			want := d.Status.DesiredNumberScheduled
			if d.Status.UpdatedNumberScheduled < want {
				out = append(out, objTarget(ovDaemonSetsGVR, d.ObjectMeta, fmt.Sprintf("updated %d/%d", d.Status.UpdatedNumberScheduled, want), severityWarning))
			}
		}
		return out
	})
}

// --- Health: jobs -----------------------------------------------------------

func jobFailed(j *batchv1.Job) (bool, string) {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true, c.Reason
		}
	}
	return false, ""
}

func jobsFailedRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/jobs-failed", section: overviewHealth, title: "/jobs failed",
		explain: `Jobs with the condition Failed=True (for example BackoffLimitExceeded or
DeadlineExceeded).`,
		kubectl: [][]string{kubectlGet("jobs", append(rc.nsArgs(), "-o",
			"custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,FAILED:.status.conditions[?(@.type=='Failed')].reason")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovJobsGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.jobs {
			j := &rc.in.jobs[i]
			if failed, reason := jobFailed(j); failed {
				out = append(out, objTarget(ovJobsGVR, j.ObjectMeta, reason, severityFailed))
			}
		}
		return out
	})
}

func cronJobsLastRunFailedRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/cronjobs-failed", section: overviewHealth, title: "/cronjobs last run failed",
		explain: `CronJobs whose newest Job (by creation time, owned by the CronJob) has
the condition Failed=True.`,
		kubectl: [][]string{
			kubectlGet("cronjobs", rc.nsArgs()...),
			kubectlGet("jobs", append(rc.nsArgs(), "--sort-by", ".metadata.creationTimestamp")...),
		},
		supplies: []string{
			"the CronJobs in scope",
			"their Jobs, the newest last, whose outcome decides the row",
		},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovCronJobsGVR, ovJobsGVR}, func() []overviewTarget {
		newest := make(map[string]*batchv1.Job) // by owner UID
		for i := range rc.in.jobs {
			j := &rc.in.jobs[i]
			ref := metav1.GetControllerOf(j)
			if ref == nil || ref.Kind != "CronJob" {
				continue
			}
			if cur, ok := newest[string(ref.UID)]; !ok || j.CreationTimestamp.After(cur.CreationTimestamp.Time) {
				newest[string(ref.UID)] = j
			}
		}
		var out []overviewTarget
		for i := range rc.in.cronJobs {
			cj := &rc.in.cronJobs[i]
			j, ok := newest[string(cj.UID)]
			if !ok {
				continue
			}
			if failed, reason := jobFailed(j); failed {
				out = append(out, objTarget(ovCronJobsGVR, cj.ObjectMeta, fmt.Sprintf("%s: %s", j.Name, reason), severityFailed))
			}
		}
		return out
	})
}

func cronJobsSuspendedRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/cronjobs-suspended", section: overviewHealth, title: "/cronjobs suspended",
		explain: `CronJobs with spec.suspend set. They do not start new Jobs.`,
		kubectl: [][]string{kubectlGet("cronjobs", rc.nsArgs()...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovCronJobsGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.cronJobs {
			cj := &rc.in.cronJobs[i]
			if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
				out = append(out, objTarget(ovCronJobsGVR, cj.ObjectMeta, "suspended", severityWarning))
			}
		}
		return out
	})
}

// --- Health: storage --------------------------------------------------------

func pvcsRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/pvcs", section: overviewHealth, title: "/pvcs pending or lost",
		explain: `PersistentVolumeClaims in phase Pending (warning) or Lost (failed).`,
		kubectl: [][]string{kubectlGet("persistentvolumeclaims", rc.nsArgs()...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPVCsGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.pvcs {
			c := &rc.in.pvcs[i]
			switch c.Status.Phase {
			case corev1.ClaimPending:
				out = append(out, objTarget(ovPVCsGVR, c.ObjectMeta, "Pending", severityWarning))
			case corev1.ClaimLost:
				out = append(out, objTarget(ovPVCsGVR, c.ObjectMeta, "Lost", severityFailed))
			}
		}
		return out
	})
}

func pvsRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "health/pvs", section: overviewHealth, title: "/pvs released or failed",
		explain: `PersistentVolumes in phase Released (warning: the claim is gone, the data
stays) or Failed (failed). In a namespace scope only volumes whose claimRef
points into that namespace count.`,
		kubectl: [][]string{kubectlGet("persistentvolumes")},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPVsGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.pvs {
			v := &rc.in.pvs[i]
			if rc.in.namespace != AllNamespaces && (v.Spec.ClaimRef == nil || v.Spec.ClaimRef.Namespace != rc.in.namespace) {
				continue
			}
			switch v.Status.Phase {
			case corev1.VolumeReleased:
				out = append(out, objTarget(ovPVsGVR, v.ObjectMeta, "Released", severityWarning))
			case corev1.VolumeFailed:
				out = append(out, objTarget(ovPVsGVR, v.ObjectMeta, "Failed "+v.Status.Reason, severityFailed))
			}
		}
		return out
	})
}

// --- Risks ------------------------------------------------------------------

func pdbsBlockingRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/pdbs-blocking", section: overviewRisks, title: "/pdbs blocking eviction",
		explain: `PodDisruptionBudgets with status.disruptionsAllowed 0 that cover at least
one pod. A drain or a node upgrade waits on them.`,
		kubectl: [][]string{kubectlGet("poddisruptionbudgets", rc.nsArgs()...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPDBsGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.pdbs {
			p := &rc.in.pdbs[i]
			if p.Status.DisruptionsAllowed != 0 || p.Status.ExpectedPods == 0 {
				continue
			}
			out = append(out, objTarget(ovPDBsGVR, p.ObjectMeta,
				fmt.Sprintf("0 allowed, %d/%d healthy", p.Status.CurrentHealthy, p.Status.DesiredHealthy), severityWarning))
		}
		return out
	})
}

// podHasRequests reports whether any container of p sets a request or limit.
func podQOSBestEffort(p *corev1.Pod) bool {
	if p.Status.QOSClass != "" {
		return p.Status.QOSClass == corev1.PodQOSBestEffort
	}
	if p.Spec.Resources != nil && (len(p.Spec.Resources.Requests) > 0 || len(p.Spec.Resources.Limits) > 0) {
		return false
	}
	for _, c := range slices.Concat(p.Spec.InitContainers, p.Spec.Containers) {
		if len(c.Resources.Requests) > 0 || len(c.Resources.Limits) > 0 {
			return false
		}
	}
	return true
}

func podsBestEffortRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/pods-besteffort", section: overviewRisks, title: "/pods without requests",
		explain: `Pods that have not terminated with QoS class BestEffort: no container sets
a CPU or memory request or limit. The scheduler places them as if they used
nothing, and the kubelet evicts them first.`,
		kubectl: [][]string{kubectlGet("pods", append(rc.nsArgs(), "-o",
			"custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,QOS:.status.qosClass")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
		var out []overviewTarget
		rc.scopedPods(func(p *corev1.Pod) {
			if !podTerminal(p) && podQOSBestEffort(p) {
				out = append(out, objTarget(ovPodsGVR, p.ObjectMeta, "BestEffort", severityWarning))
			}
		})
		return out
	})
}

func podsNoMemoryLimitRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/pods-no-memory-limit", section: overviewRisks, title: "/pods without memory limit",
		explain: `Pods that have not terminated with an app container that sets no memory
limit, and no pod-level memory limit. A LimitRange default is applied when
the pod is created, so such a container was not covered by one. It can use
all the memory of its node.`,
		kubectl: [][]string{kubectlGet("pods", append(rc.nsArgs(), "-o",
			"custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,MEMORY_LIMITS:.spec.containers[*].resources.limits.memory")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
		var out []overviewTarget
		rc.scopedPods(func(p *corev1.Pod) {
			if podTerminal(p) {
				return
			}
			if p.Spec.Resources != nil {
				if _, ok := p.Spec.Resources.Limits[corev1.ResourceMemory]; ok {
					return
				}
			}
			var missing []string
			for _, c := range p.Spec.Containers {
				if _, ok := c.Resources.Limits[corev1.ResourceMemory]; !ok {
					missing = append(missing, c.Name)
				}
			}
			if len(missing) > 0 {
				out = append(out, objTarget(ovPodsGVR, p.ObjectMeta, "no limit: "+strings.Join(missing, ", "), severityWarning))
			}
		})
		return out
	})
}

// coveredByPDB reports whether a PDB in namespace selects podLabels.
func coveredByPDB(rc *ruleContext, namespace string, podLabels map[string]string) bool {
	for i := range rc.in.pdbs {
		p := &rc.in.pdbs[i]
		if p.Namespace != namespace || p.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil || sel.Empty() {
			continue
		}
		if sel.Matches(labels.Set(podLabels)) {
			return true
		}
	}
	return false
}

func singleReplicaRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/single-replica", section: overviewRisks, title: "/workloads single replica, no PDB",
		explain: `Deployments and StatefulSets with spec.replicas 1 whose pod template no
PodDisruptionBudget in the same namespace selects. A drain or an upgrade
takes them down without warning.`,
		kubectl: [][]string{
			kubectlGet("deployments,statefulsets", rc.nsArgs()...),
			kubectlGet("poddisruptionbudgets", append(rc.nsArgs(), "-o", "wide")...),
		},
		supplies: []string{
			"the replicas of each Deployment and StatefulSet",
			"the PodDisruptionBudgets whose selectors may cover them",
		},
	}
	kinds := []schema.GroupVersionResource{ovDeploymentsGVR, ovStatefulSetsGVR}
	return partialCountFinding(rc, f, []schema.GroupVersionResource{ovPDBsGVR}, kinds, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.deployments {
			d := &rc.in.deployments[i]
			if replicasOf(d.Spec.Replicas) == 1 && !coveredByPDB(rc, d.Namespace, d.Spec.Template.Labels) {
				out = append(out, objTarget(ovDeploymentsGVR, d.ObjectMeta, "1 replica, no PDB", severityWarning))
			}
		}
		for i := range rc.in.statefulSets {
			s := &rc.in.statefulSets[i]
			if replicasOf(s.Spec.Replicas) == 1 && !coveredByPDB(rc, s.Namespace, s.Spec.Template.Labels) {
				out = append(out, objTarget(ovStatefulSetsGVR, s.ObjectMeta, "1 replica, no PDB", severityWarning))
			}
		}
		return out
	})
}

func quotasRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/quotas", section: overviewRisks, title: "/quotas near limit",
		explain: fmt.Sprintf(`ResourceQuotas where any status.used value reaches quotaWarnPercent (%d%%)
of its status.hard value. A quota at 100%% fails the row: new objects of
that kind are rejected. The value shows the fullest resource.`, rc.cfg.QuotaWarnPercent),
		kubectl: [][]string{{"describe", "resourcequotas"}},
	}
	f.kubectl[0] = append(f.kubectl[0], rc.nsArgs()...)
	return countFinding(rc, f, []schema.GroupVersionResource{ovQuotasGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.quotas {
			q := &rc.in.quotas[i]
			worst, worstName := -1.0, ""
			names := make([]string, 0, len(q.Status.Hard))
			for name := range q.Status.Hard {
				names = append(names, string(name))
			}
			sort.Strings(names)
			for _, name := range names {
				hard := q.Status.Hard[corev1.ResourceName(name)]
				used, ok := q.Status.Used[corev1.ResourceName(name)]
				if !ok || hard.IsZero() {
					continue
				}
				pct := used.AsApproximateFloat64() * 100 / hard.AsApproximateFloat64()
				if pct > worst {
					worst, worstName = pct, fmt.Sprintf("%s %s/%s", name, used.String(), hard.String())
				}
			}
			if worst < float64(rc.cfg.QuotaWarnPercent) {
				continue
			}
			sev := severityWarning
			if worst >= 100 {
				sev = severityFailed
			}
			out = append(out, overviewTarget{
				gvr: ovQuotasGVR, namespace: q.Namespace, name: q.Name, severity: sev, rank: worst,
				value: fmt.Sprintf("%s (%.0f%%)", worstName, worst),
			})
		}
		return out
	})
}

func hpaCondition(h *autoscalingv2.HorizontalPodAutoscaler, t autoscalingv2.HorizontalPodAutoscalerConditionType) *autoscalingv2.HorizontalPodAutoscalerCondition {
	for i := range h.Status.Conditions {
		if h.Status.Conditions[i].Type == t {
			return &h.Status.Conditions[i]
		}
	}
	return nil
}

func hpasRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/hpas", section: overviewRisks, title: "/hpas at max or blind",
		explain: `HorizontalPodAutoscalers whose current replicas reached spec.maxReplicas
(they cannot scale further), or whose ScalingActive condition is False
(they cannot read their metrics and do not scale).`,
		kubectl: [][]string{kubectlGet("horizontalpodautoscalers.autoscaling", rc.nsArgs()...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovHPAsGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.hpas {
			h := &rc.in.hpas[i]
			if c := hpaCondition(h, autoscalingv2.ScalingActive); c != nil && c.Status == corev1.ConditionFalse {
				out = append(out, objTarget(ovHPAsGVR, h.ObjectMeta, "ScalingActive=False "+c.Reason, severityWarning))
				continue
			}
			if h.Spec.MaxReplicas > 0 && h.Status.CurrentReplicas >= h.Spec.MaxReplicas {
				out = append(out, objTarget(ovHPAsGVR, h.ObjectMeta, fmt.Sprintf("at max %d", h.Spec.MaxReplicas), severityWarning))
			}
		}
		return out
	})
}

// --- Activity ---------------------------------------------------------------

// eventLastSeen returns when an event last happened.
func eventLastSeen(e *corev1.Event) time.Time {
	t := e.CreationTimestamp.Time
	for _, c := range []time.Time{e.EventTime.Time, e.LastTimestamp.Time} {
		if c.After(t) {
			t = c
		}
	}
	if e.Series != nil && e.Series.LastObservedTime.After(t) {
		t = e.Series.LastObservedTime.Time
	}
	return t
}

func warningEventsRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "activity/events-warning", section: overviewActivity, title: "/events warnings (" + rc.windowLabel() + ")",
		explain: fmt.Sprintf(`Events of type Warning last seen within the window (%s): the latest of
series.lastObservedTime, lastTimestamp, eventTime and the creation time.
Enter lists them, the newest first; the value shows the reason, the
involved object and how often it happened. Enter on an event opens the
involved object in the other panel.`, rc.windowLabel()),
		kubectl: [][]string{kubectlGet("events", append(rc.nsArgs(), "--field-selector", "type=Warning", "--sort-by", ".lastTimestamp")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovEventsGVR}, func() []overviewTarget {
		var out []overviewTarget
		for i := range rc.in.events {
			e := &rc.in.events[i]
			if e.Type != corev1.EventTypeWarning {
				continue
			}
			seen := eventLastSeen(e)
			if !rc.within(seen) {
				continue
			}
			count := e.Count
			if e.Series != nil && e.Series.Count > count {
				count = e.Series.Count
			}
			value := fmt.Sprintf("%s %s/%s", e.Reason, e.InvolvedObject.Kind, e.InvolvedObject.Name)
			if count > 1 {
				value += fmt.Sprintf(" x%d", count)
			}
			value += fmt.Sprintf(", %s ago", shortDuration(rc.now.Sub(seen)))
			involved := e.InvolvedObject
			out = append(out, overviewTarget{
				gvr: ovEventsGVR, namespace: e.Namespace, name: e.Name, severity: severityWarning,
				value: value, rank: float64(seen.UnixNano()), involved: &involved,
			})
		}
		return out
	})
}

func podsRestartedRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "activity/pods-restarted", section: overviewActivity, title: "/pods restarted (" + rc.windowLabel() + ")",
		explain: fmt.Sprintf(`Pods with a container whose last termination finished within the window
(%s), whatever its restart count. Enter lists them, the most recent first.`, rc.windowLabel()),
		kubectl: [][]string{kubectlGet("pods", append(rc.nsArgs(), "-o",
			"custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,RESTARTS:.status.containerStatuses[*].restartCount,LAST:.status.containerStatuses[*].lastState.terminated.finishedAt")...)},
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
		var out []overviewTarget
		rc.scopedPods(func(p *corev1.Pod) {
			var latest *corev1.ContainerStateTerminated
			name := ""
			for _, s := range podStatuses(p) {
				t := s.LastTerminationState.Terminated
				if s.RestartCount == 0 || t == nil || !rc.within(t.FinishedAt.Time) {
					continue
				}
				if latest == nil || t.FinishedAt.After(latest.FinishedAt.Time) {
					latest, name = t, s.Name
				}
			}
			if latest == nil {
				return
			}
			out = append(out, overviewTarget{
				gvr: ovPodsGVR, namespace: p.Namespace, name: p.Name, severity: severityWarning,
				value: fmt.Sprintf("%s: %s, %s ago", name, latest.Reason, shortDuration(rc.now.Sub(latest.FinishedAt.Time))),
				rank:  float64(latest.FinishedAt.UnixNano()),
			})
		})
		return out
	})
}

// shortDuration prints d like kubectl prints ages: 5m, 3h, 2d.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return duration.HumanDuration(d)
}
