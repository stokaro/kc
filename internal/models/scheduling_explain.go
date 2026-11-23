package models

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	schedcorev1 "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

// maxNodesPerReason bounds the nodes one scheduling finding cites.
const maxNodesPerReason = 5

// defaultSchedulerName is the scheduler kc's node checks follow.
const defaultSchedulerName = corev1.DefaultSchedulerName

// schedulingInput is what kc reads to explain a pod that is not scheduled.
// A nil slice with an error means the source could not be read; the checks
// that need it are reported as not run, never as "no node fits".
type schedulingInput struct {
	Pod *corev1.Pod
	Now time.Time

	Nodes    []corev1.Node
	NodesErr error
	// Assigned are the pods of every namespace, for the requests already
	// bound to each node. AssignedSkipped is set when they were not read
	// because the pod requests nothing.
	Assigned        []corev1.Pod
	AssignedErr     error
	AssignedSkipped bool
	// Claims are the claims of the pod's namespace; Volumes the bound PVs of
	// the pod's claims by name, VolumeErrs the PVs that could not be read.
	Claims     []corev1.PersistentVolumeClaim
	ClaimsErr  error
	Volumes    map[string]*corev1.PersistentVolume
	VolumeErrs map[string]error
}

// podSchedulingState is where a pod is on its way to a node.
type podSchedulingState int

const (
	schedScheduled podSchedulingState = iota
	schedGated
	schedUnschedulable
	schedPending // no PodScheduled=False yet
)

func schedulingStateOf(pod *corev1.Pod) podSchedulingState {
	if pod.Spec.NodeName != "" || podTerminal(pod) {
		return schedScheduled
	}
	if len(pod.Spec.SchedulingGates) > 0 {
		return schedGated
	}
	if c := podCondition(pod, corev1.PodScheduled); c != nil {
		switch {
		case c.Status == corev1.ConditionTrue:
			return schedScheduled
		case c.Reason == corev1.PodReasonSchedulingGated:
			return schedGated
		case c.Status == corev1.ConditionFalse:
			return schedUnschedulable
		}
	}
	return schedPending
}

// needsSchedulingExplanation reports whether a pod diagnosis explains the
// scheduling of pod: only an unschedulable pod gets node checks.
func needsSchedulingExplanation(pod *corev1.Pod) bool {
	return schedulingStateOf(pod) == schedUnschedulable
}

// boundClaimVolumes lists the PVs the pod's bound claims use.
func boundClaimVolumes(pod *corev1.Pod, claims []corev1.PersistentVolumeClaim) []string {
	var out []string
	for _, name := range podClaimNames(pod) {
		i := slices.IndexFunc(claims, func(c corev1.PersistentVolumeClaim) bool { return c.Name == name })
		if i >= 0 && claims[i].Status.Phase == corev1.ClaimBound && claims[i].Spec.VolumeName != "" {
			out = append(out, claims[i].Spec.VolumeName)
		}
	}
	return out
}

// podClaimNames lists the claims of the pod's volumes, generic ephemeral
// volumes included.
func podClaimNames(pod *corev1.Pod) []string {
	var out []string
	for _, v := range pod.Spec.Volumes {
		switch {
		case v.PersistentVolumeClaim != nil:
			out = append(out, v.PersistentVolumeClaim.ClaimName)
		case v.Ephemeral != nil:
			out = append(out, pod.Name+"-"+v.Name)
		}
	}
	return out
}

// nodeCheck is the first check of kc's that rejects a node, in the order of
// the default scheduler's filter plugins: NodeUnschedulable,
// TaintToleration, NodeAffinity, NodeResourcesFit, VolumeBinding.
type nodeCheck struct {
	key      string // groups nodes: "unschedulable", "taint", "affinity", "volume", "resource/cpu", "pods"
	title    string // "insufficient cpu"
	evidence string // for the node row
}

