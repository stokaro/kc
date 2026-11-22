package logs

import (
	"strings"

	models "github.com/sttts/kc/internal/models"
	corev1 "k8s.io/api/core/v1"
)

// DefaultContainerAnnotation names the container kubectl logs picks when a
// pod has several and none is given.
const DefaultContainerAnnotation = "kubectl.kubernetes.io/default-container"

// podContainer is implemented by container rows of a pod.
type podContainer interface {
	PodContainer() (namespace, pod, container string)
}

// Request is what a Source opens: one container of a pod, or all of them.
type Request struct {
	// Spec names the pod and the stream options. An empty Container means
	// the pod's default container, unless AllContainers is set.
	Spec models.LogsSpec
	// AllContainers merges the logs of every started container of the pod;
	// Spec.Container is empty then.
	AllContainers bool
}

// Target returns the log stream the selected item stands for: a container,
// a container's log row, a pod's all-containers log row, or a pod. For a
// pod Container is empty; the opener picks the default container with
// DefaultContainer. ok is false for every other item.
func Target(item models.Item) (req Request, ok bool) {
	switch it := item.(type) {
	case nil:
		return Request{}, false
	case models.LogsProvider:
		req.Spec = it.LogsSpec()
	case models.WorkloadLogsProvider:
		ws := it.WorkloadLogsSpec()
		if !ws.IsPod() {
			return Request{}, false
		}
		req.Spec = ws.LogsSpec()
		req.Spec.Container = ""
		req.AllContainers = true
	case podContainer:
		req.Spec.Namespace, req.Spec.Pod, req.Spec.Container = it.PodContainer()
	case models.ObjectItem:
		gvr := it.GVR()
		if gvr.Group != "" || gvr.Resource != "pods" {
			return Request{}, false
		}
		req.Spec.Namespace, req.Spec.Pod = it.Namespace(), it.Name()
	default:
		return Request{}, false
	}
	if req.Spec.Namespace == "" || req.Spec.Pod == "" {
		return Request{}, false
	}
	req.Spec.Follow = true
	if req.Spec.TailLines <= 0 {
		req.Spec.TailLines = models.DefaultLogsTailLines
	}
	return req, true
}

// ContainerNames returns the names of pod's init, main and ephemeral
// containers in that order, the containers `kubectl logs -c` accepts.
func ContainerNames(pod *corev1.Pod) []string {
	if pod == nil {
		return nil
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
	return names
}

// DefaultContainer returns the container kubectl logs reads when none is
// named: the one in the default-container annotation when the pod has it,
// else the first container. It returns "" for a pod without containers.
func DefaultContainer(pod *corev1.Pod) string {
	if pod == nil || len(pod.Spec.Containers) == 0 {
		return ""
	}
	if name := strings.TrimSpace(pod.Annotations[DefaultContainerAnnotation]); name != "" {
		for _, c := range pod.Spec.Containers {
			if c.Name == name {
				return name
			}
		}
	}
	return pod.Spec.Containers[0].Name
}

// podKey is the identity of the pod of spec. Pods are always core/v1.
func podKey(spec models.LogsSpec) string {
	return "v1/pods/" + spec.Namespace + "/" + spec.Pod
}

// targetID is the identity of a selected row's request: the pod, and the
// container or all containers it names.
func targetID(req Request) string {
	id := podKey(req.Spec) + "/" + req.Spec.Container
	if req.AllContainers {
		id += "/" + ContainerAll
	}
	return id
}
