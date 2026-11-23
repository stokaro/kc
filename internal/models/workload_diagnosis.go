package models

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// maxDiagnosedPods bounds the pods whose problems one diagnosis lists,
	// so a DaemonSet on a large cluster stays readable.
	maxDiagnosedPods = 10
	// maxEventsPerFinding bounds the events a finding cites, newest first.
	maxEventsPerFinding = 3
	// restartWindow is how recent a restart must be to count as a problem.
	// An old restart of a container that runs now is history, not a symptom.
	restartWindow = time.Hour
)

// workloadDiagnosisGVRs are the built-in types with a workload
// /diagnosis. Types with a health config entry have one too.
var workloadDiagnosisGVRs = []schema.GroupVersionResource{
	podGVR, relDeploymentsGVR, relReplicaSetsGVR, relStatefulSetsGVR, relDaemonSetsGVR, relJobsGVR,
}

func hasWorkloadDiagnosis(gvr schema.GroupVersionResource) bool {
	return slices.Contains(workloadDiagnosisGVRs, gvr) || healthAdapterFor(gvr) != nil
}

var (
	imagePullReasons = []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ErrImageNeverPull"}
	configReasons    = []string{"CreateContainerConfigError", "CreateContainerError", "RunContainerError"}
)

const workloadDiagnosisNote = "Facts from the cache at the read time. A \"maybe\" row is a correlation, not a confirmed cause; " +
	"kc does not check probes, nodes or the application, so a clean result does not prove the workload works."

// workloadInput is what a workload diagnosis reads. A nil slice with an
// error means the list could not be read: the checks that need it report
// the source as unavailable instead of finding nothing.
type workloadInput struct {
	GVR    schema.GroupVersionResource
	Object *unstructured.Unstructured // the workload or pod as read
	Now    time.Time                  // the read time
	// Grace is the startup grace (overview.startupGrace): a Deployment
	// whose Available=False condition is younger is starting, not failing.
	Grace time.Duration

	Pods           []corev1.Pod // the pods of the namespace; unused for a pod
	PodsErr        error
	ReplicaSets    []appsv1.ReplicaSet // of the namespace, for a Deployment
	ReplicaSetsErr error
	Events         []corev1.Event // of the namespace
	EventsErr      error
	Claims         []corev1.PersistentVolumeClaim // of the namespace
	ClaimsErr      error

	// Scheduling is set for an unschedulable pod: what kc's node checks
	// read.
	Scheduling *schedulingInput

	// Chain is set for a custom resource: the namespace's controllers it
	// may own on the way to its pods.
	Chain *chainInput

	health healthVerdict // of a type with a health config entry
}

// adapter returns the health config of a type kc has no built-in
// diagnosis for, or nil.
func (in *workloadInput) adapter() *healthAdapter {
	if slices.Contains(workloadDiagnosisGVRs, in.GVR) {
		return nil
	}
	return healthAdapterFor(in.GVR)
}

// diagnoseWorkload explains what the workload's status, its pods, their
// containers, their claims and the events about them show. Every finding
// cites its sources and when they were observed. Objects are matched by
// UID, so a recreated object with the same name never borrows the pods or
// events of the one it replaced.
func diagnoseWorkload(in workloadInput) (diagReport, error) {
	d := diagReport{Note: workloadDiagnosisNote}
	if in.Object == nil {
		return d, nil
	}
	ns, name, uid := in.Object.GetNamespace(), in.Object.GetName(), in.Object.GetUID()
	d.Summary = append(d.Summary,
		fmt.Sprintf("object: %s/%s, uid %s", kubectlResource(in.GVR), name, uid),
		"read at: "+in.Now.UTC().Format(time.RFC3339))
	if a := in.adapter(); a != nil {
		in.health = a.evaluate(in.Object)
		d.Summary = append(d.Summary, in.health.summary(a.key))
		d.Note = healthDiagnosisNote + " " + workloadDiagnosisNote
	}
	status, ownerUIDs, err := workloadStatusFindings(in)
	if err != nil {
		return d, err
	}
	pods, sourceFindings, err := in.ownPods(ownerUIDs)
	if err != nil {
		return d, err
	}
	d.Findings = append(d.Findings, sourceFindings...)
	if in.GVR != podGVR && in.PodsErr == nil {
		d.Summary = append(d.Summary, "pods: "+podCountSummary(pods))
	}
	problems := in.podProblems(pods)
	if slices.ContainsFunc(problems, func(f diagFinding) bool { return f.Severity >= diagWarning }) {
		for i := range status {
			if status[i].Severity >= diagWarning {
				status[i].Hint = "The pod problems below may explain this; kc relates them by owner and does not confirm the cause."
			}
		}
	}
	d.Findings = append(append(d.Findings, status...), problems...)
	if in.Scheduling != nil {
		d.Findings = append(d.Findings, explainScheduling(*in.Scheduling)...)
	}
	if len(d.Findings) == 0 {
		d.Findings = append(d.Findings, diagFinding{
			Code: "no-problem", Severity: diagInfo, Observed: in.Now,
			Title:  "No problem found by the checks kc runs",
			Detail: "kc checks the status conditions and replica counts, scheduling, pod readiness and readiness gates, image pulls, container failures and restarts, initialization and the claims the pods use.",
			Links:  in.eventLinks(ns, uid, "", true),
		})
	}
	sortFindings(d.Findings)
	return d, nil
}

