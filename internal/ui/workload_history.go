package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
)

// historyVias are the kinds between the pods and a custom workload that the
// owner series of kube-state-metrics can join: kube_replicaset_owner and
// kube_job_owner name the owner of a ReplicaSet and of a Job.
var historyVias = map[schema.GroupKind]string{
	{Group: "apps", Kind: "ReplicaSet"}: "ReplicaSet",
	{Group: "batch", Kind: "Job"}:       "Job",
}

// workloadHistoryTarget is the Prometheus target of workload wl in the
// cluster connection of key, or why its history cannot be read: the owner
// series name kinds without their API group and join at most one kind
// between a pod and its workload (ADR 0364). It does no I/O.
func workloadHistoryTarget(key kccluster.Key, wl models.InfoWorkload) (promhistory.Target, error) {
	chain, ok := models.WorkloadChainOf(key, wl.GVR)
	return historyTargetOf(wl, chain, ok)
}

// historyTargetOf is workloadHistoryTarget with the chain the overview
// engines saw for the kind of wl; ok is false when they saw none.
func historyTargetOf(wl models.InfoWorkload, chain models.WorkloadChain, ok bool) (promhistory.Target, error) {
	t := promhistory.Target{Namespace: wl.Namespace, Name: wl.Name}
	if kind, builtin := promhistory.KindFor(wl.GVR.Group, wl.GVR.Resource); builtin {
		t.Kind = kind
		return t, nil
	}
	res := wl.GVR.Resource
	if wl.GVR.Group != "" {
		res += "." + wl.GVR.Group
	}
	switch {
	case !ok:
		return t, errors.New("no pod of " + res + " seen yet")
	case slices.Contains(promhistory.Kinds, chain.Kind):
		// kube_pod_owner{owner_kind="StatefulSet"} matches the pods of an
		// apps StatefulSet of the same name too.
		return t, fmt.Errorf("the owner series name kinds without their API group: %s looks like the built-in %s", res, chain.Kind)
	case len(chain.Vias) != 1:
		return t, fmt.Errorf("pods of %s are owned in %d ways (%s)", res, len(chain.Vias), viasText(chain.Vias))
	}
	via := chain.Vias[0]
	t.Kind, t.Custom = chain.Kind, true
	switch {
	case len(via) == 0:
		return t, nil
	case len(via) == 1 && historyVias[via[0]] != "":
		t.Via = historyVias[via[0]]
		return t, nil
	}
	return t, fmt.Errorf("pods of %s are owned through %s; the owner series join one ReplicaSet or Job at most", res, viasText(chain.Vias))
}

// viasText names the ways pods reach a workload: "ReplicaSet.apps",
// "directly", "ReplicaSet.apps, Deployment.apps".
func viasText(vias [][]schema.GroupKind) string {
	parts := make([]string, len(vias))
	for i, via := range vias {
		if len(via) == 0 {
			parts[i] = "directly"
			continue
		}
		kinds := make([]string, len(via))
		for j, gk := range via {
			kinds[j] = gk.String()
		}
		parts[i] = strings.Join(kinds, ", ")
	}
	return strings.Join(parts, "; ")
}
