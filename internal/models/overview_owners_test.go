package models

import (
	"maps"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

var (
	ovRolloutsGVR       = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	ovPodSetsGVR        = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "podsets"}
	ovScalersGVR        = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "scalers"}
	ovBatchesGVR        = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "batches"}
	ovLoopsGVR          = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "loops"}
	ovKnServicesGVR     = schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	ovKnConfigsGVR      = schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "configurations"}
	ovKnRevisionsGVR    = schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "revisions"}
	ovClusterThingsGVR  = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "clusterthings"}
	ovReplicaSetsKind   = schema.GroupKind{Group: "apps", Kind: "ReplicaSet"}
	ovDeploymentsKind   = schema.GroupKind{Group: "apps", Kind: "Deployment"}
	ovKnRevisionsKind   = schema.GroupKind{Group: "serving.knative.dev", Kind: "Revision"}
	ovKnConfigsKind     = schema.GroupKind{Group: "serving.knative.dev", Kind: "Configuration"}
	ovMysteryKind       = schema.GroupKind{Group: "example.com", Kind: "Mystery"}
	ovClusterThingsKind = schema.GroupKind{Group: "example.com", Kind: "ClusterThing"}
)

// customOwnerKinds are the owner kinds outside workloadKinds that the
// engine resolved for customOwnerInput.
func customOwnerKinds() map[schema.GroupKind]ownerKind {
	kind := func(gvr schema.GroupVersionResource, k string) ownerKind {
		return ownerKind{gk: schema.GroupKind{Group: gvr.Group, Kind: k}, gvr: gvr, namespaced: true}
	}
	rollouts := kind(ovRolloutsGVR, "Rollout")
	rollouts.hasScale, rollouts.scalePath = true, []string{"spec", "replicas"}
	scalers := kind(ovScalersGVR, "Scaler")
	scalers.hasScale, scalers.scaleNote = true, "no CustomResourceDefinition scalers.example.com says where its scale reads"
	out := make(map[schema.GroupKind]ownerKind)
	for _, k := range []ownerKind{
		rollouts, scalers,
		kind(ovPodSetsGVR, "PodSet"), kind(ovBatchesGVR, "Batch"), kind(ovLoopsGVR, "Loop"),
		kind(ovKnServicesGVR, "Service"), kind(ovKnConfigsGVR, "Configuration"), kind(ovKnRevisionsGVR, "Revision"),
		{gk: ovClusterThingsKind, gvr: ovClusterThingsGVR},
	} {
		out[k.gk] = k
	}
	return out
}

func ownerObj(ns, name string, uid types.UID, owners []metav1.OwnerReference) ownerObject {
	m := ownedMeta(ns, name, uid, owners)
	return ownerObject{meta: &m}
}

func scaled(o ownerObject, n int64) ownerObject {
	o.desired, o.hasDesired = n, true
	return o
}

