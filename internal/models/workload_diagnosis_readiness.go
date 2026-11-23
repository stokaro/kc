package models

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// maxReadinessLogs bounds the log rows one readiness finding offers.
const maxReadinessLogs = 3

// readinessFindings reports a running pod whose Ready condition is not
// True. explained names the containers another finding of the pod
// already covers (a crash loop, an OOM kill): they are not repeated.
//
// Only the status the kubelet and the controllers wrote is read. kc does
// not run probes, so a container that is not ready is reported as such,
// never as a failed probe.
func (in *workloadInput) readinessFindings(pod *corev1.Pod, explained map[string]bool) []diagFinding {
	ready := podCondition(pod, corev1.PodReady)
	if ready == nil || ready.Status == corev1.ConditionTrue ||
		pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		// A pending pod is covered by the scheduling and container checks, a
		// finished one has no readiness, and a terminating one is expected to
		// stop being ready.
		return nil
	}
	var out []diagFinding
	if gates := pendingReadinessGates(pod); len(gates) > 0 {
		out = append(out, in.readinessGateFinding(pod, ready, gates))
	}
	unready := unreadyContainers(pod, explained)
	switch {
	case len(unready) > 0:
		out = append(out, in.unreadyFinding(pod, ready, unready))
	case len(out) == 0 && !allUnreadyExplained(pod, explained):
		// Ready is False while every container says it is ready and no gate
		// is pending: the kubelet's or the node controller's view disagree,
		// as when the node stopped reporting.
		out = append(out, in.unreadyFinding(pod, ready, nil))
	}
	return out
}

// pendingReadinessGates returns the readiness gates of the pod whose
// condition is missing or not True, as "type=status".
func pendingReadinessGates(pod *corev1.Pod) []string {
	var out []string
	for _, g := range pod.Spec.ReadinessGates {
		c := podCondition(pod, g.ConditionType)
		switch {
		case c == nil:
			out = append(out, string(g.ConditionType)+" not set")
		case c.Status != corev1.ConditionTrue:
			out = append(out, fmt.Sprintf("%s=%s", g.ConditionType, c.Status))
		}
	}
	return out
}

// unreadyContainers returns the containers that count for readiness and
// report ready=false: the app containers and the sidecars (init
// containers with restartPolicy Always), without those in explained.
func unreadyContainers(pod *corev1.Pod, explained map[string]bool) []containerRef {
	var out []containerRef
	for _, s := range pod.Status.InitContainerStatuses {
		if !s.Ready && !explained[s.Name] && sidecar(pod, s.Name) {
			out = append(out, containerRef{pod: pod, status: s, init: true})
		}
	}
	for _, s := range pod.Status.ContainerStatuses {
		if !s.Ready && !explained[s.Name] {
			out = append(out, containerRef{pod: pod, status: s})
		}
	}
	return out
}

// allUnreadyExplained reports whether some container is not ready and
// every such container already has a finding.
func allUnreadyExplained(pod *corev1.Pod, explained map[string]bool) bool {
	return slices.ContainsFunc(podStatuses(pod), func(s corev1.ContainerStatus) bool { return !s.Ready && explained[s.Name] })
}

func sidecar(pod *corev1.Pod, name string) bool {
	i := slices.IndexFunc(pod.Spec.InitContainers, func(c corev1.Container) bool { return c.Name == name })
	return i >= 0 && pod.Spec.InitContainers[i].RestartPolicy != nil && *pod.Spec.InitContainers[i].RestartPolicy == corev1.ContainerRestartPolicyAlways
}

// readinessSeverity is info while the Ready condition is younger than the
// startup grace, a warning after it.
func (in *workloadInput) readinessSeverity(ready *corev1.PodCondition) (diagSeverity, string) {
	since := ready.LastTransitionTime.Time
	if since.IsZero() || in.Now.Sub(since) >= in.Grace {
		return diagWarning, ""
	}
	return diagInfo, fmt.Sprintf(", starting %s (grace %s)", shortDuration(in.Now.Sub(since)), shortDuration(in.Grace))
}

