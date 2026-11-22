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

// PodTargets returns a target for every container of pod that has started:
// init, main and ephemeral containers, as `kubectl logs --all-containers`
// does. A container that is still waiting has no log yet; the API refuses
// the request ("container is waiting to start"), so it is left out until
// its status changes.
func PodTargets(pod *corev1.Pod) []Target {
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
	var out []Target
	for _, name := range names {
		st, ok := statuses[name]
		if !ok || (st.State.Running == nil && st.State.Terminated == nil) {
			continue
		}
		out = append(out, Target{
			Namespace: pod.Namespace,
			Pod:       pod.Name,
			Container: name,
			PodUID:    pod.UID,
			Restarts:  st.RestartCount,
		})
	}
	return out
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