// customOwnerObjects are the built-in objects and pods of namespace shop
// whose chains run through custom kinds:
//   - Rollout web owns ReplicaSet web-r1, which owns two pods (one ready);
//   - Rollout idle is scaled to 0; Rollout old was recreated (stale names
//     the old UID); Rollout broken has no replicas set and owns its pod;
//   - PodSet ps owns its pod and declares no scale subresource;
//   - a Knative Service owns a Configuration, a Revision, a Deployment, a
//     ReplicaSet and then the pod;
//   - Loops a and b own each other;
//   - a pod is owned by a cluster-scoped kind, one by a kind that is not
//     resolved yet, one by a Batch whose list cannot be read, one by a
//     Scaler whose scale path kc could not learn.
func customOwnerObjects() []crclient.Object {
	rsRef := controllerRef("apps/v1", "ReplicaSet", "web-r1", "rs-r1")
	knRS := controllerRef("apps/v1", "ReplicaSet", "kn-00001-deployment-abc", "kn-rs")
	limited := resources("100m", "64Mi", "200m", "128Mi")
	return []crclient.Object{
		&appsv1.ReplicaSet{ObjectMeta: ownedMeta("shop", "web-r1", "rs-r1", controllerRef("argoproj.io/v1alpha1", "Rollout", "web", "ro-web"))},
		ownedPod("shop", "web-r1-a", rsRef, limited),
		unready(ownedPod("shop", "web-r1-b", rsRef, limited)),
		ownedPod("shop", "stale", controllerRef("argoproj.io/v1alpha1", "Rollout", "old", "ro-old-1"), limited),
		ownedPod("shop", "broken-1", controllerRef("argoproj.io/v1alpha1", "Rollout", "broken", "ro-broken"), limited),
		ownedPod("shop", "ps-1", controllerRef("example.com/v1", "PodSet", "ps", "ps-uid"), limited),
		&appsv1.Deployment{ObjectMeta: ownedMeta("shop", "kn-00001-deployment", "kn-dep", controllerRef("serving.knative.dev/v1", "Revision", "kn-00001", "kn-rev"))},
		&appsv1.ReplicaSet{ObjectMeta: ownedMeta("shop", "kn-00001-deployment-abc", "kn-rs", controllerRef("apps/v1", "Deployment", "kn-00001-deployment", "kn-dep"))},
		ownedPod("shop", "kn-1", knRS, limited),
		ownedPod("shop", "loop-1", controllerRef("example.com/v1", "Loop", "a", "loop-a"), limited),
		ownedPod("shop", "mirror", controllerRef("example.com/v1", "ClusterThing", "node-x", "ct-uid"), limited),
		ownedPod("shop", "unknown-1", controllerRef("example.com/v1", "Mystery", "m", "m-uid"), limited),
		ownedPod("shop", "batch-1", controllerRef("example.com/v1", "Batch", "b1", "b1-uid"), limited),
		ownedPod("shop", "s-1", controllerRef("example.com/v1", "Scaler", "s", "s-uid"), limited),
	}
}

// customOwnerInput is customOwnerObjects read as one recompute reads them,
// with the custom objects the owner informers hold.
func customOwnerInput(t *testing.T) *overviewInput {
	t.Helper()
	in := loadOverviewInput(t.Context(), newOverviewFakeClient(customOwnerObjects()...), AllNamespaces, nil)
	if len(in.missing) > 0 {
		t.Fatalf("fake cache could not list: %v", in.missing)
	}
	broken := ownerObj("shop", "broken", "ro-broken", nil)
	broken.desiredNote = ".spec.replicas is not set"
	in.owners = ownerInput{
		kinds: customOwnerKinds(),
		objects: map[schema.GroupVersionResource][]ownerObject{
			ovRolloutsGVR: {
				scaled(ownerObj("shop", "web", "ro-web", nil), 3),
				scaled(ownerObj("shop", "idle", "ro-idle", nil), 0),
				scaled(ownerObj("shop", "old", "ro-old-2", nil), 1),
				broken,
			},
			ovPodSetsGVR:     {ownerObj("shop", "ps", "ps-uid", nil)},
			ovScalersGVR:     {ownerObj("shop", "s", "s-uid", nil)},
			ovKnServicesGVR:  {ownerObj("shop", "kn", "kn-svc", nil)},
			ovKnConfigsGVR:   {ownerObj("shop", "kn", "kn-cfg", controllerRef("serving.knative.dev/v1", "Service", "kn", "kn-svc"))},
			ovKnRevisionsGVR: {ownerObj("shop", "kn-00001", "kn-rev", controllerRef("serving.knative.dev/v1", "Configuration", "kn", "kn-cfg"))},
			ovLoopsGVR: {
				ownerObj("shop", "a", "loop-a", controllerRef("example.com/v1", "Loop", "b", "loop-b")),
				ownerObj("shop", "b", "loop-b", controllerRef("example.com/v1", "Loop", "a", "loop-a")),
			},
		},
	}
	in.missing[ovBatchesGVR] = "forbidden"
	return in
}

