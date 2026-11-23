package models

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Metadata the GitOps controllers write on the objects they apply. kc
// reads it to name the controller object that claims an object, then
// checks the claim against that object's own record (a Flux inventory, an
// Argo CD resource list) where one exists. Labels alone are a claim, not
// proof: anyone can copy them.
const (
	fluxKustomizeNameLabel      = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNamespaceLabel = "kustomize.toolkit.fluxcd.io/namespace"
	fluxHelmNameLabel           = "helm.toolkit.fluxcd.io/name"
	fluxHelmNamespaceLabel      = "helm.toolkit.fluxcd.io/namespace"
	argoTrackingAnnotation      = "argocd.argoproj.io/tracking-id"
	argoInstanceLabel           = "argocd.argoproj.io/instance"
)

var (
	fluxKustomizationGK = schema.GroupKind{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}
	fluxHelmReleaseGK   = schema.GroupKind{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}
	argoApplicationGK   = schema.GroupKind{Group: "argoproj.io", Kind: "Application"}
	fluxSourceGroup     = "source.toolkit.fluxcd.io"
)

// gitopsClaim is a controller object that an object's metadata names as
// its manager.
type gitopsClaim struct {
	GK schema.GroupKind
	// Namespace is empty when the metadata does not record it (an Argo CD
	// Application outside the apps-in-any-namespace mode).
	Namespace, Name string
	// Via names the metadata that makes the claim.
	Via string
	// Mismatch is set when the metadata names a different object, such as
	// a tracking id copied with a manifest.
	Mismatch string
}

// gitopsClaims returns the managers obj's labels and annotations name, in
// a fixed order: Flux Kustomization, Flux HelmRelease, Argo CD.
func gitopsClaims(obj *unstructured.Unstructured) []gitopsClaim {
	if obj == nil {
		return nil
	}
	labels, annotations := obj.GetLabels(), obj.GetAnnotations()
	var out []gitopsClaim
	for _, f := range []struct {
		gk          schema.GroupKind
		name, space string
	}{
		{fluxKustomizationGK, fluxKustomizeNameLabel, fluxKustomizeNamespaceLabel},
		{fluxHelmReleaseGK, fluxHelmNameLabel, fluxHelmNamespaceLabel},
	} {
		if name := labels[f.name]; name != "" {
			out = append(out, gitopsClaim{GK: f.gk, Namespace: labels[f.space], Name: name, Via: "labels " + f.name + ", " + f.space})
		}
	}
	if id := annotations[argoTrackingAnnotation]; id != "" {
		out = append(out, argoTrackingClaim(obj, id))
	} else if app := labels[argoInstanceLabel]; app != "" {
		out = append(out, gitopsClaim{GK: argoApplicationGK, Name: app, Via: "label " + argoInstanceLabel})
	}
	return out
}

// argoTrackingClaim parses an Argo CD tracking id,
// "<app>:<group>/<kind>:<namespace>/<name>", where app is
// "<namespace>_<name>" for an Application outside Argo CD's own
// namespace. Argo CD ignores an id that names another object; so does kc.
func argoTrackingClaim(obj *unstructured.Unstructured, id string) gitopsClaim {
	c := gitopsClaim{GK: argoApplicationGK, Via: "annotation " + argoTrackingAnnotation}
	app, rest, ok := strings.Cut(id, ":")
	c.Name = app
	if ns, name, ok := strings.Cut(app, "_"); ok {
		c.Namespace, c.Name = ns, name
	}
	gk, nsName, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || c.Name == "" {
		c.Mismatch = "tracking id " + id + " is not <app>:<group>/<kind>:<namespace>/<name>"
		return c
	}
	group, kind, _ := strings.Cut(gk, "/")
	ns, name, _ := strings.Cut(nsName, "/")
	self := obj.GroupVersionKind()
	if group != self.Group || kind != self.Kind || ns != obj.GetNamespace() || name != obj.GetName() {
		c.Mismatch = "tracking id " + id + " names another object; Argo CD does not manage this one through it"
	}
	return c
}

func gitopsTool(gk schema.GroupKind) string {
	switch gk {
	case fluxKustomizationGK:
		return "Flux Kustomization"
	case fluxHelmReleaseGK:
		return "Flux HelmRelease"
	case argoApplicationGK:
		return "Argo CD Application"
	}
	return gk.String()
}

// gitopsWatches watches each claimed manager by name, so a row follows the
// manager's inventory and deletion. A claim without a namespace is not
// watched: it would need a watch in every namespace.
func gitopsWatches(deps Deps, claims []gitopsClaim) []relatedWatch {
	if deps.Cl == nil {
		return nil
	}
	var out []relatedWatch
	for _, c := range claims {
		gvr, ok := servedResource(deps.Cl.RESTMapper(), c.GK)
		if !ok || c.Namespace == "" || c.Mismatch != "" {
			continue
		}
		out = append(out, relatedWatch{gvr: gvr, namespace: c.Namespace, name: c.Name})
	}
	return out
}