// checkedPod holds what every node check of one pod needs.
type checkedPod struct {
	pod       *corev1.Pod
	affinity  nodeaffinity.RequiredNodeAffinity
	requests  corev1.ResourceList
	volumes   []*corev1.PersistentVolume
	used      map[string]corev1.ResourceList // node -> requests of the pods bound there
	podCount  map[string]int
	resources bool // the capacity checks can run
}

func newCheckedPod(in schedulingInput) checkedPod {
	c := checkedPod{pod: in.Pod, affinity: nodeaffinity.GetRequiredNodeAffinity(in.Pod)}
	c.requests, _ = podRequestsAndLimits(in.Pod)
	for _, name := range slices.Sorted(maps.Keys(in.Volumes)) {
		c.volumes = append(c.volumes, in.Volumes[name])
	}
	if in.Assigned != nil {
		c.resources = true
		c.used = make(map[string]corev1.ResourceList)
		c.podCount = make(map[string]int)
		for i := range in.Assigned {
			p := &in.Assigned[i]
			if p.Spec.NodeName == "" || podTerminal(p) || p.UID == in.Pod.UID {
				continue
			}
			req, _ := podRequestsAndLimits(p)
			sum := c.used[p.Spec.NodeName]
			if sum == nil {
				sum = corev1.ResourceList{}
			}
			for name, q := range req {
				v := sum[name]
				v.Add(q)
				sum[name] = v
			}
			c.used[p.Spec.NodeName] = sum
			c.podCount[p.Spec.NodeName]++
		}
	}
	return c
}

// noScheduleTaint keeps the taints that block scheduling.
func noScheduleTaint(t *corev1.Taint) bool {
	return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
}

// check returns the first of kc's checks that rejects node; ok is true when
// node passes all of them. err is set when the pod's affinity cannot be
// evaluated.
func (c checkedPod) check(node *corev1.Node) (nodeCheck, bool, error) {
	tolerations := c.pod.Spec.Tolerations
	if node.Spec.Unschedulable {
		taint := corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule}
		if !schedcorev1.TolerationsTolerateTaint(logr.Discard(), tolerations, &taint, false) {
			return nodeCheck{key: "unschedulable", title: "cordoned (spec.unschedulable)", evidence: "spec.unschedulable=true"}, false, nil
		}
	}
	if t, bad := schedcorev1.FindMatchingUntoleratedTaint(logr.Discard(), node.Spec.Taints, tolerations, noScheduleTaint, false); bad {
		return nodeCheck{key: "taint", title: "a taint the pod does not tolerate", evidence: "taint " + t.ToString() + " not tolerated by spec.tolerations"}, false, nil
	}
	ok, err := c.affinity.Match(node)
	if err != nil {
		return nodeCheck{}, false, err
	}
	if !ok {
		return nodeCheck{key: "affinity", title: "no match for spec.nodeSelector or required node affinity", evidence: "labels do not match spec.nodeSelector / spec.affinity.nodeAffinity.required"}, false, nil
	}
	for _, pv := range c.volumes {
		if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
			continue
		}
		match, err := nodeaffinity.NewLazyErrorNodeSelector(pv.Spec.NodeAffinity.Required).Match(node)
		if err == nil && !match {
			return nodeCheck{key: "volume", title: "outside the node affinity of a bound volume", evidence: "persistentvolume " + pv.Name + " spec.nodeAffinity.required does not match"}, false, nil
		}
	}
	if c.resources {
		if nc, bad := c.capacity(node); bad {
			return nc, false, nil
		}
	}
	return nodeCheck{}, true, nil
}

