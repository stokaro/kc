package models

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// InfoWorkload names one workload by its full GVR, namespace and name. The
// Info panel mode shows bars for it when the other panel's cursor is on it.
type InfoWorkload struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
}

// String is "deployments.apps shop/web", as kubectl names the object.
func (w InfoWorkload) String() string {
	return kubectlResource(w.GVR) + " " + w.Namespace + "/" + w.Name
}

// workloadKinds maps the owner kinds the overview follows to their GVR.
// Owner references name a kind; the overview's lists know these GVRs.
var workloadKinds = map[schema.GroupKind]schema.GroupVersionResource{
	{Group: "apps", Kind: "Deployment"}:  ovDeploymentsGVR,
	{Group: "apps", Kind: "ReplicaSet"}:  ovReplicaSetsGVR,
	{Group: "apps", Kind: "StatefulSet"}: ovStatefulSetsGVR,
	{Group: "apps", Kind: "DaemonSet"}:   ovDaemonSetsGVR,
	{Group: "batch", Kind: "Job"}:        ovJobsGVR,
	{Group: "batch", Kind: "CronJob"}:    ovCronJobsGVR,
}

// IsInfoWorkload reports whether the Info mode can show bars for objects
// of gvr: the built-in controllers that own pods.
func IsInfoWorkload(gvr schema.GroupVersionResource) bool {
	for _, g := range workloadKinds {
		if g == gvr {
			return true
		}
	}
	return false
}

// overviewWorkloadSummary is what the pods of one workload use and ask for.
// A pod counts for every workload in its chain of controllers: a pod of a
// Deployment counts for its ReplicaSet and for the Deployment.
type overviewWorkloadSummary struct {
	pods, ready, measured  int64 // pods that have not terminated
	desired                int64 // replicas the workload asks for
	hasDesired             bool
	cpuUse, cpuReq, cpuLim int64 // millicores
	memUse, memReq, memLim int64 // bytes
	// cpuUnlimited and memUnlimited are true when a pod has an app
	// container without that limit (and no pod-level limit): the limit sum
	// is then not a ceiling.
	cpuUnlimited, memUnlimited bool
}

// workloadIndex resolves the controllers of a pod from the overview's
// lists. Each list is nil when it cannot be read.
type workloadIndex struct {
	objects map[schema.GroupVersionResource]map[string]*metav1.ObjectMeta
}

func objectKey(namespace, name string) string { return namespace + "/" + name }

func newWorkloadIndex(rc *ruleContext) *workloadIndex {
	idx := &workloadIndex{objects: make(map[schema.GroupVersionResource]map[string]*metav1.ObjectMeta)}
	add := func(gvr schema.GroupVersionResource, n int, meta func(int) *metav1.ObjectMeta) {
		if rc.in.missingReason(gvr) != "" {
			return
		}
		m := make(map[string]*metav1.ObjectMeta, n)
		for i := range n {
			om := meta(i)
			m[objectKey(om.Namespace, om.Name)] = om
		}
		idx.objects[gvr] = m
	}
	add(ovDeploymentsGVR, len(rc.in.deployments), func(i int) *metav1.ObjectMeta { return &rc.in.deployments[i].ObjectMeta })
	add(ovReplicaSetsGVR, len(rc.in.replicaSets), func(i int) *metav1.ObjectMeta { return &rc.in.replicaSets[i].ObjectMeta })
	add(ovStatefulSetsGVR, len(rc.in.statefulSets), func(i int) *metav1.ObjectMeta { return &rc.in.statefulSets[i].ObjectMeta })
	add(ovDaemonSetsGVR, len(rc.in.daemonSets), func(i int) *metav1.ObjectMeta { return &rc.in.daemonSets[i].ObjectMeta })
	add(ovJobsGVR, len(rc.in.jobs), func(i int) *metav1.ObjectMeta { return &rc.in.jobs[i].ObjectMeta })
	add(ovCronJobsGVR, len(rc.in.cronJobs), func(i int) *metav1.ObjectMeta { return &rc.in.cronJobs[i].ObjectMeta })
	return idx
}

// maxOwnerDepth bounds the walk up the controller chain; the deepest
// built-in chain is Pod, Job, CronJob.
const maxOwnerDepth = 4