func podCountSummary(pods []corev1.Pod) string {
	ready := 0
	for i := range pods {
		if c := podCondition(&pods[i], corev1.PodReady); c != nil && c.Status == corev1.ConditionTrue {
			ready++
		}
	}
	return fmt.Sprintf("%d owned, %d ready", len(pods), ready)
}

func (in *workloadInput) selfLink(evidence string) diagLink {
	return diagLink{GVR: in.GVR, Namespace: in.Object.GetNamespace(), Name: in.Object.GetName(), Evidence: evidence}
}

// workloadStatusFindings reads the workload's own status and returns the
// UIDs whose pods belong to it: the workload, and the ReplicaSets a
// Deployment controls.
func workloadStatusFindings(in workloadInput) ([]diagFinding, map[types.UID]bool, error) {
	owners := map[types.UID]bool{in.Object.GetUID(): true}
	var (
		out []diagFinding
		err error
	)
	switch in.GVR {
	case relDeploymentsGVR:
		var dep appsv1.Deployment
		if err = fromUnstructured(in.Object, &dep); err == nil {
			out = in.deploymentFindings(&dep, owners)
		}
	case relReplicaSetsGVR:
		var rs appsv1.ReplicaSet
		if err = fromUnstructured(in.Object, &rs); err == nil {
			out = in.replicaSetFindings(&rs)
		}
	case relStatefulSetsGVR:
		var sts appsv1.StatefulSet
		if err = fromUnstructured(in.Object, &sts); err == nil {
			out = in.replicaFinding(replicasOf(sts.Spec.Replicas), sts.Status.ReadyReplicas, "status.readyReplicas", nil)
		}
	case relDaemonSetsGVR:
		var ds appsv1.DaemonSet
		if err = fromUnstructured(in.Object, &ds); err == nil {
			out = in.daemonSetFindings(&ds)
		}
	case relJobsGVR:
		var job batchv1.Job
		if err = fromUnstructured(in.Object, &job); err == nil {
			out = in.jobFindings(&job)
		}
	default:
		if a := in.adapter(); a != nil {
			out = in.healthFindings(a, in.health)
		}
		if in.Chain != nil {
			uids, edges := walkChain(in.Object.GetUID(), *in.Chain)
			for uid := range uids {
				owners[uid] = true
			}
			out = append(out, chainFindings(*in.Chain, edges, in.Object.GetNamespace())...)
		}
	}
	return out, owners, err
}

func fromUnstructured(u *unstructured.Unstructured, into any) error {
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into); err != nil {
		return fmt.Errorf("decode %s %s: %w", u.GetKind(), u.GetName(), err)
	}
	return nil
}

