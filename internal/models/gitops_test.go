package models

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	kustomizationsGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	helmReleasesGVR   = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	gitRepositoryGVR  = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	applicationsGVR   = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
)

// gitopsMapper serves Flux and Argo CD kinds; without ok it serves none,
// like a cluster without them installed.
func gitopsMapper(ok bool) meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{
		{Group: "apps", Version: "v1"}, kustomizationsGVR.GroupVersion(), helmReleasesGVR.GroupVersion(),
		gitRepositoryGVR.GroupVersion(), applicationsGVR.GroupVersion(),
	})
	m.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, meta.RESTScopeNamespace)
	if ok {
		m.Add(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}, meta.RESTScopeNamespace)
		m.Add(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}, meta.RESTScopeNamespace)
		m.Add(schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"}, meta.RESTScopeNamespace)
		m.Add(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}, meta.RESTScopeNamespace)
	}
	return m
}

func uobj(apiVersion, kind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	obj := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"namespace": namespace, "name": name}}
	for k, v := range fields {
		obj[k] = v
	}
	return &unstructured.Unstructured{Object: obj}
}

func gitopsDeployment(labels, annotations map[string]string) *unstructured.Unstructured {
	d := uobj("apps/v1", "Deployment", "shop", "web", nil)
	d.SetLabels(labels)
	d.SetAnnotations(annotations)
	return d
}

func fluxLabels(name string) map[string]string {
	return map[string]string{fluxKustomizeNameLabel: name, fluxKustomizeNamespaceLabel: "flux-system"}
}

func kustomization(name string, status map[string]any, spec map[string]any) *unstructured.Unstructured {
	return uobj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", name, map[string]any{"spec": spec, "status": status})
}

func inventory(ids ...string) map[string]any {
	entries := make([]any, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, map[string]any{"id": id, "v": "v1"})
	}
	return map[string]any{"inventory": map[string]any{"entries": entries}}
}

func TestGitOpsRefs(t *testing.T) {
	t.Parallel()
	app := func(ns string, resources ...any) runtime.Object {
		return uobj("argoproj.io/v1alpha1", "Application", ns, "shop", map[string]any{"status": map[string]any{"resources": resources}})
	}
	webResource := map[string]any{"group": "apps", "kind": "Deployment", "namespace": "shop", "name": "web", "status": "Synced"}
	cases := []struct {
		name     string
		obj      *unstructured.Unstructured
		served   bool
		objects  map[schema.GroupVersionResource][]runtime.Object
		denied   map[schema.GroupVersionResource]bool
		relation string // substring of the only row's Relation
		problem  string // substring of its Problem; "" means none
	}{
		{name: "in the inventory", obj: gitopsDeployment(fluxLabels("apps"), nil), served: true,
			objects:  map[schema.GroupVersionResource][]runtime.Object{kustomizationsGVR: {kustomization("apps", inventory("shop_web_apps_Deployment"), nil)}},
			relation: "listed in its inventory"},
		{name: "labels copied, not in the inventory", obj: gitopsDeployment(fluxLabels("apps"), nil), served: true,
			objects:  map[schema.GroupVersionResource][]runtime.Object{kustomizationsGVR: {kustomization("apps", inventory("shop_other_apps_Deployment"), nil)}},
			relation: "label only: not in its inventory"},
		{name: "absent inventory", obj: gitopsDeployment(fluxLabels("apps"), nil), served: true,
			objects:  map[schema.GroupVersionResource][]runtime.Object{kustomizationsGVR: {kustomization("apps", map[string]any{}, nil)}},
			relation: "label only: the Kustomization has no inventory"},
		{name: "missing owner", obj: gitopsDeployment(fluxLabels("gone"), nil), served: true, problem: problemNotFound},
		{name: "forbidden owner", obj: gitopsDeployment(fluxLabels("apps"), nil), served: true,
			denied: map[schema.GroupVersionResource]bool{kustomizationsGVR: true}, problem: problemForbidden},
		{name: "Flux not installed", obj: gitopsDeployment(fluxLabels("apps"), nil), problem: problemNotServed},
		{name: "Argo CD tracking id, Application found in any namespace", served: true,
			obj:      gitopsDeployment(nil, map[string]string{argoTrackingAnnotation: "shop:apps/Deployment:shop/web"}),
			objects:  map[schema.GroupVersionResource][]runtime.Object{applicationsGVR: {app("argocd", webResource)}},
			relation: "listed in its status.resources"},
		{name: "Argo CD Application name in two namespaces", served: true,
			obj:     gitopsDeployment(nil, map[string]string{argoTrackingAnnotation: "shop:apps/Deployment:shop/web"}),
			objects: map[schema.GroupVersionResource][]runtime.Object{applicationsGVR: {app("argocd"), app("team")}},
			problem: problemAmbiguous},
		{name: "Argo CD apps-in-any-namespace id", served: true,
			obj:      gitopsDeployment(nil, map[string]string{argoTrackingAnnotation: "team_shop:apps/Deployment:shop/web"}),
			objects:  map[schema.GroupVersionResource][]runtime.Object{applicationsGVR: {app("argocd", webResource), app("team")}},
			relation: "annotation only: not in its status.resources"},
		{name: "tracking id of another object", served: true,
			obj:     gitopsDeployment(nil, map[string]string{argoTrackingAnnotation: "shop:apps/Deployment:shop/api"}),
			problem: "names another object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &denyingReader{fakeReader: newFakeReader(t, tc.objects), denied: tc.denied}
			refs := gitopsRefs(t.Context(), r, gitopsMapper(tc.served), tc.obj)
			if len(refs) != 1 {
				t.Fatalf("refs = %+v, want one", refs)
			}
			ref := refs[0]
			if !strings.Contains(ref.Relation, tc.relation) {
				t.Errorf("relation = %q, want %q", ref.Relation, tc.relation)
			}
			if (tc.problem == "") != (ref.Problem == "") || !strings.Contains(ref.Problem, tc.problem) {
				t.Errorf("problem = %q, want %q", ref.Problem, tc.problem)
			}
		})
	}
}