// owners returns the workloads in the controller chain of an object in
// namespace whose controller reference is ref, nearest first. The walk
// follows only the kinds of workloadKinds. An owner whose list is read must
// be in it with the UID of the reference (a recreated owner with the same
// name is not the owner); an owner whose list cannot be read is trusted
// from the reference, and the walk stops there.
func (idx *workloadIndex) owners(namespace string, ref *metav1.OwnerReference) []InfoWorkload {
	var out []InfoWorkload
	for depth := 0; ref != nil && depth < maxOwnerDepth; depth++ {
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			break
		}
		gvr, ok := workloadKinds[schema.GroupKind{Group: gv.Group, Kind: ref.Kind}]
		if !ok {
			break
		}
		w := InfoWorkload{GVR: gvr, Namespace: namespace, Name: ref.Name}
		objs, listed := idx.objects[gvr]
		if !listed {
			out = append(out, w)
			break
		}
		meta, found := objs[objectKey(namespace, ref.Name)]
		if !found || (ref.UID != "" && meta.UID != ref.UID) {
			break
		}
		out = append(out, w)
		ref = metav1.GetControllerOfNoCopy(meta)
	}
	return out
}

// summarizeWorkloads groups the pods in scope that have not terminated by
// their controllers. It also returns each pod's workloads by pod key, for
// the consumers of one workload. Workloads with desired replicas but no
// pods are listed too.
func summarizeWorkloads(rc *ruleContext) (map[InfoWorkload]*overviewWorkloadSummary, map[string][]InfoWorkload) {
	if rc.in.missingReason(ovPodsGVR) != "" {
		return nil, nil
	}
	idx := newWorkloadIndex(rc)
	out := make(map[InfoWorkload]*overviewWorkloadSummary)
	get := func(w InfoWorkload) *overviewWorkloadSummary {
		s := out[w]
		if s == nil {
			s = &overviewWorkloadSummary{}
			out[w] = s
		}
		return s
	}
	desired := func(gvr schema.GroupVersionResource, m metav1.ObjectMeta, n int32) {
		if !rc.in.inScope(m.Namespace) {
			return
		}
		s := get(InfoWorkload{GVR: gvr, Namespace: m.Namespace, Name: m.Name})
		s.desired, s.hasDesired = int64(n), true
	}
	for i := range rc.in.deployments {
		d := &rc.in.deployments[i]
		desired(ovDeploymentsGVR, d.ObjectMeta, replicasOf(d.Spec.Replicas))
	}
	for i := range rc.in.statefulSets {
		s := &rc.in.statefulSets[i]
		desired(ovStatefulSetsGVR, s.ObjectMeta, replicasOf(s.Spec.Replicas))
	}
	for i := range rc.in.daemonSets {
		d := &rc.in.daemonSets[i]
		desired(ovDaemonSetsGVR, d.ObjectMeta, d.Status.DesiredNumberScheduled)
	}

	byPod := make(map[string][]InfoWorkload)
	rc.scopedPods(func(p *corev1.Pod) {
		if podTerminal(p) {
			return
		}
		ws := idx.owners(p.Namespace, metav1.GetControllerOfNoCopy(p))
		if len(ws) == 0 {
			return
		}
		key := usageObjectKey(p.Namespace, p.Name)
		byPod[key] = ws
		req, lim := podRequestsAndLimits(p)
		used, measured := rc.m.pods[key]
		var cpuUse, memUse int64
		for _, c := range used {
			cpuUse += c.cpu
			memUse += c.mem
		}
		ready := false
		if c := podCondition(p, corev1.PodReady); c != nil && c.Status == corev1.ConditionTrue {
			ready = true
		}
		cpuUnlimited, memUnlimited := podUnlimited(p, corev1.ResourceCPU), podUnlimited(p, corev1.ResourceMemory)
		for _, w := range ws {
			s := get(w)
			s.pods++
			if ready {
				s.ready++
			}
			if measured {
				s.measured++
				s.cpuUse += cpuUse
				s.memUse += memUse
			}
			s.cpuReq += req.Cpu().MilliValue()
			s.memReq += req.Memory().Value()
			s.cpuLim += lim.Cpu().MilliValue()
			s.memLim += lim.Memory().Value()
			s.cpuUnlimited = s.cpuUnlimited || cpuUnlimited
			s.memUnlimited = s.memUnlimited || memUnlimited
		}
	})
	return out, byPod
}

// podUnlimited reports whether an app container of p has no limit for
// resource and the pod sets no pod-level limit for it.
func podUnlimited(p *corev1.Pod, resource corev1.ResourceName) bool {
	if p.Spec.Resources != nil {
		if _, ok := p.Spec.Resources.Limits[resource]; ok {
			return false
		}
	}
	for _, c := range p.Spec.Containers {
		if _, ok := c.Resources.Limits[resource]; !ok {
			return true
		}
	}
	return false
}
