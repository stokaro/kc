package models

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	relDeploymentsGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	relReplicaSetsGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	relStatefulSetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	relDaemonSetsGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	relJobsGVR         = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	relCronJobsGVR     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	relNodesGVR        = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
)

// Dependency is an object that another object names in its spec: a forward
// edge. Namespace is empty for a cluster-scoped target.
type Dependency struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	// Via is the field that names the target, such as "volumes[cfg]" or
	// "spec.nodeName".
	Via string
}

// DependsOnFinder lists the objects an object of one GVR names. It reads only
// the object itself, so it needs no cache.
type DependsOnFinder func(obj *unstructured.Unstructured) ([]Dependency, error)

var (
	dependsOnOnce sync.Once
	dependsOnMu   sync.RWMutex
	dependsOnMap  map[schema.GroupVersionResource]DependsOnFinder
)

func loadDependsOn() {
	dependsOnOnce.Do(func() { dependsOnMap = builtinDependsOn() })
}

// RegisterDependsOn registers how to find what objects of a GVR depend on.
func RegisterDependsOn(gvr schema.GroupVersionResource, f DependsOnFinder) {
	loadDependsOn()
	dependsOnMu.Lock()
	dependsOnMap[gvr] = f
	dependsOnMu.Unlock()
}

// DependsOnFor returns the finder for a GVR when one is registered.
func DependsOnFor(gvr schema.GroupVersionResource) (DependsOnFinder, bool) {
	loadDependsOn()
	dependsOnMu.RLock()
	f, ok := dependsOnMap[gvr]
	dependsOnMu.RUnlock()
	return f, ok
}

func builtinDependsOn() map[schema.GroupVersionResource]DependsOnFinder {
	return map[schema.GroupVersionResource]DependsOnFinder{
		podGVR:             podDependencies,
		relDeploymentsGVR:  templateDependencies(func(o *appsv1.Deployment) *corev1.PodSpec { return &o.Spec.Template.Spec }),
		relReplicaSetsGVR:  templateDependencies(func(o *appsv1.ReplicaSet) *corev1.PodSpec { return &o.Spec.Template.Spec }),
		relStatefulSetsGVR: templateDependencies(func(o *appsv1.StatefulSet) *corev1.PodSpec { return &o.Spec.Template.Spec }),
		relDaemonSetsGVR:   templateDependencies(func(o *appsv1.DaemonSet) *corev1.PodSpec { return &o.Spec.Template.Spec }),
		relJobsGVR:         templateDependencies(func(o *batchv1.Job) *corev1.PodSpec { return &o.Spec.Template.Spec }),
		relCronJobsGVR: templateDependencies(func(o *batchv1.CronJob) *corev1.PodSpec {
			return &o.Spec.JobTemplate.Spec.Template.Spec
		}),
		pvcGVR: claimDependencies,
	}
}

// podDependencies lists what a pod names (see PodReferences) and the node
// it is scheduled on.
func podDependencies(obj *unstructured.Unstructured) ([]Dependency, error) {
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
		return nil, err
	}
	refs := PodReferences(&pod)
	out := make([]Dependency, 0, len(refs)+1)
	for _, r := range refs {
		out = append(out, Dependency{GVR: r.GVR, Namespace: pod.Namespace, Name: r.Name, Via: r.Via})
	}
	if pod.Spec.NodeName != "" {
		out = append(out, Dependency{GVR: relNodesGVR, Name: pod.Spec.NodeName, Via: "spec.nodeName"})
	}
	return out, nil
}

// templateDependencies lists what the pod template of a workload names. The
// fields are relative to the pod template ("template.volumes[cfg]").
// Claims of generic ephemeral volumes are left out: their names depend on
// the pods.
func templateDependencies[T any](spec func(*T) *corev1.PodSpec) DependsOnFinder {
	return func(obj *unstructured.Unstructured) ([]Dependency, error) {
		var o T
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &o); err != nil {
			return nil, err
		}
		refs := podSpecReferences(spec(&o), "")
		out := make([]Dependency, 0, len(refs))
		for _, r := range refs {
			out = append(out, Dependency{GVR: r.GVR, Namespace: obj.GetNamespace(), Name: r.Name, Via: "template." + r.Via})
		}
		return out, nil
	}
}

// claimDependencies lists the volume a claim is bound to.
func claimDependencies(obj *unstructured.Unstructured) ([]Dependency, error) {
	vol, _, _ := unstructured.NestedString(obj.Object, "spec", "volumeName")
	if vol == "" {
		return nil, nil
	}
	return []Dependency{{GVR: pvGVR, Name: vol, Via: "spec.volumeName"}}, nil
}

// dependencyRefs turns dependencies into rows: one per target, with the
// fields joined, each checked in the cache. A target that is missing or that
// the user may not read is listed with the reason.
func dependencyRefs(ctx context.Context, r RelatedReader, deps []Dependency) []RelatedRef {
	type key struct {
		gvr             schema.GroupVersionResource
		namespace, name string
	}
	var order []key
	vias := make(map[key][]string)
	for _, d := range deps {
		k := key{d.GVR, d.Namespace, d.Name}
		if _, ok := vias[k]; !ok {
			order = append(order, k)
		}
		vias[k] = append(vias[k], d.Via)
	}
	out := make([]RelatedRef, 0, len(order))
	for _, k := range order {
		ref := RelatedRef{GVR: k.gvr, Namespace: k.namespace, Name: k.name, Relation: strings.Join(vias[k], ", ")}
		obj, err := r.GetByGVR(ctx, k.gvr, k.namespace, k.name)
		if err != nil {
			ref.Problem = readProblem(err)
		} else if obj != nil {
			ref.UID = obj.GetUID()
		}
		out = append(out, ref)
	}
	return out
}