func TestGitOpsClaimsWithoutMetadata(t *testing.T) {
	t.Parallel()
	// app.kubernetes.io/instance is set by Helm and many charts too: it is
	// not a GitOps claim.
	obj := gitopsDeployment(map[string]string{"app.kubernetes.io/instance": "shop", "app.kubernetes.io/managed-by": "Helm"}, nil)
	if c := gitopsClaims(obj); len(c) != 0 {
		t.Fatalf("claims = %+v, want none", c)
	}
}

func ready(status, reason, message string, gen int64) map[string]any {
	return map[string]any{"type": "Ready", "status": status, "reason": reason, "message": message,
		"lastTransitionTime": "2026-11-23T10:00:00Z", "observedGeneration": gen}
}

func codesOf(d diagReport) []string {
	out := make([]string, 0, len(d.Findings))
	for _, f := range d.Findings {
		out = append(out, f.Code)
	}
	slices.Sort(out)
	return out
}

func TestDiagnoseKustomization(t *testing.T) {
	t.Parallel()
	repo := uobj("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "repo", map[string]any{
		"spec":   map[string]any{"url": "https://git.example.com/shop.git"},
		"status": map[string]any{"artifact": map[string]any{"revision": "main@sha1:bbb"}, "conditions": []any{ready("True", "Succeeded", "", 1)}},
	})
	base := kustomization("base", map[string]any{"conditions": []any{ready("False", "BuildFailed", "kustomize build failed", 1)}}, nil)
	spec := map[string]any{
		"path": "./apps", "prune": true,
		"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"},
		"dependsOn": []any{map[string]any{"name": "base"}, map[string]any{"name": "missing"}},
	}
	cases := []struct {
		name   string
		obj    *unstructured.Unstructured
		denied map[schema.GroupVersionResource]bool
		want   []string
	}{
		{name: "healthy at the source revision",
			obj: kustomization("apps", map[string]any{"lastAppliedRevision": "main@sha1:bbb", "conditions": []any{ready("True", "ReconciliationSucceeded", "", 2)}, "observedGeneration": int64(2)},
				map[string]any{"path": "./apps", "sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}),
			want: []string{"drift-unchecked"}},
		{name: "nested dependencies, failed apply, stale status, source ahead",
			obj: kustomization("apps", map[string]any{
				"lastAppliedRevision": "main@sha1:aaa", "lastAttemptedRevision": "main@sha1:bbb", "observedGeneration": int64(1),
				"conditions": []any{ready("False", "HealthCheckFailed", "timeout waiting for Deployment/shop/web", 1)},
			}, spec),
			want: []string{"depends-on/1", "depends-on/2", "drift-unchecked", "ready", "revision-not-applied", "source-ahead", "stale-status"}},
		{name: "source forbidden",
			obj:    kustomization("apps", map[string]any{"conditions": []any{ready("True", "ReconciliationSucceeded", "", 0)}}, map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}),
			denied: map[schema.GroupVersionResource]bool{gitRepositoryGVR: true},
			want:   []string{"drift-unchecked", "source-unreadable"}},
		{name: "suspended without status", obj: kustomization("apps", nil, map[string]any{"suspend": true}),
			want: []string{"drift-unchecked", "ready-missing", "suspended"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.obj.SetGeneration(2)
			r := &denyingReader{fakeReader: newFakeReader(t, map[schema.GroupVersionResource][]runtime.Object{
				kustomizationsGVR: {base}, gitRepositoryGVR: {repo},
			}), denied: tc.denied}
			d := diagnoseGitOps(kustomizationsGVR, tc.obj, readerGitOpsLookups{ctx: t.Context(), reader: r, mapper: gitopsMapper(true)})
			if got := codesOf(d); !slices.Equal(got, tc.want) {
				t.Errorf("findings = %v, want %v", got, tc.want)
			}
			if d.Note == "" || !strings.Contains(d.Note, "not that live objects match Git") {
				t.Errorf("note = %q", d.Note)
			}
		})
	}
}

