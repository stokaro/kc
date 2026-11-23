package models

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

// White-box: the scope decision, the leases and the input flags are
// internal to the overview engine; the public folder API cannot tell which
// cache served a kind.

func TestChooseOverviewScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		namespace   string
		namespaced  bool
		all, inNs   access
		want        listScope
		description string
	}{
		{"allowed everywhere", "shop", true, accessAllowed, accessUnknown, scopeShared, "the cluster-wide cache, as before"},
		{"unknown everywhere keeps the shared cache", "shop", true, accessUnknown, accessUnknown, scopeShared, "a failed review never narrows an admin's overview"},
		{"denied everywhere, allowed in the namespace", "shop", true, accessDenied, accessAllowed, scopeNamespace, ""},
		{"denied everywhere, unknown in the namespace", "shop", true, accessDenied, accessUnknown, scopeNamespace, "the namespace cache works for anyone who may read there"},
		{"denied in both", "shop", true, accessDenied, accessDenied, scopeDenied, ""},
		{"denied in an all-namespaces overview", AllNamespaces, true, accessDenied, accessUnknown, scopeDenied, ""},
		{"cluster-scoped kind denied", "shop", false, accessDenied, accessUnknown, scopeDenied, "a namespace cache cannot help"},
		{"cluster-scoped kind unknown", "shop", false, accessUnknown, accessUnknown, scopeShared, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := chooseOverviewScope(tc.namespace, tc.namespaced, tc.all, tc.inNs); got != tc.want {
				t.Fatalf("chooseOverviewScope = %v, want %v (%s)", got, tc.want, tc.description)
			}
		})
	}
}

// fakeOverviewAccess answers reviews from two tables and records the
// namespace questions.
type fakeOverviewAccess struct {
	all     map[schema.GroupVersionResource]bool // missing: an error
	inNs    map[schema.GroupVersionResource]bool
	inAsked []schema.GroupVersionResource
}

func (a *fakeOverviewAccess) CanListWatchAllNamespaces(_ context.Context, gvr schema.GroupVersionResource) (bool, error) {
	ok, known := a.all[gvr]
	if !known {
		return false, errors.New("review failed")
	}
	return ok, nil
}

func (a *fakeOverviewAccess) CanListWatchInNamespace(_ context.Context, gvr schema.GroupVersionResource, _ string) (bool, error) {
	a.inAsked = append(a.inAsked, gvr)
	return a.inNs[gvr], nil
}

func TestOverviewScopesMixedAccess(t *testing.T) {
	t.Parallel()
	a := &fakeOverviewAccess{
		all: map[schema.GroupVersionResource]bool{
			ovNodesGVR: true, ovPVsGVR: false, ovPodsGVR: false, ovEventsGVR: false,
			ovDeploymentsGVR: false, ovStatefulSetsGVR: true,
		},
		inNs: map[schema.GroupVersionResource]bool{ovPodsGVR: true, ovEventsGVR: false, ovDeploymentsGVR: true},
	}
	got := overviewScopes(t.Context(), a, "shop")
	want := map[schema.GroupVersionResource]listScope{
		ovNodesGVR:        scopeShared,
		ovPVsGVR:          scopeDenied,
		ovPodsGVR:         scopeNamespace,
		ovEventsGVR:       scopeDenied,
		ovDeploymentsGVR:  scopeNamespace,
		ovStatefulSetsGVR: scopeShared,
		ovJobsGVR:         scopeShared, // the review failed: unknown
	}
	for gvr, w := range want {
		if got[gvr] != w {
			t.Errorf("%s: scope %v, want %v", gvr.Resource, got[gvr], w)
		}
	}
	// The namespace is asked only after a clear denial of a namespaced kind.
	wantAsked := []schema.GroupVersionResource{ovPodsGVR, ovEventsGVR, ovDeploymentsGVR}
	if !slices.Equal(a.inAsked, wantAsked) {
		t.Errorf("namespace reviews = %v, want %v", a.inAsked, wantAsked)
	}
}

func scopesOf(def listScope, set map[schema.GroupVersionResource]listScope) map[schema.GroupVersionResource]listScope {
	out := make(map[schema.GroupVersionResource]listScope, len(overviewKinds))
	for _, k := range overviewKinds {
		out[k.gvr] = def
	}
	maps.Copy(out, set)
	return out
}