// deploymentFindings reports a failed rollout, a replica failure and
// unavailable replicas, and adds the ReplicaSets the Deployment controls
// to owners.
func (in *workloadInput) deploymentFindings(dep *appsv1.Deployment, owners map[types.UID]bool) []diagFinding {
	var out []diagFinding
	for _, c := range dep.Status.Conditions {
		switch {
		case c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse:
			out = append(out, in.conditionFinding("rollout-failed", diagError, "The rollout does not progress", string(c.Type), string(c.Status), c.Reason, c.Message, c.LastTransitionTime))
		case c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue:
			out = append(out, in.conditionFinding("replica-failure", diagError, "Pods cannot be created", string(c.Type), string(c.Status), c.Reason, c.Message, c.LastTransitionTime))
		}
	}
	var available *appsv1.DeploymentCondition
	for i := range dep.Status.Conditions {
		if dep.Status.Conditions[i].Type == appsv1.DeploymentAvailable && dep.Status.Conditions[i].Status == corev1.ConditionFalse {
			available = &dep.Status.Conditions[i]
		}
	}
	var since *metav1.Time
	if available != nil {
		since = &available.LastTransitionTime
	}
	replicas := in.replicaFinding(replicasOf(dep.Spec.Replicas), dep.Status.AvailableReplicas, "status.availableReplicas", since)
	if len(replicas) == 1 && since != nil && !since.IsZero() && !deploymentFailed(dep) && in.Now.Sub(since.Time) < in.Grace {
		f := &replicas[0]
		f.Severity = diagInfo
		f.Title += fmt.Sprintf(", starting %s (grace %s)", shortDuration(in.Now.Sub(since.Time)), shortDuration(in.Grace))
		f.Detail += " Available became False less than the startup grace ago and the rollout has no failed condition."
	}
	out = append(out, replicas...)
	if in.ReplicaSetsErr != nil {
		out = append(out, unavailableSource("replicasets-unknown", "ReplicaSets", "which pods belong to the Deployment", in.ReplicaSetsErr, relReplicaSetsGVR, dep.Namespace))
		return out
	}
	for i := range in.ReplicaSets {
		rs := &in.ReplicaSets[i]
		if ref := metav1.GetControllerOf(rs); ref != nil && ref.UID == dep.UID {
			owners[rs.UID] = true
			out = append(out, in.ownerEventFinding(rs.ObjectMeta, relReplicaSetsGVR)...)
		}
	}
	return out
}

func (in *workloadInput) replicaSetFindings(rs *appsv1.ReplicaSet) []diagFinding {
	var out []diagFinding
	for _, c := range rs.Status.Conditions {
		if c.Type == appsv1.ReplicaSetReplicaFailure && c.Status == corev1.ConditionTrue {
			out = append(out, in.conditionFinding("replica-failure", diagError, "Pods cannot be created", string(c.Type), string(c.Status), c.Reason, c.Message, c.LastTransitionTime))
		}
	}
	return append(out, in.replicaFinding(replicasOf(rs.Spec.Replicas), rs.Status.ReadyReplicas, "status.readyReplicas", nil)...)
}

func (in *workloadInput) daemonSetFindings(ds *appsv1.DaemonSet) []diagFinding {
	var out []diagFinding
	if n := ds.Status.NumberUnavailable; n > 0 {
		out = append(out, diagFinding{
			Code: "unavailable", Severity: diagWarning, Observed: in.Now,
			Title:  fmt.Sprintf("%d of %d scheduled pods are unavailable", n, ds.Status.DesiredNumberScheduled),
			Detail: fmt.Sprintf("status.numberUnavailable=%d, status.desiredNumberScheduled=%d, read at the observed time.", n, ds.Status.DesiredNumberScheduled),
			Links:  []diagLink{in.selfLink(fmt.Sprintf("status.numberUnavailable=%d", n))},
			Next:   "Open the pods below; a DaemonSet pod that cannot run on its node shows why in its events.",
		})
	}
	if n := ds.Status.NumberMisscheduled; n > 0 {
		out = append(out, diagFinding{
			Code: "misscheduled", Severity: diagInfo, Observed: in.Now,
			Title:  fmt.Sprintf("%d pods run on nodes the DaemonSet should not use", n),
			Detail: fmt.Sprintf("status.numberMisscheduled=%d.", n),
			Links:  []diagLink{in.selfLink(fmt.Sprintf("status.numberMisscheduled=%d", n))},
			Next:   "Compare the node selector and tolerations with the labels and taints of the nodes.",
		})
	}
	return out
}

func (in *workloadInput) jobFindings(job *batchv1.Job) []diagFinding {
	var out []diagFinding
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			out = append(out, in.conditionFinding("job-failed", diagError, "The Job failed", string(c.Type), string(c.Status), c.Reason, c.Message, c.LastTransitionTime))
		}
	}
	return out
}

// conditionFinding reports one status condition of the workload.
func (in *workloadInput) conditionFinding(code string, sev diagSeverity, title, condType, condStatus, reason, message string, since metav1.Time) diagFinding {
	if reason != "" {
		title += ": " + reason
	}
	return diagFinding{
		Code: code, Severity: sev, Title: title, Detail: message, Observed: since.Time,
		Links: append([]diagLink{in.selfLink(fmt.Sprintf("status.conditions[%s]=%s %s", condType, condStatus, reason))},
			in.eventLinks(in.Object.GetNamespace(), in.Object.GetUID(), "", true)...),
		Next: "Read the condition message, then the events and pods below.",
	}
}

