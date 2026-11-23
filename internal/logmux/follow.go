package logmux

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"

	"github.com/stokaro/kc/internal/logresume"
)

// TargetSetter receives the current target set. *Mux implements it.
type TargetSetter interface {
	SetTargets([]Target)
}

// PodMatcher picks the pods whose containers are streamed. It sees only
// metadata, so pods that do not match are never converted.
type PodMatcher func(metav1.Object) bool

// SelectorMatcher matches the pods in namespace whose labels match sel, as
// `kubectl logs -l <selector> -n <namespace>` does.
func SelectorMatcher(namespace string, sel labels.Selector) PodMatcher {
	return func(o metav1.Object) bool {
		return o.GetNamespace() == namespace && sel.Matches(labels.Set(o.GetLabels()))
	}
}

// NameMatcher matches one pod.
func NameMatcher(namespace, name string) PodMatcher {
	return func(o metav1.Object) bool {
		return o.GetNamespace() == namespace && o.GetName() == name
	}
}

// Follower keeps a target set in step with the pods of an informer. It is a
// client-go event handler: register it on the shared pod informer, and the
// informer's add, update and delete events (including the initial list)
// update the set. It starts no watch of its own.
type Follower struct {
	set   TargetSetter
	match PodMatcher

	mu   sync.Mutex
	pods map[types.NamespacedName][]Target
	// instances holds the container states of the matching pods, by pod
	// and container name, for Instance.
	instances map[types.NamespacedName]map[string]logresume.Instance
	// seen marks the pods that matched once, so that a pod that left is
	// known to be gone.
	seen map[types.NamespacedName]bool
}

// Instance returns the current state of t's container, for
// Options.Check: a pod that left the set is known to be gone (a zero
// Instance with ok), a pod the follower never saw is unknown.
func (f *Follower) Instance(t Target) (logresume.Instance, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := types.NamespacedName{Namespace: t.Namespace, Name: t.Pod}
	containers, ok := f.instances[key]
	if !ok {
		return logresume.Instance{}, f.seen[key]
	}
	return containers[t.Container], true
}

var _ toolscache.ResourceEventHandler = (*Follower)(nil)

// NewFollower returns a follower that sends the targets of the pods match
// accepts to set.
func NewFollower(set TargetSetter, match PodMatcher) *Follower {
	return &Follower{
		set:       set,
		match:     match,
		pods:      make(map[types.NamespacedName][]Target),
		instances: make(map[types.NamespacedName]map[string]logresume.Instance),
		seen:      make(map[types.NamespacedName]bool),
	}
}

// Pods returns the number of matching pods seen so far.
func (f *Follower) Pods() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pods)
}

// OnAdd handles an added pod.
func (f *Follower) OnAdd(obj any, _ bool) { f.upsert(obj) }

// OnUpdate handles a changed pod.
func (f *Follower) OnUpdate(_, newObj any) { f.upsert(newObj) }

// OnDelete handles a deleted pod.
func (f *Follower) OnDelete(obj any) {
	if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		obj = tomb.Obj
	}
	meta, ok := obj.(metav1.Object)
	if !ok {
		return
	}
	key := types.NamespacedName{Namespace: meta.GetNamespace(), Name: meta.GetName()}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pods[key]; !ok {
		return
	}
	delete(f.pods, key)
	delete(f.instances, key)
	f.publishLocked()
}

func (f *Follower) upsert(obj any) {
	meta, ok := obj.(metav1.Object)
	if !ok {
		return
	}
	key := types.NamespacedName{Namespace: meta.GetNamespace(), Name: meta.GetName()}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, known := f.pods[key]
	if !f.match(meta) {
		if known {
			delete(f.pods, key)
			delete(f.instances, key)
			f.publishLocked()
		}
		return
	}
	pod, err := toPod(obj)
	if err != nil {
		return
	}
	f.pods[key] = PodTargets(pod)
	f.instances[key] = logresume.PodInstances(pod)
	f.seen[key] = true
	f.publishLocked()
}

func (f *Follower) publishLocked() {
	keys := make([]types.NamespacedName, 0, len(f.pods))
	for k := range f.pods {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	var all []Target
	for _, k := range keys {
		all = append(all, f.pods[k]...)
	}
	f.set.SetTargets(all)
}

func toPod(obj any) (*corev1.Pod, error) {
	switch o := obj.(type) {
	case *corev1.Pod:
		return o, nil
	case *unstructured.Unstructured:
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &pod); err != nil {
			return nil, err
		}
		return &pod, nil
	default:
		return nil, fmt.Errorf("unexpected object %T", obj)
	}
}

// ErrNoSelector reports a workload without a usable pod selector.
var ErrNoSelector = errors.New("no pod selector")

// WorkloadSelector returns the pod selector of a workload with a
// `spec.selector` label selector: Deployments, ReplicaSets, StatefulSets,
// DaemonSets and Jobs. An empty selector is refused, since it would match
// every pod in the namespace.
func WorkloadSelector(obj *unstructured.Unstructured) (labels.Selector, error) {
	raw, found, err := unstructured.NestedMap(obj.Object, "spec", "selector")
	if err != nil {
		return nil, fmt.Errorf("%s/%s: spec.selector: %w", obj.GetKind(), obj.GetName(), err)
	}
	if !found {
		return nil, fmt.Errorf("%s/%s: %w", obj.GetKind(), obj.GetName(), ErrNoSelector)
	}
	var ls metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &ls); err != nil {
		return nil, fmt.Errorf("%s/%s: spec.selector: %w", obj.GetKind(), obj.GetName(), err)
	}
	if len(ls.MatchLabels) == 0 && len(ls.MatchExpressions) == 0 {
		return nil, fmt.Errorf("%s/%s: %w", obj.GetKind(), obj.GetName(), ErrNoSelector)
	}
	sel, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: spec.selector: %w", obj.GetKind(), obj.GetName(), err)
	}
	return sel, nil
}