func TestScopedOverviewConnLeasesOnlyNeededCaches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		scopes map[schema.GroupVersionResource]listScope
		want   []listScope
	}{
		{"admin", scopesOf(scopeShared, nil), []listScope{scopeShared}},
		{"namespace only", scopesOf(scopeDenied, map[schema.GroupVersionResource]listScope{ovPodsGVR: scopeNamespace, ovEventsGVR: scopeNamespace}), []listScope{scopeNamespace}},
		{"mixed", scopesOf(scopeNamespace, map[schema.GroupVersionResource]listScope{ovNodesGVR: scopeShared, ovPVsGVR: scopeDenied}), []listScope{scopeShared, scopeNamespace}},
		{"nothing readable", scopesOf(scopeDenied, nil), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var acquired []listScope
			released := 0
			c, err := newScopedOverviewConn("shop", tc.scopes, func(s listScope) (*kccluster.Cluster, func(), error) {
				acquired = append(acquired, s)
				return nil, func() { released++ }, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(acquired, tc.want) {
				t.Fatalf("acquired %v, want %v", acquired, tc.want)
			}
			for _, k := range overviewKinds {
				reason, _ := c.Unavailable(k.gvr)
				only := c.OnlyNamespace(k.gvr)
				switch tc.scopes[k.gvr] {
				case scopeDenied:
					if !strings.HasPrefix(reason, "not permitted to list and watch") || c.Reader(k.gvr) != nil {
						t.Errorf("%s denied: reason %q, reader %v", k.gvr.Resource, reason, c.Reader(k.gvr))
					}
				case scopeNamespace:
					if only != "shop" {
						t.Errorf("%s: OnlyNamespace = %q, want shop", k.gvr.Resource, only)
					}
				case scopeShared:
					if only != "" {
						t.Errorf("%s: OnlyNamespace = %q, want all namespaces", k.gvr.Resource, only)
					}
				}
			}
			c.releaseAll()
			c.releaseAll() // a lease is given up once
			if released != len(tc.want) {
				t.Fatalf("released %d leases, want %d", released, len(tc.want))
			}
		})
	}
}

func TestScopedOverviewConnAcquireFailureReleases(t *testing.T) {
	t.Parallel()
	released := 0
	scopes := scopesOf(scopeNamespace, map[schema.GroupVersionResource]listScope{ovNodesGVR: scopeShared})
	_, err := newScopedOverviewConn("shop", scopes, func(s listScope) (*kccluster.Cluster, func(), error) {
		if s == scopeNamespace {
			return nil, nil, errors.New("pool closed")
		}
		return nil, func() { released++ }, nil
	})
	if err == nil {
		t.Fatal("no error when a cache cannot be acquired")
	}
	if released != 1 {
		t.Fatalf("released %d leases after the failure, want the shared one", released)
	}
}

func TestOverviewDeniedReason(t *testing.T) {
	t.Parallel()
	nodes, pods := overviewKinds[0], overviewKinds[1]
	tests := []struct {
		kind      overviewKind
		namespace string
		want      string
	}{
		{nodes, "shop", "not permitted to list and watch"},
		{pods, AllNamespaces, "not permitted to list and watch in all namespaces"},
		{pods, "shop", "not permitted to list and watch in all namespaces or in namespace shop"},
	}
	for _, tc := range tests {
		if got := overviewDeniedReason(tc.kind, tc.namespace); got != tc.want {
			t.Errorf("%s in %q: %q, want %q", tc.kind.gvr.Resource, tc.namespace, got, tc.want)
		}
	}
}

func emptyOverviewInput(namespace string) *overviewInput {
	return &overviewInput{
		namespace: namespace,
		missing:   make(map[schema.GroupVersionResource]string),
		onlyIn:    make(map[schema.GroupVersionResource]string),
	}
}

// TestCapacityNeedsPodsInAllNamespaces: pods read in one namespace give
// the namespace's rows but never a node's load.
func TestCapacityNeedsPodsInAllNamespaces(t *testing.T) {
	t.Parallel()
	in := emptyOverviewInput("shop")
	in.nodes = []corev1.Node{*readyNode("n1", "4", "8Gi", 110)}
	in.pods = []corev1.Pod{{Namespace: "shop", Name: "p", Spec: corev1.PodSpec{NodeName: "n1"}, Status: corev1.PodStatus{Phase: corev1.PodPending}}}
	in.onlyIn[ovPodsGVR] = "shop"
	r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil)
	want := "pods: readable only in namespace shop; this needs every one in all namespaces"
	for _, id := range []string{"capacity/cpu", "capacity/memory", "capacity/pods"} {
		if f, _ := r.finding(id); f.unavailable != want || f.value != "n/a" {
			t.Errorf("%s = %q (%q), want n/a because %q", id, f.value, f.unavailable, want)
		}
	}
	if r.nodes != nil {
		t.Errorf("node summaries from one namespace's pods: %v", r.nodes)
	}
	if f, _ := r.finding("health/pods-pending"); f.unavailable != "" || len(f.targets) != 1 {
		t.Errorf("pods-pending = %q (%q), want the namespace's pod", f.value, f.unavailable)
	}
}

func unavailableStatefulSet(name string) appsv1.StatefulSet {
	return appsv1.StatefulSet{
		Namespace: "a", Name: name, UID: types.UID("uid-" + name),
		Spec:   appsv1.StatefulSetSpec{Replicas: ptr.To[int32](2)},
		Status: appsv1.StatefulSetStatus{AvailableReplicas: 0},
	}
}

func TestPartialCountFinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		missing     []schema.GroupVersionResource
		wantValue   string
		unavailable string
		partial     string
	}{
		{"all readable", nil, "1", "", ""},
		{"one kind missing", []schema.GroupVersionResource{ovDaemonSetsGVR}, "1 (partial)", "", "daemonsets.apps: forbidden"},
		{"two kinds missing", []schema.GroupVersionResource{ovDeploymentsGVR, ovDaemonSetsGVR}, "1 (partial)", "", "deployments.apps: forbidden; daemonsets.apps: forbidden"},
		{"every kind missing", []schema.GroupVersionResource{ovDeploymentsGVR, ovStatefulSetsGVR, ovDaemonSetsGVR}, "n/a", "deployments.apps: forbidden", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := emptyOverviewInput(AllNamespaces)
			for _, gvr := range tc.missing {
				in.missing[gvr] = "forbidden"
			}
			if !slices.Contains(tc.missing, ovStatefulSetsGVR) {
				in.statefulSets = []appsv1.StatefulSet{unavailableStatefulSet("db")}
			}
			r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil)
			f, _ := r.finding("health/workloads-unavailable")
			if f.value != tc.wantValue || f.unavailable != tc.unavailable || f.partial != tc.partial {
				t.Fatalf("workloads-unavailable = %q, unavailable %q, partial %q; want %q, %q, %q",
					f.value, f.unavailable, f.partial, tc.wantValue, tc.unavailable, tc.partial)
			}
		})
	}
}

// TestPartialKindStaysUnknown: a target of a kind that a partial row can
// no longer read is not shown as recovered.
func TestPartialKindStaysUnknown(t *testing.T) {
	t.Parallel()
	life := newOverviewLifecycle()
	now := overviewTestNow
	in := emptyOverviewInput(AllNamespaces)
	in.statefulSets = []appsv1.StatefulSet{unavailableStatefulSet("db")}
	lifeCompute(in, now, nil, life)

	in = emptyOverviewInput(AllNamespaces)
	in.missing[ovStatefulSetsGVR] = "the cache did not sync"
	r := lifeCompute(in, now.Add(time.Minute), nil, life)
	f, _ := r.finding("health/workloads-unavailable")
	if f.value != "0 (partial)" {
		t.Fatalf("value = %q, want 0 (partial)", f.value)
	}
	tg := onlyTarget(t, f)
	if tg.severity != severityUnknown || !strings.HasPrefix(tg.value, "unknown (statefulsets.apps: the cache did not sync)") {
		t.Fatalf("target = %v %q, want it unknown, not recovered", tg.severity, tg.value)
	}
}

// TestFindingPreviewNamespaceOnlyReasons: the Info preview of a finding
// that a namespace-only user cannot compute suggests the permission check
// that matches the missing scope (ADR 0173, ADR 0182).
func TestFindingPreviewNamespaceOnlyReasons(t *testing.T) {
	t.Parallel()
	in := emptyOverviewInput("shop")
	in.missing[ovNodesGVR] = overviewDenied
	in.missing[ovPVsGVR] = overviewDenied
	in.missing[ovJobsGVR] = overviewDeniedInNamespace + "shop"
	in.missing[ovStatefulSetsGVR] = overviewDeniedInNamespace + "shop"
	in.pods = []corev1.Pod{{Namespace: "shop", Name: "p", Status: corev1.PodStatus{Phase: corev1.PodPending}}}
	r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil)
	f := previewFolder(t, "shop", r)
	tests := []struct {
		id, unavailable, check string
	}{
		{"health/nodes-not-ready", "nodes: not permitted to list and watch", "kubectl auth can-i list nodes"},
		{"health/jobs-failed", "jobs.batch: not permitted to list and watch in all namespaces or in namespace shop", "kubectl auth can-i list jobs.batch -n shop"},
	}
	for _, tc := range tests {
		p := previewOf(t, f, tc.id)
		if p.Unavailable != tc.unavailable || len(p.Verify) == 0 || p.Verify[0] != tc.check {
			t.Errorf("%s: unavailable %q, verify %q; want %q, first %q", tc.id, p.Unavailable, p.Verify, tc.unavailable, tc.check)
		}
	}

	// Nodes readable, pods only in the namespace: the check is for all
	// namespaces, where capacity needs them.
	in = emptyOverviewInput("shop")
	in.nodes = []corev1.Node{*readyNode("n1", "4", "8Gi", 110)}
	in.onlyIn[ovPodsGVR] = "shop"
	f = previewFolder(t, "shop", computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil))
	if p := previewOf(t, f, "capacity/cpu"); len(p.Verify) == 0 || p.Verify[0] != "kubectl auth can-i list pods -A" {
		t.Errorf("capacity/cpu verify = %q, want a check of pods in all namespaces first", p.Verify)
	}

	// A partial row carries what it leaves out, and is not "nothing to check".
	wl := previewOf(t, previewFolder(t, "shop", r), "health/workloads-unavailable")
	if wl.Value != "0 (partial)" || wl.NotApplicable != "" || !strings.HasPrefix(wl.Partial, "statefulsets.apps: not permitted") {
		t.Errorf("workloads preview value %q, not applicable %q, partial %q", wl.Value, wl.NotApplicable, wl.Partial)
	}
}