func TestSummarizeWorkloadsThroughCustomKinds(t *testing.T) {
	t.Parallel()
	r := computeOverview(customOwnerInput(t), appconfig.DefaultOverview(), overviewTestNow, nil)
	wl := func(gvr schema.GroupVersionResource, name string) InfoWorkload {
		return InfoWorkload{GVR: gvr, Namespace: "shop", Name: name}
	}
	tests := []struct {
		name        string
		w           InfoWorkload
		pods, ready int64
		desired     int64
		hasDesired  bool
		note        string
	}{
		{name: "rollout through its ReplicaSet", w: wl(ovRolloutsGVR, "web"), pods: 2, ready: 1, desired: 3, hasDesired: true},
		{name: "the ReplicaSet counts the same pods", w: wl(ovReplicaSetsGVR, "web-r1"), pods: 2, ready: 1},
		{name: "rollout scaled to 0", w: wl(ovRolloutsGVR, "idle"), hasDesired: true},
		{name: "rollout without replicas", w: wl(ovRolloutsGVR, "broken"), pods: 1, ready: 1, note: "desired count unknown: .spec.replicas is not set"},
		{name: "kind without scale", w: wl(ovPodSetsGVR, "ps"), pods: 1, ready: 1, note: "no desired count: podsets.example.com declares no scale subresource"},
		{name: "scale path not learned", w: wl(ovScalersGVR, "s"), pods: 1, ready: 1, note: "desired count unknown: no CustomResourceDefinition scalers.example.com says where its scale reads"},
		{name: "unreadable list is trusted", w: wl(ovBatchesGVR, "b1"), pods: 1, ready: 1, note: "no desired count: batches.example.com declares no scale subresource"},
		{name: "knative revision", w: wl(ovKnRevisionsGVR, "kn-00001"), pods: 1, ready: 1, note: "no desired count: revisions.serving.knative.dev declares no scale subresource"},
		{name: "knative service five owners up", w: wl(ovKnServicesGVR, "kn"), pods: 1, ready: 1, note: "no desired count: services.serving.knative.dev declares no scale subresource"},
		{name: "loop a", w: wl(ovLoopsGVR, "a"), pods: 1, ready: 1, note: "no desired count: loops.example.com declares no scale subresource"},
		{name: "loop b", w: wl(ovLoopsGVR, "b"), pods: 1, ready: 1, note: "no desired count: loops.example.com declares no scale subresource"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := r.workloads[tt.w]
			if s == nil {
				t.Fatalf("no summary for %v; have %v", tt.w, workloadKeys(r))
			}
			if s.pods != tt.pods || s.ready != tt.ready || s.desired != tt.desired || s.hasDesired != tt.hasDesired || s.desiredNote != tt.note {
				t.Fatalf("pods %d ready %d desired %d/%v note %q, want %d %d %d/%v %q", s.pods, s.ready, s.desired, s.hasDesired, s.desiredNote, tt.pods, tt.ready, tt.desired, tt.hasDesired, tt.note)
			}
		})
	}

	for _, p := range r.pods {
		switch p.name {
		case "stale", "mirror", "unknown-1":
			if len(p.owners) > 0 {
				t.Errorf("pod %s has owners %v, want none", p.name, p.owners)
			}
		case "loop-1":
			if want := []InfoWorkload{wl(ovLoopsGVR, "a"), wl(ovLoopsGVR, "b")}; !slices.Equal(p.owners, want) {
				t.Errorf("loop-1 owners = %v, want %v (the cycle ends the walk)", p.owners, want)
			}
		case "kn-1":
			want := []InfoWorkload{
				wl(ovReplicaSetsGVR, "kn-00001-deployment-abc"), wl(ovDeploymentsGVR, "kn-00001-deployment"),
				wl(ovKnRevisionsGVR, "kn-00001"), wl(ovKnConfigsGVR, "kn"), wl(ovKnServicesGVR, "kn"),
			}
			if !slices.Equal(p.owners, want) {
				t.Errorf("kn-1 owners = %v, want %v", p.owners, want)
			}
		}
	}
	for w, s := range r.workloads {
		if w.GVR == ovClusterThingsGVR {
			t.Errorf("unexpected workload %v: %+v", w, s)
		}
	}
	// The recreated Rollout old is listed with its count, but the pod of
	// the old UID is not its.
	if s := r.workloads[wl(ovRolloutsGVR, "old")]; s == nil || s.pods != 0 || s.desired != 1 {
		t.Errorf("rollout old = %+v, want 1 desired and no pods", s)
	}

	if !r.learnt.met[ovMysteryKind] || r.learnt.met[ovClusterThingsKind] || len(r.learnt.met) != 1 {
		t.Errorf("met = %v, want only the unresolved Mystery kind", r.learnt.met)
	}
	vias := func(gvr schema.GroupVersionResource) []string {
		k := r.learnt.chains[gvr]
		if k == nil {
			return nil
		}
		return slices.Sorted(maps.Keys(k.vias))
	}
	for gvr, want := range map[schema.GroupVersionResource][]string{
		ovRolloutsGVR:    {"", "ReplicaSet.apps"},
		ovPodSetsGVR:     {""},
		ovKnServicesGVR:  {"ReplicaSet.apps/Deployment.apps/Revision.serving.knative.dev/Configuration.serving.knative.dev"},
		ovKnRevisionsGVR: {"ReplicaSet.apps/Deployment.apps"},
		ovLoopsGVR:       {"", "Loop.example.com"},
	} {
		if got := vias(gvr); !slices.Equal(got, want) {
			t.Errorf("vias of %v = %q, want %q", gvr, got, want)
		}
	}
	if k := r.learnt.chains[ovRolloutsGVR]; k == nil || k.kind != "Rollout" {
		t.Errorf("rollouts chain = %+v, want kind Rollout", k)
	}
	if want := []schema.GroupKind{ovReplicaSetsKind, ovDeploymentsKind, ovKnRevisionsKind, ovKnConfigsKind}; !slices.Equal(r.learnt.chains[ovKnServicesGVR].vias[viaText(want)], want) {
		t.Errorf("knative service via = %v, want %v", r.learnt.chains[ovKnServicesGVR].vias, want)
	}
	for gvr := range r.learnt.chains {
		if IsInfoWorkload(gvr) {
			t.Errorf("built-in kind %v is learned as a chain", gvr)
		}
	}
}