// replicaFinding reports replicas that are not available. since is the
// condition that dates the state, when the workload has one; otherwise the
// state is dated by the read.
func (in *workloadInput) replicaFinding(want, have int32, field string, since *metav1.Time) []diagFinding {
	if want == 0 || have >= want {
		return nil
	}
	observed := in.Now
	if since != nil && !since.IsZero() {
		observed = since.Time
	}
	return []diagFinding{{
		Code: "unavailable", Severity: diagWarning, Observed: observed,
		Title:  fmt.Sprintf("%d of %d replicas are unavailable", want-have, want),
		Detail: fmt.Sprintf("%s=%d, spec.replicas=%d.", field, have, want),
		Links:  []diagLink{in.selfLink(fmt.Sprintf("%s=%d of spec.replicas=%d", field, have, want))},
		Next:   "Open the pods below; their conditions, containers and events show why they are not ready.",
	}}
}

// ownerEventFinding reports the warning events of an intermediate owner,
// such as the ReplicaSet that cannot create pods because of a quota.
func (in *workloadInput) ownerEventFinding(meta metav1.ObjectMeta, gvr schema.GroupVersionResource) []diagFinding {
	if in.EventsErr != nil {
		return nil // reported once, by the pod checks
	}
	links := in.eventLinks(meta.Namespace, meta.UID, "", true)
	if len(links) == 0 {
		return nil
	}
	return []diagFinding{{
		Code: "events/" + meta.Name, Severity: diagWarning, Observed: eventsObserved(in.Events, meta.UID),
		Title:  fmt.Sprintf("%s %s has warning events", kubectlResource(gvr), meta.Name),
		Detail: "The events below are about an object the workload controls.",
		Links:  append([]diagLink{{GVR: gvr, Namespace: meta.Namespace, Name: meta.Name, Evidence: "controlled by " + in.Object.GetName() + ", uid " + string(meta.UID)}}, links...),
		Next:   "Read the event messages; a FailedCreate event names the quota, limit or admission check that refused the pod.",
	}}
}

// unavailableSource reports a list kc could not read: what depends on it
// is unknown, not absent.
func unavailableSource(code, what, consequence string, err error, gvr schema.GroupVersionResource, namespace string) diagFinding {
	return diagFinding{
		Code: code, Severity: diagIncomplete,
		Title:  fmt.Sprintf("%s could not be read; %s is unknown", what, consequence),
		Detail: err.Error(),
		Links:  []diagLink{{GVR: gvr, Namespace: namespace, Name: "(not listed)", Evidence: strings.ToLower(what), Problem: readProblem(err)}},
	}
}

// ownPods returns the pods the workload controls, by controller owner UID,
// sorted by name. For a pod it is the pod itself.
func (in *workloadInput) ownPods(owners map[types.UID]bool) ([]corev1.Pod, []diagFinding, error) {
	if in.GVR == podGVR {
		var pod corev1.Pod
		if err := fromUnstructured(in.Object, &pod); err != nil {
			return nil, nil, err
		}
		return []corev1.Pod{pod}, nil, nil
	}
	if in.PodsErr != nil {
		return nil, []diagFinding{unavailableSource("pods-unknown", "Pods", "the state of the workload's pods", in.PodsErr, podGVR, in.Object.GetNamespace())}, nil
	}
	var pods []corev1.Pod
	for i := range in.Pods {
		if ref := metav1.GetControllerOf(&in.Pods[i]); ref != nil && owners[ref.UID] {
			pods = append(pods, in.Pods[i])
		}
	}
	slices.SortFunc(pods, func(a, b corev1.Pod) int { return cmp.Compare(a.Name, b.Name) })
	return pods, nil, nil
}

// podProblems returns the findings of the first maxDiagnosedPods pods that
// have any, and one more finding that counts the pods left out.
func (in *workloadInput) podProblems(pods []corev1.Pod) []diagFinding {
	var (
		out     []diagFinding
		shown   int
		skipped int
	)
	for i := range pods {
		fs := in.podFindings(&pods[i])
		if len(fs) == 0 {
			continue
		}
		if shown == maxDiagnosedPods {
			skipped++
			continue
		}
		shown++
		out = append(out, fs...)
	}
	if skipped > 0 {
		out = append(out, diagFinding{
			Code: "more-pods", Severity: diagInfo,
			Title:  fmt.Sprintf("%d more pods have problems; only the first %d are explained", skipped, maxDiagnosedPods),
			Detail: "Open the workload's pods to see the rest.",
		})
	}
	if in.EventsErr != nil && len(out) > 0 {
		out = append(out, unavailableSource("events-unknown", "Events", "what the cluster reported about these objects", in.EventsErr, ovEventsGVR, in.Object.GetNamespace()))
	}
	return out
}