// capacity compares the pod's requests plus the requests already bound to
// node with its allocatable, as NodeResourcesFit does.
func (c checkedPod) capacity(node *corev1.Node) (nodeCheck, bool) {
	alloc := node.Status.Allocatable
	if limit, ok := alloc[corev1.ResourcePods]; ok && int64(c.podCount[node.Name]+1) > limit.Value() {
		return nodeCheck{key: "pods", title: "too many pods", evidence: fmt.Sprintf("%d pods bound, allocatable pods %s", c.podCount[node.Name], limit.String())}, true
	}
	for _, name := range slices.Sorted(maps.Keys(c.requests)) {
		want := c.requests[name]
		if want.IsZero() || name == corev1.ResourcePods {
			continue
		}
		have, ok := alloc[name]
		if !ok && name == corev1.ResourceEphemeralStorage {
			continue // a node that reports no ephemeral storage is not checked for it
		}
		used := c.used[node.Name][name]
		total := used.DeepCopy()
		total.Add(want)
		if total.Cmp(have) > 0 {
			return nodeCheck{
				key: "resource/" + string(name), title: "insufficient " + string(name),
				evidence: fmt.Sprintf("allocatable %s %s, requested by %d bound pods %s, this pod %s", name, quantity(have), c.podCount[node.Name], quantity(used), quantity(want)),
			}, true
		}
	}
	return nodeCheck{}, false
}

func quantity(q resource.Quantity) string {
	if q.IsZero() {
		return "0"
	}
	return q.String()
}

// nodeGroup is the nodes one check rejects first, with the evidence per
// node.
type nodeGroup struct {
	check nodeCheck
	nodes []string
	ev    []string
}

// verdicts is the result of the checks over every node: the rejecting
// groups, their keys by size then key, and the nodes that pass.
type verdicts struct {
	groups map[string]*nodeGroup
	keys   []string
	passed []string
}