// TestCustomWorkloadInfoSummary checks the bars of a custom workload: the
// title names it as kubectl does, the Pods bar compares with the scale
// path's count, says why there is none, and shows an object scaled to 0.
func TestCustomWorkloadInfoSummary(t *testing.T) {
	t.Parallel()
	m := &overviewMeasured{
		usageAt: overviewTestNow,
		pods: map[string][]containerUsage{
			"shop/web-r1-a": {{name: "app", cpu: 50, mem: 40 << 20}},
			"shop/web-r1-b": {{name: "app", cpu: 20, mem: 90 << 20}},
		},
	}
	r := computeOverview(customOwnerInput(t), appconfig.DefaultOverview(), overviewTestNow, m)
	cfg := appconfig.DefaultOverview()
	pods := func(s InfoSummary) InfoBar {
		t.Helper()
		for _, b := range s.Bars {
			if b.Label == "Pods" {
				return b
			}
		}
		t.Fatalf("no Pods bar in %+v", s.Bars)
		return InfoBar{}
	}

	web := workloadInfoSummary(r, nil, cfg, InfoWorkload{GVR: ovRolloutsGVR, Namespace: "shop", Name: "web"})
	if web.BarsTitle != "Pods of rollouts.argoproj.io shop/web" {
		t.Errorf("title = %q", web.BarsTitle)
	}
	if b := pods(web); b.Detail != "1/3 ready, 2 pods" || b.Flag != "not ready" || len(b.More) != 0 {
		t.Errorf("web pods = %+v", b)
	}
	var top []string
	for _, c := range web.Top {
		top = append(top, c.Name)
	}
	if !slices.Equal(top, []string{"web-r1-b", "web-r1-a"}) {
		t.Errorf("top = %v, want both pods by memory", top)
	}

	ps := pods(workloadInfoSummary(r, nil, cfg, InfoWorkload{GVR: ovPodSetsGVR, Namespace: "shop", Name: "ps"}))
	if ps.Detail != "1/1 ready" || !slices.Equal(ps.More, []string{"no desired count: podsets.example.com declares no scale subresource"}) {
		t.Errorf("podset pods = %+v", ps)
	}

	idle := pods(workloadInfoSummary(r, nil, cfg, InfoWorkload{GVR: ovRolloutsGVR, Namespace: "shop", Name: "idle"}))
	if idle.Detail != "scaled to 0, 0 pods" {
		t.Errorf("idle pods = %+v", idle)
	}
}

