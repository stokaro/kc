package models

import (
	corev1 "k8s.io/api/core/v1"
	resourcehelper "k8s.io/component-helpers/resource"
)

// podRequestsAndLimits returns the effective requests and limits of p as the
// scheduler and kubectl describe node compute them from the spec: the larger
// of the init containers and the app containers (sidecars included), plus
// pod overhead and pod-level resources.
func podRequestsAndLimits(p *corev1.Pod) (corev1.ResourceList, corev1.ResourceList) {
	opts := resourcehelper.PodResourcesOptions{}
	return resourcehelper.PodRequests(p, opts), resourcehelper.PodLimits(p, opts)
}