func (in *workloadInput) readinessBase(pod *corev1.Pod, ready *corev1.PodCondition, code, title string) diagFinding {
	sev, starting := in.readinessSeverity(ready)
	observed := ready.LastTransitionTime.Time
	if observed.IsZero() {
		observed = in.Now
	}
	links := []diagLink{in.podLink(pod, fmt.Sprintf("status.conditions[Ready]=%s %s, uid %s", ready.Status, ready.Reason, pod.UID))}
	if c := podCondition(pod, corev1.ContainersReady); c != nil {
		links = append(links, in.podLink(pod, fmt.Sprintf("status.conditions[ContainersReady]=%s %s", c.Status, c.Reason)))
	}
	return diagFinding{
		Code: code + "/" + pod.Name, Severity: sev, Observed: observed,
		Title:  title + starting,
		Detail: strings.TrimSpace(ready.Message),
		Links:  links,
	}
}

func (in *workloadInput) readinessGateFinding(pod *corev1.Pod, ready *corev1.PodCondition, gates []string) diagFinding {
	f := in.readinessBase(pod, ready, "readiness-gate",
		fmt.Sprintf("Pod %s is not ready: readiness gate %s", pod.Name, strings.Join(gates, ", ")))
	for _, g := range gates {
		f.Links = append(f.Links, in.podLink(pod, "spec.readinessGates: "+g))
	}
	f.Links = append(f.Links, in.eventLinks(pod.Namespace, pod.UID, "", true)...)
	f.Next = "A readiness gate is set by another controller, such as a load balancer controller; check that controller and its events."
	return f
}

func (in *workloadInput) unreadyFinding(pod *corev1.Pod, ready *corev1.PodCondition, unready []containerRef) diagFinding {
	if len(unready) == 0 {
		f := in.readinessBase(pod, ready, "unready", fmt.Sprintf("Pod %s is not ready: %s", pod.Name, orNoReason(ready.Reason)))
		f.Links = append(f.Links, in.eventLinks(pod.Namespace, pod.UID, "", true)...)
		f.Next = "The containers report ready but the pod does not; check the node's Ready condition, the status may be stale."
		return f
	}
	names := make([]string, 0, len(unready))
	for _, c := range unready {
		names = append(names, c.status.Name)
	}
	f := in.readinessBase(pod, ready, "unready",
		fmt.Sprintf("Pod %s is not ready: container %s not ready (%s)", pod.Name, strings.Join(names, ", "), orNoReason(ready.Reason)))
	for _, c := range unready {
		f.Links = append(f.Links, in.podLink(pod, c.statusField()+": "+containerReadiness(c)))
	}
	f.Links = append(f.Links, in.eventLinks(pod.Namespace, pod.UID, "", true)...)
	for _, c := range unready[:min(len(unready), maxReadinessLogs)] {
		f.Logs = append(f.Logs, diagLog{
			Label: "logs " + c.status.Name,
			Spec:  LogsSpec{Namespace: pod.Namespace, Pod: pod.Name, Container: c.status.Name, Follow: true, TailLines: DefaultLogsTailLines},
		})
	}
	f.Next = "A pod that is not ready receives no Service traffic. Read the Unhealthy events and the container log; kc does not run the probes."
	return f
}

// containerReadiness describes what the status and the spec say about a
// container that is not ready, without guessing why.
func containerReadiness(c containerRef) string {
	s := c.status
	var parts []string
	switch {
	case s.State.Running != nil:
		parts = append(parts, "running, ready=false")
	case s.State.Waiting != nil:
		parts = append(parts, "waiting "+orNoReason(s.State.Waiting.Reason)+", ready=false")
	case s.State.Terminated != nil:
		parts = append(parts, "terminated "+orNoReason(s.State.Terminated.Reason)+", ready=false")
	default:
		parts = append(parts, "ready=false")
	}
	if s.Started != nil && !*s.Started {
		parts = append(parts, "started=false")
	}
	spec := c.spec()
	switch {
	case spec == nil:
	case spec.StartupProbe != nil && s.Started != nil && !*s.Started:
		parts = append(parts, "has a startupProbe")
	case spec.ReadinessProbe != nil:
		parts = append(parts, "has a readinessProbe")
	default:
		parts = append(parts, "no readinessProbe")
	}
	return strings.Join(parts, ", ")
}

// spec returns the container's spec, or nil.
func (c containerRef) spec() *corev1.Container {
	containers := c.pod.Spec.Containers
	if c.init {
		containers = c.pod.Spec.InitContainers
	}
	for i := range containers {
		if containers[i].Name == c.status.Name {
			return &containers[i]
		}
	}
	return nil
}

func orNoReason(reason string) string {
	if reason == "" {
		return "no reason"
	}
	return reason
}
