package models

import (
	"slices"
	"time"

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

// workloadKinds maps the built-in owner kinds the overview lists itself to
// their GVR. Owner references name a kind; the overview's lists know these
// GVRs. The walk follows other kinds too (overview_owners.go).
var workloadKinds = map[schema.GroupKind]schema.GroupVersionResource{
	{Group: "apps", Kind: "Deployment"}:  ovDeploymentsGVR,
	{Group: "apps", Kind: "ReplicaSet"}:  ovReplicaSetsGVR,
	{Group: "apps", Kind: "StatefulSet"}: ovStatefulSetsGVR,
	{Group: "apps", Kind: "DaemonSet"}:   ovDaemonSetsGVR,
	{Group: "batch", Kind: "Job"}:        ovJobsGVR,
	{Group: "batch", Kind: "CronJob"}:    ovCronJobsGVR,
}

// IsInfoWorkload reports whether gvr is one of the built-in controllers
// that own pods. IsWorkloadTarget adds the kinds learned from the cluster.
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
	pods, ready, measured int64 // pods that have not terminated
	desired               int64 // replicas the workload asks for
	hasDesired            bool
	// desiredNote says why a workload of a kind outside workloadKinds has
	// no desired count: the kind declares none, or it cannot be read.
	desiredNote            string
	cpuUse, cpuReq, cpuLim int64 // millicores
	memUse, memReq, memLim int64 // bytes
	// cpuUnlimited and memUnlimited are true when a pod has an app
	// container without that limit (and no pod-level limit): the limit sum
	// is then not a ceiling.
	cpuUnlimited, memUnlimited bool
	// usageAt is the oldest timestamp of the PodMetrics summed into the
	// use: the use is never dated later than any part of it. Zero when no
	// measured pod carries a timestamp.
	usageAt time.Time
}

// dateBy folds the sample time of one measured pod into s; a zero time
// (a pod without a timestamp) changes nothing.
func (s *overviewWorkloadSummary) dateBy(at time.Time) {
	if !at.IsZero() && (s.usageAt.IsZero() || at.Before(s.usageAt)) {
		s.usageAt = at
	}
}

// workloadIndex resolves the controllers of a pod from the overview's
// lists. Each list is nil when it cannot be read.
type workloadIndex struct {
	objects map[schema.GroupVersionResource]map[string]*metav1.ObjectMeta
	// owners are the kinds outside workloadKinds the engine resolved.
	ownerKinds map[schema.GroupKind]ownerKind
	// kindOf names the kind of every GVR the walk can reach.
	kindOf map[schema.GroupVersionResource]schema.GroupKind
	// met collects the kinds the walks reached that nothing resolves yet;
	// the engine resolves them for the next recompute.
	met map[schema.GroupKind]bool
}

func objectKey(namespace, name string) string { return namespace + "/" + name }

func newWorkloadIndex(rc *ruleContext) *workloadIndex {
	idx := &workloadIndex{
		objects:    make(map[schema.GroupVersionResource]map[string]*metav1.ObjectMeta),
		ownerKinds: rc.in.owners.kinds,
		kindOf:     make(map[schema.GroupVersionResource]schema.GroupKind),
		met:        make(map[schema.GroupKind]bool),
	}
	for gk, gvr := range workloadKinds {
		idx.kindOf[gvr] = gk
	}
	for gk, k := range rc.in.owners.kinds {
		if k.namespaced {
			idx.kindOf[k.gvr] = gk
		}
	}
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
	for gvr, objs := range rc.in.owners.objects {
		add(gvr, len(objs), func(i int) *metav1.ObjectMeta { return objs[i].meta })
	}
	return idx
}

// maxOwnerDepth bounds the walk up the controller chain. The deepest chains
// seen are Knative's: Pod, ReplicaSet, Deployment, Revision, Configuration,
// Service.
const maxOwnerDepth = 8

// isWorkload reports whether objects of gvr are workloads the walk reaches:
// a built-in controller or a resolved namespaced owner kind.
func (idx *workloadIndex) isWorkload(gvr schema.GroupVersionResource) bool {
	_, ok := idx.kindOf[gvr]
	return ok
}

// resolve returns the GVR of the owner ref names when the walk can follow
// it. A kind nothing resolves yet is recorded in met; a cluster-scoped kind
// or one that is not served ends the walk.
func (idx *workloadIndex) resolve(ref *metav1.OwnerReference) (schema.GroupVersionResource, bool) {
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return schema.GroupVersionResource{}, false
	}
	gk := schema.GroupKind{Group: gv.Group, Kind: ref.Kind}
	if gvr, ok := workloadKinds[gk]; ok {
		return gvr, true
	}
	if k, ok := idx.ownerKinds[gk]; ok {
		return k.gvr, k.namespaced
	}
	idx.met[gk] = true
	return schema.GroupVersionResource{}, false
}