// maxOwnerHops bounds the walk from a pod up its controller chain: pod,
// ReplicaSet, Deployment is two hops; the bound leaves room for operators
// that add a level or two.
const maxOwnerHops = 4

// ownerKey identifies an object of one walk: GVR, namespace, name and UID.
// One folder reads one context, so the context is not part of it.
type ownerKey struct {
	gvr             schema.GroupVersionResource
	namespace, name string
	uid             string
}

// workloadRefs walks the controller chain of every pod in refs and returns
// one row per controller it meets: the ReplicaSets, Deployments, Jobs and so
// on that run the pods. Each controller is listed once, whatever the number
// of its pods, and says which of the listed objects it controls. The pod
// rows stay as they are.
//
// Only controller references are followed, at most maxOwnerHops from a pod,
// and an object met twice on one chain ends it (owner references can form a
// cycle; the garbage collector breaks it, kc only must not loop). A
// controller that is gone, replaced by an object with another UID, of a kind
// the cluster does not serve, or that the user may not read is listed with
// the reason and ends its chain.
func workloadRefs(ctx context.Context, r RelatedReader, mapper meta.RESTMapper, refs []RelatedRef) []RelatedRef {
	w := ownerWalk{r: r, mapper: mapper, found: make(map[ownerKey]*walkedOwner), reads: make(map[ownerKey]ownerRead)}
	for _, p := range refs {
		if p.GVR == podGVR && len(p.ownerRefs) > 0 {
			w.walk(ctx, p)
		}
	}
	out := make([]RelatedRef, 0, len(w.order))
	for _, k := range w.order {
		o := w.found[k]
		o.ref.Relation = "controller of " + joinLimited(o.children, 3)
		out = append(out, o.ref)
	}
	return out
}

// ownerWalk is the state of workloadRefs across pods: the controllers met,
// in order, and each controller's read, done once.
type ownerWalk struct {
	r      RelatedReader
	mapper meta.RESTMapper
	order  []ownerKey
	found  map[ownerKey]*walkedOwner
	reads  map[ownerKey]ownerRead
}

type walkedOwner struct {
	ref      RelatedRef
	children []string
}

type ownerRead struct {
	obj     *unstructured.Unstructured
	problem string
}

// walk follows the controller chain of one pod.
func (w *ownerWalk) walk(ctx context.Context, pod RelatedRef) {
	visited := map[ownerKey]bool{{gvr: podGVR, namespace: pod.Namespace, name: pod.Name, uid: string(pod.UID)}: true}
	child := kubectlResource(podGVR) + "/" + pod.Name
	owners := pod.ownerRefs
	for range maxOwnerHops {
		ctrl := metav1.GetControllerOfNoCopy(&metav1.ObjectMeta{OwnerReferences: owners})
		if ctrl == nil {
			return
		}
		o := ResolveOwners(w.mapper, pod.Namespace, []metav1.OwnerReference{*ctrl})[0]
		k := ownerKey{gvr: o.GVR, namespace: o.Namespace, name: o.Name, uid: string(o.UID)}
		if o.GVR.Empty() {
			k.gvr = schema.GroupVersionResource{Group: o.APIVersion, Resource: o.Kind}
		}
		if visited[k] {
			return
		}
		visited[k] = true
		met := w.meet(k, o, child)
		if o.Problem != "" {
			return
		}
		res := w.read(ctx, k, o)
		if res.problem != "" {
			met.ref.Problem = res.problem
			return
		}
		child = kubectlResource(o.GVR) + "/" + o.Name
		owners = res.obj.GetOwnerReferences()
	}
}

// meet records that the controller k controls child.
func (w *ownerWalk) meet(k ownerKey, o RelatedRef, child string) *walkedOwner {
	met, ok := w.found[k]
	if !ok {
		met = &walkedOwner{ref: o}
		w.found[k] = met
		w.order = append(w.order, k)
	}
	if !slices.Contains(met.children, child) {
		met.children = append(met.children, child)
	}
	return met
}

// read reads a controller once. An object with another UID than the
// reference is a replacement, not the controller.
func (w *ownerWalk) read(ctx context.Context, k ownerKey, o RelatedRef) ownerRead {
	if res, ok := w.reads[k]; ok {
		return res
	}
	obj, err := w.r.GetByGVR(ctx, o.GVR, o.Namespace, o.Name)
	res := ownerRead{obj: obj, problem: readProblem(err)}
	switch {
	case err != nil:
	case obj == nil:
		res.problem = problemNotFound
	case o.UID != "" && obj.GetUID() != o.UID:
		res = ownerRead{problem: problemReplaced}
	}
	w.reads[k] = res
	return res
}

// joinLimited joins at most n items and counts the rest.
func joinLimited(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(items[:n], ", "), len(items)-n)
}

// partialRef is the row that says a used-by lookup could not read a source
// type: the user may not list it, so the result is incomplete rather than
// empty. ok is false for any other error.
func partialRef(namespace string, err error) (RelatedRef, bool) {
	if !apierrors.IsForbidden(err) {
		return RelatedRef{}, false
	}
	resource := "objects"
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		if d := status.Status().Details; d != nil && d.Kind != "" {
			resource = d.Kind
			if d.Group != "" {
				resource += "." + d.Group
			}
		}
	}
	return RelatedRef{
		Name:      "(not listed)",
		Namespace: namespace,
		Kind:      resource,
		Relation:  "source",
		Problem:   problemForbidden + "; the result is incomplete",
	}, true
}
