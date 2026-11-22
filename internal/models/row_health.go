package models

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RowHealth is the state of an object as the row colors show it. The row
// stylers and the "faults only" filter both read it, so a colored row and a
// row the filter keeps are the same thing.
type RowHealth int

const (
	// RowHealthUnknown means kc has no rule for the kind or no status to read.
	RowHealthUnknown RowHealth = iota
	// RowHealthOK means the object is healthy.
	RowHealthOK
	// RowHealthSucceeded means the object finished its work (a Job).
	RowHealthSucceeded
	// RowHealthNotReady means the object runs but is not ready or available.
	RowHealthNotReady
	// RowHealthFailed means the object failed and will not recover by itself.
	RowHealthFailed
)

// Faulty reports whether the state is a fault.
func (h RowHealth) Faulty() bool {
	return h == RowHealthNotReady || h == RowHealthFailed
}

var (
	podsGVR         = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	nodesGVR        = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	pvcsGVR         = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	pvsGVR          = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	deploymentsGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	statefulsetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	replicasetsGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	daemonsetsGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	jobsGVR         = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

// healthRules classifies objects per resource.
var healthRules = map[schema.GroupVersionResource]func(*unstructured.Unstructured) RowHealth{
	podsGVR:         podHealth,
	nodesGVR:        nodeHealth,
	pvcsGVR:         pvcHealth,
	pvsGVR:          pvHealth,
	deploymentsGVR:  deploymentHealth,
	statefulsetsGVR: replicatedHealth,
	replicasetsGVR:  replicatedHealth,
	daemonsetsGVR:   daemonSetHealth,
	jobsGVR:         jobHealth,
}

// ClassifyRowHealth returns the state of the object u of resource gvr.
func ClassifyRowHealth(gvr schema.GroupVersionResource, u *unstructured.Unstructured) RowHealth {
	if u == nil {
		return RowHealthUnknown
	}
	rule, ok := healthRules[gvr]
	if !ok {
		return RowHealthUnknown
	}
	return rule(u)
}

// podWaitingOK are the waiting reasons of a container that is still starting.
var podWaitingOK = map[string]bool{
	"":                  true,
	"ContainerCreating": true,
	"PodInitializing":   true,
}

// podHealth: a pod is faulty when it is Pending, Failed or Unknown, when it
// runs but is not ready, or when a container waits for a reason other than
// starting (CrashLoopBackOff, ImagePullBackOff, ...). A Succeeded pod is
// fine; its Ready condition is False because its containers have exited.
func podHealth(u *unstructured.Unstructured) RowHealth {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	switch phase {
	case "Succeeded":
		return RowHealthOK
	case "Failed":
		return RowHealthFailed
	case "Pending", "Unknown":
		return RowHealthNotReady
	}
	for _, field := range []string{"initContainerStatuses", "containerStatuses"} {
		statuses, _, _ := unstructured.NestedSlice(u.Object, "status", field)
		for _, raw := range statuses {
			st, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if _, waiting, _ := unstructured.NestedMap(st, "state", "waiting"); waiting {
				reason, _, _ := unstructured.NestedString(st, "state", "waiting", "reason")
				if !podWaitingOK[reason] {
					return RowHealthNotReady
				}
			}
		}
	}
	switch readinessStatus(u, "Ready") {
	case conditionFalse:
		return RowHealthNotReady
	case conditionTrue:
		return RowHealthOK
	}
	if phase == "Running" {
		return RowHealthOK
	}
	return RowHealthUnknown
}

// nodeHealth: a node is faulty unless its Ready condition is True. A
// cordoned node is not a fault; it was cordoned on purpose.
func nodeHealth(u *unstructured.Unstructured) RowHealth {
	switch readinessStatus(u, "Ready") {
	case conditionTrue:
		return RowHealthOK
	case "":
		return RowHealthUnknown
	}
	return RowHealthNotReady
}

// pvcHealth: a claim is faulty unless it is Bound.
func pvcHealth(u *unstructured.Unstructured) RowHealth {
	switch phase, _, _ := unstructured.NestedString(u.Object, "status", "phase"); phase {
	case "Bound":
		return RowHealthOK
	case "Lost":
		return RowHealthFailed
	case "":
		return RowHealthUnknown
	}
	return RowHealthNotReady
}

// pvHealth: a volume is faulty when it Failed. Available and Released
// volumes are normal states.
func pvHealth(u *unstructured.Unstructured) RowHealth {
	switch phase, _, _ := unstructured.NestedString(u.Object, "status", "phase"); phase {
	case "Failed":
		return RowHealthFailed
	case "":
		return RowHealthUnknown
	}
	return RowHealthOK
}

// deploymentHealth: a deployment is faulty when Available is False or fewer
// replicas are ready or available than wanted.
func deploymentHealth(u *unstructured.Unstructured) RowHealth {
	if readinessStatus(u, "Available") == conditionFalse {
		return RowHealthNotReady
	}
	return replicatedHealth(u)
}

// replicatedHealth covers StatefulSets and ReplicaSets: faulty when fewer
// replicas are ready than spec.replicas asks for.
func replicatedHealth(u *unstructured.Unstructured) RowHealth {
	if readinessStatus(u, "Ready") == conditionFalse {
		return RowHealthNotReady
	}
	replicas, found, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	if !found {
		replicas = 1 // the API default
	}
	if replicas <= 0 {
		return RowHealthOK
	}
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	if ready < replicas {
		return RowHealthNotReady
	}
	if available, found, _ := unstructured.NestedInt64(u.Object, "status", "availableReplicas"); found && available < replicas {
		return RowHealthNotReady
	}
	return RowHealthOK
}

// daemonSetHealth: faulty when fewer pods are ready or available than
// scheduled.
func daemonSetHealth(u *unstructured.Unstructured) RowHealth {
	desired, _, _ := unstructured.NestedInt64(u.Object, "status", "desiredNumberScheduled")
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "numberReady")
	available, _, _ := unstructured.NestedInt64(u.Object, "status", "numberAvailable")
	if desired > 0 && (ready < desired || available < desired) {
		return RowHealthNotReady
	}
	if readinessStatus(u, "NumberAvailable") == conditionFalse {
		return RowHealthNotReady
	}
	return RowHealthOK
}

// jobHealth: a job succeeded when a pod succeeded, and failed when its
// Failed condition is True or the failures reached the backoff limit. A
// running job is fine.
func jobHealth(u *unstructured.Unstructured) RowHealth {
	succeeded, _, _ := unstructured.NestedInt64(u.Object, "status", "succeeded")
	if succeeded > 0 {
		return RowHealthSucceeded
	}
	if readinessStatus(u, "Failed") == conditionTrue {
		return RowHealthFailed
	}
	failed, _, _ := unstructured.NestedInt64(u.Object, "status", "failed")
	backoffLimit, _, _ := unstructured.NestedInt64(u.Object, "spec", "backoffLimit")
	if failed > 0 && backoffLimit > 0 && failed >= backoffLimit {
		return RowHealthFailed
	}
	return RowHealthOK
}