// nodeVerdicts runs the checks against every node.
func nodeVerdicts(cp checkedPod, nodes []corev1.Node) (verdicts, error) {
	v := verdicts{groups: make(map[string]*nodeGroup)}
	for i := range nodes {
		node := &nodes[i]
		nc, ok, err := cp.check(node)
		if err != nil {
			return verdicts{}, err
		}
		if ok {
			v.passed = append(v.passed, node.Name)
			continue
		}
		g := v.groups[nc.key]
		if g == nil {
			g = &nodeGroup{check: nc}
			v.groups[nc.key] = g
		}
		g.nodes = append(g.nodes, node.Name)
		g.ev = append(g.ev, nc.evidence)
	}
	v.keys = slices.Collect(maps.Keys(v.groups))
	slices.SortFunc(v.keys, func(a, b string) int {
		if d := len(v.groups[b].nodes) - len(v.groups[a].nodes); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	return v, nil
}

// explainScheduling runs kc's node checks for a pod the scheduler could not
// place. The scheduler's condition and events stay the reported outcome
// (the unschedulable finding); these findings say which nodes kc's own
// checks reject and why, and what kc did not check.
func explainScheduling(in schedulingInput) []diagFinding {
	pod := in.Pod
	if pod == nil || schedulingStateOf(pod) != schedUnschedulable {
		return nil
	}
	podLink := func(evidence string) diagLink {
		return diagLink{GVR: podGVR, Namespace: pod.Namespace, Name: pod.Name, Evidence: evidence}
	}
	if in.NodesErr != nil {
		f := unavailableSource("sched/nodes-unknown", "Nodes", "which nodes could take the pod", in.NodesErr, relNodesGVR, "")
		return []diagFinding{f, unevaluatedFinding(in, podLink)}
	}
	cp := newCheckedPod(in)
	v, err := nodeVerdicts(cp, in.Nodes)
	groups, keys, passed := v.groups, v.keys, v.passed
	if err != nil {
		return []diagFinding{{
			Code: "sched/affinity-invalid", Severity: diagIncomplete, Observed: in.Now,
			Title:  "kc cannot evaluate the pod's required node affinity",
			Detail: err.Error(),
			Links:  []diagLink{podLink("spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution")},
		}, unevaluatedFinding(in, podLink)}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", len(groups[k].nodes), groups[k].check.title))
	}
	summary := schedulingSummary(in, len(passed), parts, cp.resources || in.AssignedSkipped)
	summary.Links = append([]diagLink{podLink(constraintEvidence(pod, cp.requests))}, nodeLinks(passed, func(int) string { return "passes every check kc runs" })...)
	if len(passed) > maxNodesPerReason {
		summary.Detail = strings.TrimSpace(summary.Detail + fmt.Sprintf(" %d more nodes pass.", len(passed)-maxNodesPerReason))
	}
	out := []diagFinding{summary}
	for _, k := range keys {
		g := groups[k]
		f := diagFinding{
			Code: "sched/" + k, Severity: diagInfo, Observed: in.Now,
			Title: fmt.Sprintf("%d nodes rejected by kc's check: %s", len(g.nodes), g.check.title),
			Links: nodeLinks(g.nodes, func(i int) string { return g.ev[i] }),
			Next:  checkNext(k),
		}
		if len(g.nodes) > maxNodesPerReason {
			f.Detail = fmt.Sprintf("%d more nodes.", len(g.nodes)-maxNodesPerReason)
		}
		out = append(out, f)
	}
	if in.AssignedErr != nil {
		out = append(out, unavailableSource("sched/pods-unknown", "Pods of all namespaces", "the requests already on each node (capacity was not checked)", in.AssignedErr, podGVR, ""))
	}
	for _, name := range slices.Sorted(maps.Keys(in.VolumeErrs)) {
		out = append(out, unavailableSource("sched/pv-unknown/"+name, "PersistentVolume "+name, "its node affinity (not checked)", in.VolumeErrs[name], pvGVR, ""))
	}
	return append(out, unevaluatedFinding(in, podLink))
}

// nodeLinks cites the first maxNodesPerReason nodes.
func nodeLinks(nodes []string, evidence func(int) string) []diagLink {
	out := make([]diagLink, 0, min(len(nodes), maxNodesPerReason))
	for i, n := range nodes[:min(len(nodes), maxNodesPerReason)] {
		out = append(out, diagLink{GVR: relNodesGVR, Name: n, Evidence: evidence(i)})
	}
	return out
}

// schedulingSummary is the sched/nodes finding without its links: how many
// nodes pass, how many each check rejects, and whether capacity was
// checked.
func schedulingSummary(in schedulingInput, passed int, parts []string, resourcesRun bool) diagFinding {
	f := diagFinding{
		Code: "sched/nodes", Observed: in.Now,
		Next: "Compare with the scheduler's message above: it is the decision; kc's checks are an explanation of the constraints it can read.",
	}
	rejected := strings.Join(parts, ", ")
	switch {
	case len(in.Nodes) == 0:
		f.Severity, f.Title = diagWarning, "kc's node checks: the cluster lists no nodes"
	case passed == 0 && resourcesRun:
		f.Severity = diagWarning
		f.Title = fmt.Sprintf("kc's node checks: 0 of %d nodes pass (%s)", len(in.Nodes), rejected)
		f.Detail = "Each node is counted under the first check that rejects it, in the order of the default scheduler's filters."
	case passed == 0:
		f.Severity = diagIncomplete
		f.Title = fmt.Sprintf("kc's node checks: 0 of %d nodes pass the checks kc could run (%s); capacity was not checked", len(in.Nodes), rejected)
	default:
		f.Severity = diagInfo
		f.Title = fmt.Sprintf("kc's node checks: %d of %d nodes pass", passed, len(in.Nodes))
		if rejected != "" {
			f.Title += " (rejected: " + rejected + ")"
		}
		f.Hint = "The scheduler rejected these nodes for a reason kc does not check (see the unevaluated constraints), or the cluster changed since the scheduler's last attempt."
		if !resourcesRun {
			f.Severity = diagIncomplete
			f.Hint = "kc could not check capacity; the scheduler's message may name insufficient resources."
		}
	}
	return f
}

// constraintEvidence names the pod fields the checks read.
func constraintEvidence(pod *corev1.Pod, requests corev1.ResourceList) string {
	parts := []string{fmt.Sprintf("%d tolerations", len(pod.Spec.Tolerations))}
	if len(pod.Spec.NodeSelector) > 0 {
		sel := make([]string, 0, len(pod.Spec.NodeSelector))
		for _, k := range slices.Sorted(maps.Keys(pod.Spec.NodeSelector)) {
			sel = append(sel, k+"="+pod.Spec.NodeSelector[k])
		}
		parts = append(parts, "nodeSelector "+strings.Join(sel, ","))
	}
	if a := pod.Spec.Affinity; a != nil && a.NodeAffinity != nil && a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		parts = append(parts, fmt.Sprintf("required node affinity with %d terms", len(a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms)))
	}
	if len(requests) > 0 {
		req := make([]string, 0, len(requests))
		for _, n := range slices.Sorted(maps.Keys(requests)) {
			q := requests[n]
			req = append(req, string(n)+"="+q.String())
		}
		parts = append(parts, "effective requests "+strings.Join(req, ","))
	} else {
		parts = append(parts, "no requests")
	}
	return strings.Join(parts, "; ")
}

func checkNext(key string) string {
	switch key {
	case "unschedulable":
		return "The node is cordoned; uncordon it or wait for the drain to finish."
	case "taint":
		return "Add a toleration for the taint to the pod template, or remove the taint from the node."
	case "affinity":
		return "Compare the node labels with spec.nodeSelector and the required node affinity of the pod."
	case "volume":
		return "A bound volume can only be used on the nodes its node affinity names (often one zone)."
	case "pods":
		return "The node runs as many pods as its allocatable pod count allows."
	default:
		return "Requests count, not usage: lower the pod's requests, free requests on the node, or add nodes."
	}
}

// unevaluatedFinding lists what the pod asks for that kc does not check,
// so that "passes" is never read as "the scheduler would place it".
func unevaluatedFinding(in schedulingInput, podLink func(string) diagLink) diagFinding {
	pod := in.Pod
	var not []string
	if name := pod.Spec.SchedulerName; name != "" && name != defaultSchedulerName {
		not = append(not, "scheduler "+name+" (kc follows the default scheduler's filters)")
	}
	if a := pod.Spec.Affinity; a != nil && (a.PodAffinity != nil || a.PodAntiAffinity != nil) {
		not = append(not, "pod affinity and anti-affinity")
	}
	if len(pod.Spec.TopologySpreadConstraints) > 0 {
		not = append(not, "topology spread constraints")
	}
	for _, c := range slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers) {
		if slices.ContainsFunc(c.Ports, func(p corev1.ContainerPort) bool { return p.HostPort != 0 }) {
			not = append(not, "host ports")
			break
		}
	}
	if len(pod.Spec.ResourceClaims) > 0 {
		not = append(not, "dynamic resource claims")
	}
	if in.ClaimsErr == nil {
		for _, name := range podClaimNames(pod) {
			i := slices.IndexFunc(in.Claims, func(c corev1.PersistentVolumeClaim) bool { return c.Name == name })
			if i >= 0 && in.Claims[i].Status.Phase != corev1.ClaimBound {
				not = append(not, "the topology of unbound claim "+name+" (provisioning or WaitForFirstConsumer)")
			}
		}
	} else if len(podClaimNames(pod)) > 0 {
		not = append(not, "the pod's claims (they could not be read)")
	}
	if in.AssignedSkipped {
		not = append(not, "the pod count of each node (the pod requests nothing, so kc did not read the pods of all namespaces)")
	}
	not = append(not, "preemption of lower-priority pods", "volume count limits", "pods the scheduler has assumed but not bound yet")
	return diagFinding{
		Code: "sched/unevaluated", Severity: diagInfo,
		Title:  "kc does not simulate the scheduler; not checked: " + strings.Join(not, "; "),
		Detail: "kc checks cordoned nodes, NoSchedule and NoExecute taints, spec.nodeSelector, required node affinity, the node affinity of bound volumes, and requests against allocatable.",
		Links:  []diagLink{podLink("spec.schedulerName=" + cmp.Or(pod.Spec.SchedulerName, defaultSchedulerName))},
	}
}