func (in *workloadInput) podFindings(pod *corev1.Pod) []diagFinding {
	var out []diagFinding
	if f, ok := in.schedulingFinding(pod); ok {
		out = append(out, f)
	}
	explained := make(map[string]bool)
	for _, s := range pod.Status.InitContainerStatuses {
		if f, ok := in.containerFinding(pod, s, true); ok {
			out = append(out, f)
			explained[s.Name] = f.Severity >= diagWarning
		}
	}
	for _, s := range pod.Status.ContainerStatuses {
		if f, ok := in.containerFinding(pod, s, false); ok {
			out = append(out, f)
			// An info row (an old OOM kill) is history: it does not
			// explain that the container is not ready now.
			explained[s.Name] = f.Severity >= diagWarning
		}
	}
	out = append(out, in.readinessFindings(pod, explained)...)
	return append(out, in.claimFindings(pod)...)
}

func (in *workloadInput) podLink(pod *corev1.Pod, evidence string) diagLink {
	return diagLink{GVR: podGVR, Namespace: pod.Namespace, Name: pod.Name, Evidence: evidence}
}

// schedulingFinding reports a pod the scheduler has not placed: the
// scheduler's condition and events are the reported outcome. A gated pod
// is waiting for a controller, not failing.
func (in *workloadInput) schedulingFinding(pod *corev1.Pod) (diagFinding, bool) {
	if schedulingStateOf(pod) == schedGated {
		return in.gatedFinding(pod), true
	}
	c := podCondition(pod, corev1.PodScheduled)
	if c == nil || c.Status != corev1.ConditionFalse || podTerminal(pod) {
		return diagFinding{}, false
	}
	links := []diagLink{in.podLink(pod, fmt.Sprintf("status.conditions[PodScheduled]=False %s, uid %s", c.Reason, pod.UID))}
	next := "Open the /diagnosis of pod " + pod.Name + " for kc's node checks: selectors, affinity, taints, bound volumes and requests against allocatable."
	if in.GVR == podGVR {
		next = "The scheduler's message is the decision. kc's node checks below explain the constraints kc can read."
	}
	return diagFinding{
		Code: "unschedulable/" + pod.Name, Severity: diagError, Observed: c.LastTransitionTime.Time,
		Title:  fmt.Sprintf("Pod %s cannot be scheduled: %s", pod.Name, c.Reason),
		Detail: c.Message,
		Links:  append(links, in.eventLinks(pod.Namespace, pod.UID, "", true)...),
		Next:   next,
	}, true
}

// gatedFinding reports a pod whose spec.schedulingGates keep the
// scheduler from considering it.
func (in *workloadInput) gatedFinding(pod *corev1.Pod) diagFinding {
	gates := make([]string, 0, len(pod.Spec.SchedulingGates))
	for _, g := range pod.Spec.SchedulingGates {
		gates = append(gates, g.Name)
	}
	observed := in.Now
	if c := podCondition(pod, corev1.PodScheduled); c != nil && !c.LastTransitionTime.IsZero() {
		observed = c.LastTransitionTime.Time
	}
	return diagFinding{
		Code: "gated/" + pod.Name, Severity: diagWarning, Observed: observed,
		Title:  fmt.Sprintf("Pod %s waits for scheduling gates: %s", pod.Name, strings.Join(gates, ", ")),
		Detail: "The scheduler does not consider a pod while spec.schedulingGates is not empty; the controller that added a gate removes it.",
		Links:  []diagLink{in.podLink(pod, "spec.schedulingGates="+strings.Join(gates, ","))},
		Next:   "Find the controller that owns the gate (its name usually says which). kc runs no node checks for a gated pod.",
	}
}