// TestCustomWorkloadEvents checks that an Event about a custom workload
// reaches it once its kind is resolved.
func TestCustomWorkloadEvents(t *testing.T) {
	t.Parallel()
	in := customOwnerInput(t)
	idx := newWorkloadIndex(&ruleContext{in: in})
	gvr, ok := involvedGVR(idx, corev1.ObjectReference{APIVersion: "argoproj.io/v1alpha1", Kind: "Rollout", Name: "web"})
	if !ok || gvr != ovRolloutsGVR || !idx.isWorkload(gvr) {
		t.Fatalf("involvedGVR(Rollout) = %v, %v", gvr, ok)
	}
	if _, ok := involvedGVR(idx, corev1.ObjectReference{APIVersion: "example.com/v1", Kind: "ClusterThing", Name: "node-x"}); ok {
		t.Fatal("a cluster-scoped owner kind is a workload")
	}
}

func TestDesiredAt(t *testing.T) {
	t.Parallel()
	path := []string{"spec", "replicas"}
	tests := []struct {
		name string
		obj  map[string]any
		want int64
		ok   bool
		note string
	}{
		{name: "int64", obj: map[string]any{"spec": map[string]any{"replicas": int64(3)}}, want: 3, ok: true},
		{name: "whole float", obj: map[string]any{"spec": map[string]any{"replicas": float64(2)}}, want: 2, ok: true},
		{name: "zero", obj: map[string]any{"spec": map[string]any{"replicas": int64(0)}}, ok: true},
		{name: "fraction", obj: map[string]any{"spec": map[string]any{"replicas": 1.5}}, note: ".spec.replicas is not a whole number (1.5)"},
		{name: "string", obj: map[string]any{"spec": map[string]any{"replicas": "3"}}, note: ".spec.replicas is not a whole number (3)"},
		{name: "null", obj: map[string]any{"spec": map[string]any{"replicas": nil}}, note: ".spec.replicas is not set"},
		{name: "missing", obj: map[string]any{"spec": make(map[string]any)}, note: ".spec.replicas is not set"},
		{name: "spec not a map", obj: map[string]any{"spec": "x"}, note: ".spec.replicas is not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok, note := desiredAt(tt.obj, path)
			if got != tt.want || ok != tt.ok || note != tt.note {
				t.Fatalf("desiredAt = %d, %v, %q; want %d, %v, %q", got, ok, note, tt.want, tt.ok, tt.note)
			}
		})
	}
}

