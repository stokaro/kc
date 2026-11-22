package models

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// The resources the overview reads. Every one comes from the cache of the
// overview's cluster; none is listed from the API server directly.
var (
	ovNodesGVR        = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	ovPodsGVR         = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	ovEventsGVR       = schema.GroupVersionResource{Version: "v1", Resource: "events"}
	ovPVCsGVR         = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	ovPVsGVR          = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	ovQuotasGVR       = schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}
	ovDeploymentsGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	ovStatefulSetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	ovDaemonSetsGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	ovJobsGVR         = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	ovCronJobsGVR     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	ovPDBsGVR         = schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}
	ovHPAsGVR         = schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
)

// overviewKind is one resource the overview watches and lists.
type overviewKind struct {
	gvr        schema.GroupVersionResource
	namespaced bool
	newObject  func() crclient.Object
	newList    func() crclient.ObjectList
}

// overviewKinds lists every resource the overview reads, in a fixed order.
var overviewKinds = []overviewKind{
	{ovNodesGVR, false, func() crclient.Object { return &corev1.Node{} }, func() crclient.ObjectList { return &corev1.NodeList{} }},
	{ovPodsGVR, true, func() crclient.Object { return &corev1.Pod{} }, func() crclient.ObjectList { return &corev1.PodList{} }},
	{ovEventsGVR, true, func() crclient.Object { return &corev1.Event{} }, func() crclient.ObjectList { return &corev1.EventList{} }},
	{ovPVCsGVR, true, func() crclient.Object { return &corev1.PersistentVolumeClaim{} }, func() crclient.ObjectList { return &corev1.PersistentVolumeClaimList{} }},
	{ovPVsGVR, false, func() crclient.Object { return &corev1.PersistentVolume{} }, func() crclient.ObjectList { return &corev1.PersistentVolumeList{} }},
	{ovQuotasGVR, true, func() crclient.Object { return &corev1.ResourceQuota{} }, func() crclient.ObjectList { return &corev1.ResourceQuotaList{} }},
	{ovDeploymentsGVR, true, func() crclient.Object { return &appsv1.Deployment{} }, func() crclient.ObjectList { return &appsv1.DeploymentList{} }},
	{ovStatefulSetsGVR, true, func() crclient.Object { return &appsv1.StatefulSet{} }, func() crclient.ObjectList { return &appsv1.StatefulSetList{} }},
	{ovDaemonSetsGVR, true, func() crclient.Object { return &appsv1.DaemonSet{} }, func() crclient.ObjectList { return &appsv1.DaemonSetList{} }},
	{ovJobsGVR, true, func() crclient.Object { return &batchv1.Job{} }, func() crclient.ObjectList { return &batchv1.JobList{} }},
	{ovCronJobsGVR, true, func() crclient.Object { return &batchv1.CronJob{} }, func() crclient.ObjectList { return &batchv1.CronJobList{} }},
	{ovPDBsGVR, true, func() crclient.Object { return &policyv1.PodDisruptionBudget{} }, func() crclient.ObjectList { return &policyv1.PodDisruptionBudgetList{} }},
	{ovHPAsGVR, true, func() crclient.Object { return &autoscalingv2.HorizontalPodAutoscaler{} }, func() crclient.ObjectList { return &autoscalingv2.HorizontalPodAutoscalerList{} }},
}

// overviewInput is what one recompute reads from the cache. Pods are always
// listed across all namespaces: the capacity of a node depends on every pod
// on it. Everything else is listed in the overview's namespace scope.
type overviewInput struct {
	namespace    string // AllNamespaces or one namespace
	nodes        []corev1.Node
	pods         []corev1.Pod
	events       []corev1.Event
	pvcs         []corev1.PersistentVolumeClaim
	pvs          []corev1.PersistentVolume
	quotas       []corev1.ResourceQuota
	deployments  []appsv1.Deployment
	statefulSets []appsv1.StatefulSet
	daemonSets   []appsv1.DaemonSet
	jobs         []batchv1.Job
	cronJobs     []batchv1.CronJob
	pdbs         []policyv1.PodDisruptionBudget
	hpas         []autoscalingv2.HorizontalPodAutoscaler

	// missing holds the resources that could not be read, with the reason.
	// Rows that need them are shown dim with that reason.
	missing map[schema.GroupVersionResource]string
}

// inScope reports whether an object in namespace belongs to the overview's
// namespace scope.
func (in *overviewInput) inScope(namespace string) bool {
	return in.namespace == AllNamespaces || namespace == in.namespace
}

// missingReason returns why one of gvrs could not be read, or "".
func (in *overviewInput) missingReason(gvrs ...schema.GroupVersionResource) string {
	for _, gvr := range gvrs {
		if reason, ok := in.missing[gvr]; ok {
			return fmt.Sprintf("%s: %s", kubectlResource(gvr), reason)
		}
	}
	return ""
}

// loadOverviewInput lists every overview kind from r. unavailable returns a
// reason for a kind whose informer cannot serve it (not synced, not served);
// those kinds are not listed. The lists share the cache's objects, so the
// rules must not modify them.
func loadOverviewInput(ctx context.Context, r crclient.Reader, namespace string, unavailable func(schema.GroupVersionResource) string) *overviewInput {
	in := &overviewInput{namespace: namespace, missing: map[schema.GroupVersionResource]string{}}
	for _, k := range overviewKinds {
		if unavailable != nil {
			if reason := unavailable(k.gvr); reason != "" {
				in.missing[k.gvr] = reason
				continue
			}
		}
		list := k.newList()
		opts := []crclient.ListOption{crclient.UnsafeDisableDeepCopy}
		if k.namespaced && namespace != AllNamespaces && k.gvr != ovPodsGVR {
			opts = append(opts, crclient.InNamespace(namespace))
		}
		if err := r.List(ctx, list, opts...); err != nil {
			in.missing[k.gvr] = err.Error()
			continue
		}
		in.assign(list)
	}
	return in
}

func (in *overviewInput) assign(list crclient.ObjectList) {
	switch l := list.(type) {
	case *corev1.NodeList:
		in.nodes = l.Items
	case *corev1.PodList:
		in.pods = l.Items
	case *corev1.EventList:
		in.events = l.Items
	case *corev1.PersistentVolumeClaimList:
		in.pvcs = l.Items
	case *corev1.PersistentVolumeList:
		in.pvs = l.Items
	case *corev1.ResourceQuotaList:
		in.quotas = l.Items
	case *appsv1.DeploymentList:
		in.deployments = l.Items
	case *appsv1.StatefulSetList:
		in.statefulSets = l.Items
	case *appsv1.DaemonSetList:
		in.daemonSets = l.Items
	case *batchv1.JobList:
		in.jobs = l.Items
	case *batchv1.CronJobList:
		in.cronJobs = l.Items
	case *policyv1.PodDisruptionBudgetList:
		in.pdbs = l.Items
	case *autoscalingv2.HorizontalPodAutoscalerList:
		in.hpas = l.Items
	}
}