// containerFinding reports the worst state of one container: an image it
// cannot pull, a configuration it cannot start with, an OOM kill, a crash
// loop or recent restart, or a failed exit.
func (in *workloadInput) containerFinding(pod *corev1.Pod, s corev1.ContainerStatus, init bool) (diagFinding, bool) {
	c := containerRef{pod: pod, status: s, init: init}
	if w := s.State.Waiting; w != nil {
		switch {
		case slices.Contains(imagePullReasons, w.Reason):
			return in.waitingFinding(c, "image", "cannot pull its image", "Check the image name and tag ("+s.Image+"), the registry and the pod's imagePullSecrets."), true
		case slices.Contains(configReasons, w.Reason):
			return in.waitingFinding(c, "config", "cannot be started", "Check the ConfigMaps, Secrets and volumes the container refers to (/requires lists them)."), true
		}
	}
	if f, ok := in.oomFinding(c); ok {
		return f, true
	}
	if last := s.LastTerminationState.Terminated; last != nil && (crashLooping(s) || in.Now.Sub(last.FinishedAt.Time) <= restartWindow) {
		f := in.terminatedFinding(c, "restarts", last, fmt.Sprintf("restarted %d times; the last instance exited with code %d", s.RestartCount, last.ExitCode))
		if !crashLooping(s) {
			f.Severity = diagWarning
		}
		f.Next = c.next("Read the previous log for the reason of the exit.")
		return f, true
	}
	if t := s.State.Terminated; t != nil && t.ExitCode != 0 {
		f := in.terminatedFinding(c, "exit", t, fmt.Sprintf("exited with code %d", t.ExitCode))
		f.Next = c.next("Read the log of the container for the reason of the exit.")
		return f, true
	}
	return diagFinding{}, false
}

// containerRef names one container of a pod and its status.
type containerRef struct {
	pod    *corev1.Pod
	status corev1.ContainerStatus
	init   bool
}

func (c containerRef) label() string {
	if c.init {
		return fmt.Sprintf("Init container %s of pod %s", c.status.Name, c.pod.Name)
	}
	return fmt.Sprintf("Container %s of pod %s", c.status.Name, c.pod.Name)
}

// statusField is the field of the pod status that holds the container.
func (c containerRef) statusField() string {
	if c.init {
		return "status.initContainerStatuses[" + c.status.Name + "]"
	}
	return "status.containerStatuses[" + c.status.Name + "]"
}

// fieldPath is how events name the container.
func (c containerRef) fieldPath() string {
	if c.init {
		return "spec.initContainers{" + c.status.Name + "}"
	}
	return "spec.containers{" + c.status.Name + "}"
}

func (c containerRef) code(kind string) string {
	return kind + "/" + c.pod.Name + "/" + c.status.Name
}

func (c containerRef) next(next string) string {
	if c.init {
		return "Initialization blocks the main containers. " + next
	}
	return next
}

func (in *workloadInput) waitingFinding(c containerRef, kind, what, next string) diagFinding {
	w := c.status.State.Waiting
	links := []diagLink{in.podLink(c.pod, fmt.Sprintf("%s.state.waiting=%s, uid %s", c.statusField(), w.Reason, c.pod.UID))}
	events := in.eventLinks(c.pod.Namespace, c.pod.UID, c.fieldPath(), true)
	observed := eventsObservedAt(in.Events, c.pod.UID, c.fieldPath())
	if observed.IsZero() {
		observed = in.Now
	}
	return diagFinding{
		Code: c.code(kind), Severity: diagError, Observed: observed,
		Title:  fmt.Sprintf("%s %s: %s", c.label(), what, w.Reason),
		Detail: w.Message,
		Links:  append(links, events...),
		Next:   c.next(next),
	}
}

func (in *workloadInput) terminatedFinding(c containerRef, kind string, t *corev1.ContainerStateTerminated, what string) diagFinding {
	reason := t.Reason
	if reason == "" {
		reason = "no reason"
	}
	field := c.statusField() + ".state.terminated"
	if c.status.LastTerminationState.Terminated == t {
		field = c.statusField() + ".lastState.terminated"
	}
	f := diagFinding{
		Code: c.code(kind), Severity: diagError, Observed: t.FinishedAt.Time,
		Title:  fmt.Sprintf("%s %s (%s)", c.label(), what, reason),
		Detail: t.Message,
		Links: append([]diagLink{in.podLink(c.pod, fmt.Sprintf("%s: exit code %d, %s, uid %s", field, t.ExitCode, reason, c.pod.UID))},
			in.eventLinks(c.pod.Namespace, c.pod.UID, c.fieldPath(), true)...),
	}
	spec := LogsSpec{Namespace: c.pod.Namespace, Pod: c.pod.Name, Container: c.status.Name, Follow: true, TailLines: DefaultLogsTailLines}
	f.Logs = append(f.Logs, diagLog{Label: "logs", Spec: spec})
	if c.status.RestartCount > 0 {
		prev := spec
		prev.Previous = true
		f.Logs = append(f.Logs, diagLog{Label: "logs (previous)", Spec: prev})
	}
	return f
}