func TestDiagnoseKustomizationEvidence(t *testing.T) {
	t.Parallel()
	repo := uobj("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "repo", map[string]any{
		"status": map[string]any{"artifact": map[string]any{"revision": "main@sha1:bbb"}},
	})
	obj := kustomization("apps", map[string]any{
		"lastAppliedRevision": "main@sha1:aaa",
		"conditions":          []any{ready("False", "HealthCheckFailed", "timeout", 2)},
	}, map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}})
	obj.SetGeneration(2)
	obj.SetLabels(fluxLabels("flux-system"))
	r := newFakeReader(t, map[schema.GroupVersionResource][]runtime.Object{gitRepositoryGVR: {repo}})
	d := diagnoseGitOps(kustomizationsGVR, obj, readerGitOpsLookups{ctx: t.Context(), reader: r, mapper: gitopsMapper(true)})
	summary := strings.Join(d.Summary, "\n")
	for _, want := range []string{
		"source: GitRepository flux-system/repo at main@sha1:bbb",
		"applied revision: main@sha1:aaa",
		"ready: False (HealthCheckFailed) since 2026-11-23T10:00:00Z",
		"managed by: Flux Kustomization flux-system/flux-system",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary misses %q:\n%s", want, summary)
		}
	}
	for _, f := range d.Findings {
		if f.Code == "ready" && (f.Detail != "timeout" || f.Observed.IsZero() || len(f.Links) != 1) {
			t.Errorf("ready finding = %+v", f)
		}
	}
	if len(d.Backends) != 1 || d.Backends[0].GVR != gitRepositoryGVR || d.Backends[0].Name != "repo" {
		t.Errorf("backends = %+v", d.Backends)
	}
}

func TestDiagnoseArgoApplication(t *testing.T) {
	t.Parallel()
	obj := uobj("argoproj.io/v1alpha1", "Application", "argocd", "shop", map[string]any{
		"spec": map[string]any{"source": map[string]any{"repoURL": "https://git.example.com/shop.git", "path": "deploy", "targetRevision": "main"}},
		"status": map[string]any{
			"sync":           map[string]any{"status": "OutOfSync", "revision": "abc"},
			"health":         map[string]any{"status": "Degraded", "message": "Deployment web exceeded its progress deadline"},
			"reconciledAt":   "2026-11-23T10:00:00Z",
			"operationState": map[string]any{"phase": "Failed", "message": "one or more objects failed to apply", "finishedAt": "2026-11-23T09:59:00Z"},
			"conditions":     []any{map[string]any{"type": "SyncError", "message": "failed"}},
			"resources":      []any{map[string]any{"kind": "Deployment", "name": "web", "status": "OutOfSync"}},
		},
	})
	d := diagnoseGitOps(applicationsGVR, obj, readerGitOpsLookups{ctx: t.Context(), reader: newFakeReader(t, nil), mapper: gitopsMapper(true)})
	want := []string{"condition/1", "drift-unchecked", "health", "operation", "out-of-sync"}
	if got := codesOf(d); !slices.Equal(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	summary := strings.Join(d.Summary, "\n")
	for _, w := range []string{"source 1: https://git.example.com/shop.git path deploy at main", "sync: OutOfSync at revision abc, compared 2026-11-23T10:00:00Z", "resources: 1, 1 out of sync"} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary misses %q:\n%s", w, summary)
		}
	}
}

func TestHasGitOpsDiagnosis(t *testing.T) {
	t.Parallel()
	for gvr, want := range map[schema.GroupVersionResource]bool{
		kustomizationsGVR: true, helmReleasesGVR: true, applicationsGVR: true,
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1beta2", Resource: "kustomizations"}: true,
		gitRepositoryGVR: false, relDeploymentsGVR: false,
	} {
		if got := hasGitOpsDiagnosis(gvr); got != want {
			t.Errorf("%s: %v, want %v", gvr, got, want)
		}
	}
}