// owners returns the workloads in the controller chain of an object in
// namespace whose controller reference is ref, nearest first. The walk
// follows any namespaced kind the engine resolved. An owner whose list is
// read must be in it with the UID of the reference (a recreated owner with
// the same name is not the owner); an owner whose list cannot be read is
// trusted from the reference, and the walk stops there. An owner met twice
// (a cycle of references) ends the walk.
func (idx *workloadIndex) owners(namespace string, ref *metav1.OwnerReference) []InfoWorkload {
	var out []InfoWorkload
	for depth := 0; ref != nil && depth < maxOwnerDepth; depth++ {
		gvr, ok := idx.resolve(ref)
		if !ok {
			break
		}
		w := InfoWorkload{GVR: gvr, Namespace: namespace, Name: ref.Name}
		if slices.Contains(out, w) {
			break
		}
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

// learnChain records in seen how the pods reach each workload of chain
// whose kind is not built in: the kinds between the pod and it.
func (idx *workloadIndex) learnChain(seen map[schema.GroupVersionResource]*chainKind, chain []InfoWorkload) {
	for i, w := range chain {
		if IsInfoWorkload(w.GVR) {
			continue
		}
		k := seen[w.GVR]
		if k == nil {
			k = &chainKind{kind: idx.kindOf[w.GVR].Kind, vias: make(map[string][]schema.GroupKind)}
			seen[w.GVR] = k
		}
		via := make([]schema.GroupKind, i)
		for j := range i {
			via[j] = idx.kindOf[chain[j].GVR]
		}
		k.vias[viaText(via)] = via
	}
}

// workloadLearning is what the walks of one recompute found for the engine:
// the owner kinds to resolve and the custom kinds seen in chains.
type workloadLearning struct {
	met    map[schema.GroupKind]bool
	chains map[schema.GroupVersionResource]*chainKind
}

// summarizeWorkloads groups the pods in scope that have not terminated by
// their controllers. It also returns each pod's workloads by pod key, for
// the consumers of one workload, and what the walks learned. Workloads with
// desired replicas but no pods are listed too.
func summarizeWorkloads(rc *ruleContext) (map[InfoWorkload]*overviewWorkloadSummary, map[string][]InfoWorkload, workloadLearning) {
	if rc.in.missingReason(ovPodsGVR) != "" {
		return nil, nil, workloadLearning{}
	}
	idx := newWorkloadIndex(rc)
	learnt := workloadLearning{met: idx.met, chains: make(map[schema.GroupVersionResource]*chainKind)}
	out := make(map[InfoWorkload]*overviewWorkloadSummary)
	get := func(w InfoWorkload) *overviewWorkloadSummary {
		s := out[w]
		if s == nil {
			s = &overviewWorkloadSummary{}
			out[w] = s
		}
		return s
	}
	addDesired(rc, get)

	byPod := make(map[string][]InfoWorkload)
	rc.scopedPods(func(p *corev1.Pod) {
		if podTerminal(p) {
			return
		}
		ws := idx.owners(p.Namespace, metav1.GetControllerOfNoCopy(p))
		if len(ws) == 0 {
			return
		}
		idx.learnChain(learnt.chains, ws)
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
				s.dateBy(rc.m.podsTiming.sampled[key])
			}
			s.cpuReq += req.Cpu().MilliValue()
			s.memReq += req.Memory().Value()
			s.cpuLim += lim.Cpu().MilliValue()
			s.memLim += lim.Memory().Value()
			s.cpuUnlimited = s.cpuUnlimited || cpuUnlimited
			s.memUnlimited = s.memUnlimited || memUnlimited
		}
	})
	for w, s := range out {
		if !s.hasDesired && !IsInfoWorkload(w.GVR) {
			s.desiredNote = desiredNote(rc.in, w)
		}
	}
	return out, byPod, learnt
}

// addDesired sets the desired replicas of the workloads in scope that have
// a count, through get.
func addDesired(rc *ruleContext, get func(InfoWorkload) *overviewWorkloadSummary) {
	desired := func(gvr schema.GroupVersionResource, m *metav1.ObjectMeta, n int64) {
		if !rc.in.inScope(m.Namespace) {
			return
		}
		s := get(InfoWorkload{GVR: gvr, Namespace: m.Namespace, Name: m.Name})
		s.desired, s.hasDesired = n, true
	}
	for i := range rc.in.deployments {
		d := &rc.in.deployments[i]
		desired(ovDeploymentsGVR, &d.ObjectMeta, int64(replicasOf(d.Spec.Replicas)))
	}
	for i := range rc.in.statefulSets {
		s := &rc.in.statefulSets[i]
		desired(ovStatefulSetsGVR, &s.ObjectMeta, int64(replicasOf(s.Spec.Replicas)))
	}
	for i := range rc.in.daemonSets {
		d := &rc.in.daemonSets[i]
		desired(ovDaemonSetsGVR, &d.ObjectMeta, int64(d.Status.DesiredNumberScheduled))
	}
	// A custom kind with a scale path is listed like a Deployment: an
	// object scaled to 0 is a workload without pods.
	for gvr, objs := range rc.in.owners.objects {
		for i := range objs {
			if o := &objs[i]; o.hasDesired {
				desired(gvr, o.meta, o.desired)
			}
		}
	}
}

// desiredNote says why a workload of a kind outside workloadKinds has no
// desired count.
func desiredNote(in *overviewInput, w InfoWorkload) string {
	k, ok := in.owners.kindOf(w.GVR)
	switch {
	case !ok:
		return "desired count unknown: " + kubectlResource(w.GVR) + " is not resolved yet"
	case !k.hasScale:
		return "no desired count: " + kubectlResource(w.GVR) + " declares no scale subresource"
	case len(k.scalePath) == 0:
		return "desired count unknown: " + k.scaleNote
	}
	if reason := in.missingReason(w.GVR); reason != "" {
		return "desired count unknown: " + reason
	}
	for _, o := range in.owners.objects[w.GVR] {
		if o.meta.Namespace == w.Namespace && o.meta.Name == w.Name {
			return "desired count unknown: " + o.desiredNote
		}
	}
	return "desired count unknown: " + w.String() + " is not listed"
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