// Problems and relations of GitOps manager rows.
const (
	problemNotServed     = "not served by the cluster"
	problemAmbiguous     = "ambiguous"
	problemNamespaceGone = "namespace not recorded"
)

// gitopsRefs resolves the claims of obj to rows of its owners folder. Each
// row says which metadata makes the claim and whether the manager's own
// record confirms it.
func gitopsRefs(ctx context.Context, r RelatedReader, mapper meta.RESTMapper, obj *unstructured.Unstructured) []RelatedRef {
	var out []RelatedRef
	for _, c := range gitopsClaims(obj) {
		out = append(out, gitopsRef(ctx, r, mapper, obj, c))
	}
	return out
}

func gitopsRef(ctx context.Context, r RelatedReader, mapper meta.RESTMapper, obj *unstructured.Unstructured, c gitopsClaim) RelatedRef {
	ref := RelatedRef{Namespace: c.Namespace, Name: c.Name, Kind: c.GK.Kind, Relation: "managed by " + gitopsTool(c.GK) + " (" + c.Via + ")"}
	if c.Mismatch != "" {
		ref.Problem = c.Mismatch
		return ref
	}
	gvr, ok := servedResource(mapper, c.GK)
	if !ok {
		ref.Problem = problemNotServed
		return ref
	}
	ref.GVR = gvr
	manager, err := findManager(ctx, r, gvr, c)
	if err != nil {
		ref.Problem = err.Error()
		return ref
	}
	ref.Namespace, ref.UID = manager.GetNamespace(), manager.GetUID()
	ref.Relation += "; " + recordVerdict(c.GK, manager, obj)
	return ref
}

// servedResource returns the resource the cluster serves for gk, in its
// preferred version.
func servedResource(mapper meta.RESTMapper, gk schema.GroupKind) (schema.GroupVersionResource, bool) {
	if mapper == nil {
		return schema.GroupVersionResource{}, false
	}
	m, err := mapper.RESTMapping(gk)
	if err != nil {
		return schema.GroupVersionResource{}, false
	}
	return m.Resource, true
}

// findManager reads the claimed manager. An Argo CD Application without a
// recorded namespace is looked up by name in every namespace the user may
// list; two matches are ambiguous.
func findManager(ctx context.Context, r RelatedReader, gvr schema.GroupVersionResource, c gitopsClaim) (*unstructured.Unstructured, error) {
	if c.Namespace != "" {
		obj, err := r.GetByGVR(ctx, gvr, c.Namespace, c.Name)
		if err != nil {
			return nil, fmt.Errorf("%s", readProblem(err))
		}
		return obj, nil
	}
	if c.GK != argoApplicationGK {
		return nil, fmt.Errorf("%s", problemNamespaceGone)
	}
	list, err := r.ListByGVR(ctx, gvr, "")
	if err != nil {
		return nil, fmt.Errorf("%s (listing applications in all namespaces)", readProblem(err))
	}
	var found []*unstructured.Unstructured
	for i := range list.Items {
		if list.Items[i].GetName() == c.Name {
			found = append(found, &list.Items[i])
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%s", problemNotFound)
	case 1:
		return found[0], nil
	}
	spaces := make([]string, 0, len(found))
	for _, f := range found {
		spaces = append(spaces, f.GetNamespace())
	}
	return nil, fmt.Errorf("%s: Applications named %s in %s", problemAmbiguous, c.Name, strings.Join(spaces, ", "))
}

// recordVerdict says whether the manager's own record lists obj: a Flux
// Kustomization's status.inventory, an Argo CD Application's
// status.resources. A HelmRelease keeps no object list in its status.
func recordVerdict(gk schema.GroupKind, manager, obj *unstructured.Unstructured) string {
	self := obj.GroupVersionKind()
	switch gk {
	case fluxKustomizationGK:
		// An inventory with no entries may be written as null.
		_, found, _ := unstructured.NestedFieldNoCopy(manager.Object, "status", "inventory")
		entries, _, _ := unstructured.NestedSlice(manager.Object, "status", "inventory", "entries")
		if !found {
			return "label only: the Kustomization has no inventory"
		}
		want := fmt.Sprintf("%s_%s_%s_%s", obj.GetNamespace(), obj.GetName(), self.Group, self.Kind)
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok && m["id"] == want {
				return "listed in its inventory"
			}
		}
		return "label only: not in its inventory (pruned from the source, or the labels were copied)"
	case argoApplicationGK:
		resources, found, _ := unstructured.NestedSlice(manager.Object, "status", "resources")
		if !found {
			return "annotation only: the Application lists no resources"
		}
		for _, e := range resources {
			m, ok := e.(map[string]any)
			if ok && m["group"] == nilIfEmpty(self.Group) && m["kind"] == self.Kind && m["name"] == obj.GetName() && m["namespace"] == nilIfEmpty(obj.GetNamespace()) {
				return "listed in its status.resources"
			}
		}
		return "annotation only: not in its status.resources"
	}
	return "label only: a HelmRelease records no object list"
}

// nilIfEmpty matches an optional string field of unstructured JSON: an
// empty value is left out of the object.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