func crashLooping(s corev1.ContainerStatus) bool {
	return s.State.Waiting != nil && s.State.Waiting.Reason == "CrashLoopBackOff"
}

const reasonOOMKilled = "OOMKilled"

// oomFinding reports an OOM kill of the container. A container that is
// terminated by it now, or crash loops after it, fails: an error. One
// that was killed less than restartWindow ago and runs again is a
// warning. An older kill of a container that runs now is history: it is
// kept as info, with its time, because it says the memory sizing was
// once too small, but it is not a current failure.
func (in *workloadInput) oomFinding(c containerRef) (diagFinding, bool) {
	s := c.status
	if t := s.State.Terminated; t != nil && t.Reason == reasonOOMKilled {
		f := in.terminatedFinding(c, "oom", t, fmt.Sprintf("was OOMKilled (%d restarts)", s.RestartCount))
		f.Next = c.next(oomNext(c))
		return f, true
	}
	t := s.LastTerminationState.Terminated
	if t == nil || t.Reason != reasonOOMKilled {
		return diagFinding{}, false
	}
	if crashLooping(s) {
		f := in.terminatedFinding(c, "oom", t, fmt.Sprintf("was OOMKilled (%d restarts)", s.RestartCount))
		f.Next = c.next(oomNext(c))
		return f, true
	}
	ago := in.Now.Sub(t.FinishedAt.Time)
	if t.FinishedAt.IsZero() || ago <= restartWindow {
		f := in.terminatedFinding(c, "oom", t, fmt.Sprintf("was OOMKilled %s ago and runs again (%d restarts)", shortDuration(ago), s.RestartCount))
		if t.FinishedAt.IsZero() {
			f.Title = fmt.Sprintf("%s was OOMKilled at an unknown time and runs again (%d restarts) (%s)", c.label(), s.RestartCount, reasonOOMKilled)
		}
		f.Severity = diagWarning
		f.Next = c.next(oomNext(c))
		return f, true
	}
	f := in.terminatedFinding(c, "oom", t, fmt.Sprintf("was OOMKilled %s ago and has run since (%d restarts)", shortDuration(ago), s.RestartCount))
	f.Severity = diagInfo
	f.Next = c.next("History, not a current failure: the container has run for more than " + shortDuration(restartWindow) +
		" since. " + oomNext(c))
	return f, true
}

// oomNext says what an OOM kill implies, from the limit the container
// has. Without a container memory limit, the kill came from a pod-level
// limit or from memory pressure on the node, not from exceeding a limit
// of the container.
func oomNext(c containerRef) string {
	if q, ok := memoryLimit(c.pod, c.status.Name, c.init); ok {
		return "The container reached its memory limit (limits.memory " + q + "); read the previous log and compare the usage with the limit."
	}
	return "The container has no limits.memory, so the kill came from a pod-level memory limit or from memory pressure on the node; " +
		"check the pod's resources and the node's memory, and read the previous log."
}

// memoryLimit returns the container's limits.memory, if it has one.
func memoryLimit(pod *corev1.Pod, container string, init bool) (string, bool) {
	containers := pod.Spec.Containers
	if init {
		containers = pod.Spec.InitContainers
	}
	for _, c := range containers {
		if c.Name != container {
			continue
		}
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			return q.String(), true
		}
	}
	return "", false
}

// claimFindings checks the claims the pod's volumes name, and only those:
// a missing or unbound claim the pod does not use is not its problem.
func (in *workloadInput) claimFindings(pod *corev1.Pod) []diagFinding {
	var out []diagFinding
	for i, v := range pod.Spec.Volumes {
		var claim, field string
		switch {
		case v.PersistentVolumeClaim != nil:
			claim, field = v.PersistentVolumeClaim.ClaimName, fmt.Sprintf("spec.volumes[%d].persistentVolumeClaim", i)
		case v.Ephemeral != nil:
			claim, field = pod.Name+"-"+v.Name, fmt.Sprintf("spec.volumes[%d].ephemeral", i)
		default:
			continue
		}
		if f, ok := in.claimFinding(pod, claim, field); ok {
			out = append(out, f)
		}
	}
	return out
}

