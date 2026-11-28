// Package logmux merges the log streams of several containers into one
// stream of prefixed lines, the way `kubectl logs -l <selector>
// --all-containers --prefix -f` does. It opens one stream per container,
// limits how many run at once, follows pods as they come and go, and stops
// everything on Close.
package logmux

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Target is one container instance whose log is streamed.
//
// PodUID and Restarts tell instances apart: a pod recreated under the same
// name, or a container that restarted, is a new target with a new stream,
// while the stream of the old instance ends on its own.
type Target struct {
	Namespace string
	Pod       string
	Container string
	PodUID    types.UID
	Restarts  int32
}

// Name is the `pod/container` label used for the line prefix and its color.
func (t Target) Name() string { return t.Pod + "/" + t.Container }

// Candidate is a target of the set and what its container does, as the pod
// status says. The state is not part of the target's identity: a container
// that starts, or waits after a crash, keeps its target.
type Candidate struct {
	Target
	// Waiting is why the container waits, such as CrashLoopBackOff,
	// ImagePullBackOff or PodInitializing. It is empty while the container
	// runs and after it terminated, when it has a log to read.
	Waiting string
	// Previous marks a waiting container that ran before: its last
	// terminated instance, the one Restarts counts, still has a log, which
	// `kubectl logs --previous` reads.
	Previous bool
	// Done is set instead of Waiting for a container that has no log while
	// its pod is over (phase Failed or Succeeded): nothing will run it, so
	// it never gets one. It names the pod's end, such as
	// "pod Failed (OutOfcpu)".
	Done string
}

// waitingUnknown is the reason of a waiting container whose status gives
// none.
const waitingUnknown = "Waiting"

// PodCandidates returns a candidate for every container of pod: init, main
// and ephemeral containers, as `kubectl logs --all-containers` does,
// whether they started or not. A container the status does not list yet
// waits with the reason the pod is not scheduled (Unschedulable), or
// Pending. In a pod that is over, a container without a log is Done.
func PodCandidates(pod *corev1.Pod) []Candidate {
	if pod == nil {
		return nil
	}
	statuses := make(map[string]corev1.ContainerStatus)
	for _, list := range [][]corev1.ContainerStatus{
		pod.Status.InitContainerStatuses,
		pod.Status.ContainerStatuses,
		pod.Status.EphemeralContainerStatuses,
	} {
		for _, st := range list {
			statuses[st.Name] = st
		}
	}
	names := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers)+len(pod.Spec.EphemeralContainers))
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.EphemeralContainers {
		names = append(names, c.Name)
	}
	out := make([]Candidate, 0, len(names))
	for _, name := range names {
		st, ok := statuses[name]
		c := Candidate{
			Namespace: pod.Namespace,
			Pod:       pod.Name,
			Container: name,
			PodUID:    pod.UID,
			Restarts:  st.RestartCount,
		}
		switch {
		case !ok:
			c.Waiting = unreportedReason(pod)
		case st.State.Running != nil || st.State.Terminated != nil:
		default:
			c.Waiting = waitingUnknown
			if w := st.State.Waiting; w != nil && w.Reason != "" {
				c.Waiting = w.Reason
			}
			// The kubelet finds the previous log by this container ID; a
			// status it lost (ContainerStatusUnknown) has none.
			last := st.LastTerminationState.Terminated
			c.Previous = last != nil && last.ContainerID != ""
		}
		// A pod the kubelet rejected (OutOfcpu) or that failed before its
		// init containers finished keeps such containers waiting, or
		// unlisted, for good.
		if c.Waiting != "" && !c.Previous && podOver(pod) {
			c.Waiting, c.Done = "", podEnd(pod)
		}
		out = append(out, c)
	}
	return out
}

func podOver(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded
}

// podEnd names how a pod that is over ended: "pod Failed (OutOfcpu)".
func podEnd(pod *corev1.Pod) string {
	end := "pod " + string(pod.Status.Phase)
	if pod.Status.Reason != "" {
		end += " (" + pod.Status.Reason + ")"
	}
	return end
}

// unreportedReason is why a container the pod status does not list waits.
func unreportedReason(pod *corev1.Pod) string {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason != "" {
			return c.Reason
		}
	}
	return "Pending"
}

// sortTargets orders targets by namespace, then name, so that the streams
// that start first under the limit do not depend on map order.
func sortTargets(ts []Target) {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].Namespace != ts[j].Namespace {
			return ts[i].Namespace < ts[j].Namespace
		}
		if ts[i].Name() != ts[j].Name() {
			return ts[i].Name() < ts[j].Name()
		}
		if ts[i].PodUID != ts[j].PodUID {
			return ts[i].PodUID < ts[j].PodUID
		}
		return ts[i].Restarts < ts[j].Restarts
	})
}
