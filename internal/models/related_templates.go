package models

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// templateSources are the workload types whose pod templates declare the
// ConfigMaps, Secrets, claims and ServiceAccount their pods will use.
// ReplicaSets are left out: a Deployment's template already declares the
// same, and a bare ReplicaSet is rare.
var templateSources = []schema.GroupVersionResource{relDeploymentsGVR, relStatefulSetsGVR, relDaemonSetsGVR, relJobsGVR, relCronJobsGVR}

// templateRelationPrefix marks a relation found in a pod template, as
// opposed to a running pod.
const templateRelationPrefix = "pod template: "

// templateUsers finds the workloads whose pod template names target
// namespace/name, also when they run no pod (scaled to zero, a CronJob
// between runs). A type the user may not list gives a partial row, and the
// other types are still read.
func templateUsers(ctx context.Context, r RelatedReader, target schema.GroupVersionResource, namespace, name string) []RelatedRef {
	var out []RelatedRef
	for _, gvr := range templateSources {
		finder, ok := DependsOnFor(gvr)
		if !ok {
			continue
		}
		list, err := r.ListByGVR(ctx, gvr, namespace)
		if err != nil {
			if partial, ok := partialRef(namespace, err); ok {
				out = append(out, partial)
			}
			continue
		}
		for i := range list.Items {
			obj := &list.Items[i]
			deps, err := finder(obj)
			if err != nil {
				continue
			}
			var vias []string
			for _, d := range deps {
				if d.GVR == target && d.Name == name {
					vias = append(vias, d.Via)
				}
			}
			if len(vias) > 0 {
				out = append(out, RelatedRef{GVR: gvr, Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: obj.GetUID(),
					Relation: templateRelationPrefix + strings.Join(vias, ", ")})
			}
		}
	}
	return out
}

// mergeRelations joins refs to the same object: a Deployment that both
// declares a ConfigMap in its template and runs pods that use it is one
// row that says both. The first occurrence keeps its place. Refs with
// different UIDs are different instances under one name and stay apart; a
// ref without a UID joins the first one of its name.
func mergeRelations(refs []RelatedRef) []RelatedRef {
	out := make([]RelatedRef, 0, len(refs))
	byUID := make(map[string]int, len(refs))
	byName := make(map[string]int, len(refs))
	for _, ref := range refs {
		if ref.GVR.Empty() {
			out = append(out, ref)
			continue
		}
		name := RelatedRowID(ref)
		var (
			i  int
			ok bool
		)
		if ref.UID != "" {
			i, ok = byUID[name+"|"+string(ref.UID)]
			if !ok {
				if j, seen := byName[name]; seen && out[j].UID == "" {
					i, ok = j, true
				}
			}
		} else {
			i, ok = byName[name]
		}
		if !ok {
			i = len(out)
			out = append(out, ref)
			if _, seen := byName[name]; !seen {
				byName[name] = i
			}
			if ref.UID != "" {
				byUID[name+"|"+string(ref.UID)] = i
			}
			continue
		}
		if out[i].UID == "" && ref.UID != "" {
			byUID[name+"|"+string(ref.UID)] = i
		}
		if ref.Relation != "" && !strings.Contains(out[i].Relation, ref.Relation) {
			out[i].Relation += "; " + ref.Relation
		}
		if out[i].UID == "" {
			out[i].UID = ref.UID
		}
	}
	return out
}
