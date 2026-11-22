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

// Target returns the log stream the selected item stands for: a container,
// a container's log row, or a pod. For a pod Container is empty; the opener
// picks the default container with DefaultContainer. ok is false for every
// other item.
func Target(item models.Item) (spec models.LogsSpec, ok bool) {
	switch it := item.(type) {
	case nil:
		return models.LogsSpec{}, false
	case models.LogsProvider:
		spec = it.LogsSpec()
	case podContainer:
		spec.Namespace, spec.Pod, spec.Container = it.PodContainer()
	case models.ObjectItem:
		gvr := it.GVR()
		if gvr.Group != "" || gvr.Resource != "pods" {
			return models.LogsSpec{}, false
		}
		spec.Namespace, spec.Pod = it.Namespace(), it.Name()
	default:
		return models.LogsSpec{}, false
	}
	if spec.Namespace == "" || spec.Pod == "" {
		return models.LogsSpec{}, false
	}
	spec.Follow = true
	if spec.TailLines <= 0 {
		spec.TailLines = models.DefaultLogsTailLines
	}
	return spec, true
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

// key is the identity of a stream target. Pods are always core/v1.
func key(spec models.LogsSpec) string {
	return "v1/pods/" + spec.Namespace + "/" + spec.Pod + "/" + spec.Container
}
