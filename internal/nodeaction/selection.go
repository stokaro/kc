package nodeaction

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The messages match kubectl drain (k8s.io/kubectl/pkg/drain/filters.go),
// so a refusal reads the same in kc and in a terminal.
const (
	daemonSetFatal    = "DaemonSet-managed Pods (use --ignore-daemonsets to ignore)"
	daemonSetWarning  = "ignoring DaemonSet-managed Pods"
	localStorageFatal = "Pods with local storage (use --delete-emptydir-data to override)"
	localStorageNote  = "deleting Pods with local storage"
	unmanagedFatal    = "cannot delete Pods that declare no controller (use --force to override)"
	unmanagedWarning  = "deleting Pods that declare no controller"
	mirrorPodNote     = "mirror Pod (static Pod of the kubelet)"
)

// PodNote names a pod and why it is skipped, refused or warned about.
type PodNote struct {
	Namespace string
	Name      string
	Message   string
}

// String returns "namespace/name: message".
func (n PodNote) String() string {
	return n.Namespace + "/" + n.Name + ": " + n.Message
}

// Selection is the verdict on the pods of one node.
type Selection struct {
	// Evict are the pods the drain evicts.
	Evict []corev1.Pod
	// Skipped are pods the drain leaves alone: DaemonSet pods (with
	// IgnoreDaemonSets) and mirror pods.
	Skipped []PodNote
	// Blocked are pods that stop the drain; kubectl refuses them too.
	Blocked []PodNote
	// Warnings are evicted pods worth a line in the dialog: emptyDir data
	// that is lost, unmanaged pods that do not come back.
	Warnings []PodNote
}

// DaemonSetExists reports whether the DaemonSet namespace/name exists.
type DaemonSetExists func(namespace, name string) (bool, error)

// SelectPods applies kubectl drain's rules, in kubectl's order:
//
//  1. A pod controlled by a DaemonSet is skipped with IgnoreDaemonSets and
//     refused without it, unless it has finished. If its DaemonSet is gone,
//     Force evicts it and no Force refuses it.
//  2. A mirror pod is skipped: the API cannot remove it.
//  3. A running pod with an emptyDir volume is refused unless
//     DeleteEmptyDirData.
//  4. A running pod without a controller is refused unless Force.
//
// The first rule that does not evict a pod decides; the later ones do not
// run for it.
func SelectPods(pods []corev1.Pod, opts DrainOptions, dsExists DaemonSetExists) Selection {
	var s Selection
	for _, pod := range pods {
		note := func(msg string) PodNote {
			return PodNote{Namespace: pod.Namespace, Name: pod.Name, Message: msg}
		}
		finished := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
		controller := metav1.GetControllerOf(&pod)
		var warning string

		if controller != nil && isDaemonSet(controller) && !finished {
			exists, err := dsExists(pod.Namespace, controller.Name)
			switch {
			case err != nil:
				s.Blocked = append(s.Blocked, note(err.Error()))
				continue
			case !exists && opts.Force:
				warning = "DaemonSet " + controller.Name + " not found"
			case !exists:
				s.Blocked = append(s.Blocked, note("DaemonSet "+controller.Name+" not found (use --force to override)"))
				continue
			case opts.IgnoreDaemonSets:
				s.Skipped = append(s.Skipped, note(daemonSetWarning))
				continue
			default:
				s.Blocked = append(s.Blocked, note(daemonSetFatal))
				continue
			}
		}
		if _, mirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
			s.Skipped = append(s.Skipped, note(mirrorPodNote))
			continue
		}
		if hasEmptyDir(pod) && !finished {
			if !opts.DeleteEmptyDirData {
				s.Blocked = append(s.Blocked, note(localStorageFatal))
				continue
			}
			warning = localStorageNote
		}
		if controller == nil && !finished {
			if !opts.Force {
				s.Blocked = append(s.Blocked, note(unmanagedFatal))
				continue
			}
			warning = unmanagedWarning
		}
		if warning != "" {
			s.Warnings = append(s.Warnings, note(warning))
		}
		s.Evict = append(s.Evict, pod)
	}
	return s
}

func isDaemonSet(ref *metav1.OwnerReference) bool {
	return ref.Kind == "DaemonSet" && ref.APIVersion == "apps/v1"
}

func hasEmptyDir(pod corev1.Pod) bool {
	for _, v := range pod.Spec.Volumes {
		if v.EmptyDir != nil {
			return true
		}
	}
	return false
}
