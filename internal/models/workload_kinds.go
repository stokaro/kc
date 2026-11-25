package models

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// learnedWorkloads is what the overview engines of this kc process learned
// about the kinds outside workloadKinds whose objects control pods, per
// cluster connection (ADR 0364). Info and the dashboard read it on the UI
// goroutine, without I/O, to decide whether the row under the cursor is a
// workload.
var learnedWorkloads = newWorkloadKindRegistry()

// workloadKindRegistry holds the learned kinds of each cluster connection.
type workloadKindRegistry struct {
	mu       sync.RWMutex
	clusters map[kccluster.Key]*clusterWorkloadKinds
}

// clusterWorkloadKinds is what one cluster connection taught the engines.
// Both sets only grow while kc runs, so a kind stays a workload after its
// last pod is gone.
type clusterWorkloadKinds struct {
	// chains are the kinds seen in a controller chain of a pod, with the
	// ways their pods reach them.
	chains map[schema.GroupVersionResource]*chainKind
	// scalable are the namespaced kinds whose scale subresource discovery
	// lists, with their kind.
	scalable map[schema.GroupVersionResource]string
	// scale caches where the owner kinds read their desired replicas, so a
	// new engine (another namespace in Info) does not read the CRDs again.
	scale map[schema.GroupVersionResource]scaleSpec
}

// scaleSpec is whether a kind has a scale subresource and where it reads
// the desired replicas, as kc learned it at a time.
type scaleSpec struct {
	hasScale bool
	path     []string
	note     string // why path is empty
	at       time.Time
}

// chainKind is one kind seen in the controller chain of a pod.
type chainKind struct {
	kind string
	// vias holds each sequence of kinds seen between a pod and an object of
	// the kind, nearest the pod first, by its text.
	vias map[string][]schema.GroupKind
}

func newWorkloadKindRegistry() *workloadKindRegistry {
	return &workloadKindRegistry{clusters: make(map[kccluster.Key]*clusterWorkloadKinds)}
}

// registryKey is the cluster connection of key: the namespace and the
// selector of a pool entry name caches of the same cluster.
func registryKey(key kccluster.Key) kccluster.Key {
	key.Namespace, key.SelectorKey = "", ""
	return key
}

// clusterLocked returns the entry of key, creating it. r.mu must be held
// for writing.
func (r *workloadKindRegistry) clusterLocked(key kccluster.Key) *clusterWorkloadKinds {
	c := r.clusters[key]
	if c == nil {
		c = &clusterWorkloadKinds{
			chains:   make(map[schema.GroupVersionResource]*chainKind),
			scalable: make(map[schema.GroupVersionResource]string),
			scale:    make(map[schema.GroupVersionResource]scaleSpec),
		}
		r.clusters[key] = c
	}
	return c
}

// recordChains adds the kinds that a recompute saw in controller chains.
func (r *workloadKindRegistry) recordChains(key kccluster.Key, seen map[schema.GroupVersionResource]*chainKind) {
	if len(seen) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.clusterLocked(registryKey(key))
	for gvr, k := range seen {
		have := c.chains[gvr]
		if have == nil {
			have = &chainKind{kind: k.kind, vias: make(map[string][]schema.GroupKind)}
			c.chains[gvr] = have
		}
		maps.Copy(have.vias, k.vias)
	}
}

// recordScalable adds the kinds with a scale subresource.
func (r *workloadKindRegistry) recordScalable(key kccluster.Key, scalable map[schema.GroupVersionResource]string) {
	if len(scalable) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	maps.Copy(r.clusterLocked(registryKey(key)).scalable, scalable)
}

// scaleSpec returns the scale spec of gvr learned within maxAge of now.
func (r *workloadKindRegistry) scaleSpec(key kccluster.Key, gvr schema.GroupVersionResource, now time.Time, maxAge time.Duration) (scaleSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := r.clusters[registryKey(key)]
	if c == nil {
		return scaleSpec{}, false
	}
	s, ok := c.scale[gvr]
	if !ok || now.Sub(s.at) >= maxAge {
		return scaleSpec{}, false
	}
	return s, true
}

func (r *workloadKindRegistry) recordScaleSpec(key kccluster.Key, gvr schema.GroupVersionResource, s scaleSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clusterLocked(registryKey(key)).scale[gvr] = s
}

func (r *workloadKindRegistry) isTarget(key kccluster.Key, gvr schema.GroupVersionResource) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := r.clusters[registryKey(key)]
	if c == nil {
		return false
	}
	_, chained := c.chains[gvr]
	_, scalable := c.scalable[gvr]
	return chained || scalable
}

func (r *workloadKindRegistry) chainOf(key kccluster.Key, gvr schema.GroupVersionResource) (WorkloadChain, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := r.clusters[registryKey(key)]
	if c == nil {
		return WorkloadChain{}, false
	}
	k := c.chains[gvr]
	if k == nil {
		return WorkloadChain{}, false
	}
	out := WorkloadChain{Kind: k.kind}
	for _, via := range k.vias {
		out.Vias = append(out.Vias, slices.Clone(via))
	}
	slices.SortFunc(out.Vias, func(a, b []schema.GroupKind) int { return strings.Compare(viaText(a), viaText(b)) })
	return out, true
}

// viaText writes the kinds between a pod and a workload as
// "ReplicaSet.apps/Deployment.apps"; "" for a pod the workload owns itself.
func viaText(via []schema.GroupKind) string {
	parts := make([]string, len(via))
	for i, gk := range via {
		parts[i] = gk.String()
	}
	return strings.Join(parts, "/")
}

// IsWorkloadTarget reports whether Info and the dashboard show an object
// of gvr in the cluster connection of key as a workload: a built-in
// controller (IsInfoWorkload), a kind that an overview engine of key saw
// in the controller chain of a pod, or a kind whose scale subresource
// discovery lists. It does no I/O; until an engine of key has computed, a
// custom kind is not known yet.
func IsWorkloadTarget(key kccluster.Key, gvr schema.GroupVersionResource) bool {
	return IsInfoWorkload(gvr) || learnedWorkloads.isTarget(key, gvr)
}

// WorkloadChain says how the pods of a custom workload kind reach it, as
// far as the overview engines saw: for the Prometheus history, which must
// join the owner series of the kinds in between.
type WorkloadChain struct {
	// Kind is the kind as owner references name it: Rollout.
	Kind string
	// Vias are the sequences of kinds seen between a pod and an object of
	// the kind, nearest the pod first, sorted; an empty sequence is a pod
	// the object owns itself.
	Vias [][]schema.GroupKind
}

// WorkloadChainOf returns how pods reach objects of gvr in the cluster
// connection of key; false when no engine saw a pod of the kind.
func WorkloadChainOf(key kccluster.Key, gvr schema.GroupVersionResource) (WorkloadChain, bool) {
	return learnedWorkloads.chainOf(key, gvr)
}