func TestScaleSpecOf(t *testing.T) {
	t.Parallel()
	crd := func(versions ...any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"versions": versions}}}
		u.SetName("rollouts.argoproj.io")
		return u
	}
	version := func(name, path string) map[string]any {
		v := map[string]any{"name": name}
		if path != "" {
			v["subresources"] = map[string]any{"scale": map[string]any{"specReplicasPath": path, "statusReplicasPath": ".status.replicas"}}
		}
		return v
	}
	tests := []struct {
		name string
		crd  *unstructured.Unstructured
		want scaleSpec
	}{
		{name: "scale path", crd: crd(version("v1alpha1", ".spec.replicas")), want: scaleSpec{hasScale: true, path: []string{"spec", "replicas"}}},
		{name: "nested path without the dot", crd: crd(version("v1alpha2", ".spec.replicas"), version("v1alpha1", "spec.scale.count")), want: scaleSpec{hasScale: true, path: []string{"spec", "scale", "count"}}},
		{name: "no scale in this version", crd: crd(version("v1alpha1", ""), version("v1", ".spec.replicas")), want: scaleSpec{note: "version v1alpha1 of rollouts.argoproj.io declares no scale subresource"}},
		{name: "no such version", crd: crd(version("v1", ".spec.replicas")), want: scaleSpec{hasScale: true, note: "rollouts.argoproj.io has no version v1alpha1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := scaleSpecOf(tt.crd, ovRolloutsGVR)
			if got.hasScale != tt.want.hasScale || !slices.Equal(got.path, tt.want.path) || got.note != tt.want.note {
				t.Fatalf("scaleSpecOf = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestWorkloadKindRegistry checks what Info and the dashboard read: a kind
// becomes a workload target per cluster connection, once seen in a chain
// or listed with a scale subresource, and its chain is sorted.
func TestWorkloadKindRegistry(t *testing.T) {
	t.Parallel()
	key := kccluster.Key{ContextName: "registry-test", ImpersonateUser: t.Name()}
	other := kccluster.Key{ContextName: "registry-test-other", ImpersonateUser: t.Name()}
	if IsWorkloadTarget(key, ovRolloutsGVR) || IsWorkloadTarget(key, ovPodSetsGVR) {
		t.Fatal("a custom kind is a target before an engine saw it")
	}
	if !IsWorkloadTarget(key, ovDeploymentsGVR) {
		t.Fatal("a Deployment is no target")
	}
	learnedWorkloads.recordChains(key, map[schema.GroupVersionResource]*chainKind{
		ovRolloutsGVR: {kind: "Rollout", vias: map[string][]schema.GroupKind{"ReplicaSet.apps": {ovReplicaSetsKind}}},
	})
	learnedWorkloads.recordChains(key, map[schema.GroupVersionResource]*chainKind{
		ovRolloutsGVR: {kind: "Rollout", vias: map[string][]schema.GroupKind{"": {}}},
	})
	learnedWorkloads.recordScalable(key, map[schema.GroupVersionResource]string{ovScalersGVR: "Scaler"})

	// Another namespace or selector of the same connection shares it.
	scoped := key
	scoped.Namespace, scoped.SelectorKey = "shop", "app=web"
	for _, k := range []kccluster.Key{key, scoped} {
		if !IsWorkloadTarget(k, ovRolloutsGVR) || !IsWorkloadTarget(k, ovScalersGVR) {
			t.Errorf("rollouts or scalers are no target in %+v", k)
		}
		if IsWorkloadTarget(k, ovPodSetsGVR) {
			t.Errorf("podsets are a target in %+v", k)
		}
	}
	if IsWorkloadTarget(other, ovRolloutsGVR) {
		t.Error("another connection shares the learned kinds")
	}
	// Identity is the full GVR: another version is not the learned kind.
	if IsWorkloadTarget(key, schema.GroupVersionResource{Group: "argoproj.io", Version: "v1", Resource: "rollouts"}) {
		t.Error("another version of rollouts is a target")
	}

	chain, ok := WorkloadChainOf(scoped, ovRolloutsGVR)
	if !ok || chain.Kind != "Rollout" || len(chain.Vias) != 2 || len(chain.Vias[0]) != 0 || !slices.Equal(chain.Vias[1], []schema.GroupKind{ovReplicaSetsKind}) {
		t.Fatalf("chain = %+v, %v; want Rollout directly and through a ReplicaSet", chain, ok)
	}
	if _, ok := WorkloadChainOf(key, ovScalersGVR); ok {
		t.Error("a scalable kind without pods has a chain")
	}

	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	learnedWorkloads.recordScaleSpec(key, ovRolloutsGVR, scaleSpec{hasScale: true, path: []string{"spec", "replicas"}, at: now})
	if s, ok := learnedWorkloads.scaleSpec(scoped, ovRolloutsGVR, now.Add(time.Minute), scaleSpecMaxAge); !ok || !s.hasScale {
		t.Errorf("fresh scale spec = %+v, %v", s, ok)
	}
	if _, ok := learnedWorkloads.scaleSpec(key, ovRolloutsGVR, now.Add(scaleSpecMaxAge), scaleSpecMaxAge); ok {
		t.Error("an old scale spec still serves")
	}
}

func TestOwnerWatchWant(t *testing.T) {
	t.Parallel()
	var none *ownerWatch
	if none.want(ovRolloutsGVR) {
		t.Fatal("a nil watch wants a kind")
	}
	w := &ownerWatch{wanted: make(map[schema.GroupVersionResource]int)}
	if w.want(ovDeploymentsGVR) {
		t.Error("a built-in kind is wanted")
	}
	if !w.want(ovRolloutsGVR) || w.want(ovRolloutsGVR) {
		t.Error("want reports a new kind once")
	}
}

func TestViasText(t *testing.T) {
	t.Parallel()
	if got := viaText([]schema.GroupKind{ovReplicaSetsKind, ovDeploymentsKind}); got != "ReplicaSet.apps/Deployment.apps" {
		t.Errorf("viaText = %q", got)
	}
	if got := viaText(nil); got != "" {
		t.Errorf("viaText(nil) = %q", got)
	}
	if got := (InfoWorkload{GVR: ovRolloutsGVR, Namespace: "shop", Name: "web"}).String(); got != "rollouts.argoproj.io shop/web" {
		t.Error("a custom workload is not named as kubectl names it")
	}
}