func (in *workloadInput) claimFinding(pod *corev1.Pod, claim, field string) (diagFinding, bool) {
	code := "claim/" + pod.Name + "/" + claim
	podLink := in.podLink(pod, field+" names "+claim)
	if in.ClaimsErr != nil {
		f := unavailableSource(code, "PersistentVolumeClaims", "whether claim "+claim+" is bound", in.ClaimsErr, pvcGVR, pod.Namespace)
		f.Links = append([]diagLink{podLink}, f.Links...)
		return f, true
	}
	idx := slices.IndexFunc(in.Claims, func(c corev1.PersistentVolumeClaim) bool { return c.Name == claim })
	if idx < 0 {
		return diagFinding{
			Code: code, Severity: diagError, Observed: in.Now,
			Title: fmt.Sprintf("Pod %s uses claim %s, which does not exist", pod.Name, claim),
			Links: []diagLink{podLink, {GVR: pvcGVR, Namespace: pod.Namespace, Name: claim, Evidence: "claim", Problem: problemNotFound}},
			Next:  "Create the claim, or fix the claim name in the pod template.",
		}, true
	}
	pvc := &in.Claims[idx]
	if pvc.Status.Phase == corev1.ClaimBound {
		return diagFinding{}, false
	}
	phase := string(pvc.Status.Phase)
	if phase == "" {
		phase = "unknown"
	}
	observed := eventsObserved(in.Events, pvc.UID)
	if observed.IsZero() {
		observed = in.Now
	}
	links := []diagLink{podLink, {GVR: pvcGVR, Namespace: pvc.Namespace, Name: pvc.Name, Evidence: "status.phase=" + phase + ", uid " + string(pvc.UID)}}
	return diagFinding{
		Code: code, Severity: diagWarning, Observed: observed,
		Title:  fmt.Sprintf("Claim %s used by pod %s is %s, not Bound", claim, pod.Name, phase),
		Detail: "A pod does not start until every claim it uses is bound.",
		Links:  append(links, in.eventLinks(pvc.Namespace, pvc.UID, "", false)...),
		Next:   "Read the claim's events: check its StorageClass, the provisioner and the volume binding mode.",
	}, true
}

// eventLinks returns the newest events about the object with uid, at most
// maxEventsPerFinding, limited to warnings when warningsOnly is set and to
// one container when fieldPath is set. A list kc could not read is one
// link that says so.
func (in *workloadInput) eventLinks(namespace string, uid types.UID, fieldPath string, warningsOnly bool) []diagLink {
	if in.EventsErr != nil {
		return []diagLink{{GVR: ovEventsGVR, Namespace: namespace, Name: "(not listed)", Evidence: "events", Problem: readProblem(in.EventsErr)}}
	}
	var match []*corev1.Event
	for i := range in.Events {
		e := &in.Events[i]
		if e.InvolvedObject.UID != uid || (fieldPath != "" && e.InvolvedObject.FieldPath != fieldPath) {
			continue
		}
		if warningsOnly && e.Type != corev1.EventTypeWarning {
			continue
		}
		match = append(match, e)
	}
	slices.SortStableFunc(match, func(a, b *corev1.Event) int { return eventTime(b).Compare(eventTime(a)) })
	if len(match) > maxEventsPerFinding {
		match = match[:maxEventsPerFinding]
	}
	out := make([]diagLink, 0, len(match))
	for _, e := range match {
		out = append(out, diagLink{GVR: ovEventsGVR, Namespace: e.Namespace, Name: e.Name, Evidence: eventEvidence(e)})
	}
	return out
}

func eventEvidence(e *corev1.Event) string {
	msg := strings.TrimSpace(e.Message)
	if len(msg) > 160 {
		msg = msg[:157] + "..."
	}
	return fmt.Sprintf("%s %s: %s (x%d, last %s)", e.Type, e.Reason, msg, eventCount(e), eventTime(e).UTC().Format(time.RFC3339))
}

// eventTime is when the event was last seen, from the newest field set.
func eventTime(e *corev1.Event) time.Time {
	switch {
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.Time
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.Time
	}
	return e.CreationTimestamp.Time
}

func eventCount(e *corev1.Event) int32 {
	switch {
	case e.Series != nil && e.Series.Count > 0:
		return e.Series.Count
	case e.Count > 0:
		return e.Count
	}
	return 1
}

// eventsObserved is when the newest event about uid was seen, or zero.
func eventsObserved(events []corev1.Event, uid types.UID) time.Time {
	return eventsObservedAt(events, uid, "")
}

func eventsObservedAt(events []corev1.Event, uid types.UID, fieldPath string) time.Time {
	var newest time.Time
	for i := range events {
		e := &events[i]
		if e.InvolvedObject.UID != uid || (fieldPath != "" && e.InvolvedObject.FieldPath != fieldPath) {
			continue
		}
		if t := eventTime(e); t.After(newest) {
			newest = t
		}
	}
	return newest
}
