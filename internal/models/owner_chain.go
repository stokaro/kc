package models

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// chainGVRs are the built-in controllers an operator's custom resource may
// own on the way to its pods, in the order kc reads them.
var chainGVRs = []schema.GroupVersionResource{relDeploymentsGVR, relReplicaSetsGVR, relStatefulSetsGVR, relDaemonSetsGVR, relJobsGVR}

// maxChainDepth bounds the controller chain from a custom resource to its
// pods: CR -> Deployment -> ReplicaSet -> Pod is two intermediate hops.
const maxChainDepth = 4

// chainInput is what an owner-chain walk reads: the namespace's objects of
// each chain type, or the error that kept a type from being read.
type chainInput struct {
	Objects map[schema.GroupVersionResource][]unstructured.Unstructured
	Errs    map[schema.GroupVersionResource]error
}

// chainEdge is one controller owner edge the walk followed.
type chainEdge struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	UID       types.UID
	Owner     string // kind/name of the controller
	Depth     int
}

// readChain lists the chain types of namespace through r.
func readChain(ctx context.Context, r RelatedReader, namespace string) chainInput {
	in := chainInput{Objects: make(map[schema.GroupVersionResource][]unstructured.Unstructured), Errs: make(map[schema.GroupVersionResource]error)}
	for _, gvr := range chainGVRs {
		list, err := r.ListByGVR(ctx, gvr, namespace)
		if err != nil {
			in.Errs[gvr] = err
			continue
		}
		in.Objects[gvr] = list.Items
	}
	return in
}

// walkChain follows controller owner references down from root: every
// object whose controller is root or an object already reached, up to
// maxChainDepth hops. Edges are matched by UID only, so an object of a
// deleted owner with the same name is never included; a UID met twice
// (a cycle) is walked once. It returns the UIDs that may control pods and
// the edges, in walk order.
func walkChain(root types.UID, in chainInput) (map[types.UID]bool, []chainEdge) {
	owners := map[types.UID]bool{root: true}
	var edges []chainEdge
	frontier := map[types.UID]bool{root: true}
	for depth := 1; depth <= maxChainDepth && len(frontier) > 0; depth++ {
		next := map[types.UID]bool{}
		for _, gvr := range chainGVRs {
			for i := range in.Objects[gvr] {
				obj := &in.Objects[gvr][i]
				ctrl := metav1.GetControllerOfNoCopy(obj)
				if ctrl == nil || !frontier[ctrl.UID] || owners[obj.GetUID()] {
					continue
				}
				owners[obj.GetUID()] = true
				next[obj.GetUID()] = true
				edges = append(edges, chainEdge{GVR: gvr, Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: obj.GetUID(),
					Owner: ctrl.Kind + "/" + ctrl.Name, Depth: depth})
			}
		}
		frontier = next
	}
	return owners, edges
}

// chainFindings reports the controllers a custom resource reaches pods
// through, and the chain types kc could not read: pods owned through
// them are missing from the result.
func chainFindings(in chainInput, edges []chainEdge, namespace string) []diagFinding {
	var out []diagFinding
	if len(edges) > 0 {
		f := diagFinding{Code: "owner-chain", Severity: diagInfo,
			Title:  fmt.Sprintf("Pods are matched through %d controlled objects", len(edges)),
			Detail: "kc follows controller owner references by UID from this object, at most four hops."}
		for _, e := range edges {
			f.Links = append(f.Links, diagLink{GVR: e.GVR, Namespace: e.Namespace, Name: e.Name,
				Evidence: "controller " + e.Owner + ", uid " + string(e.UID)})
		}
		out = append(out, f)
	}
	var denied []string
	for _, gvr := range chainGVRs {
		if err := in.Errs[gvr]; err != nil {
			denied = append(denied, kubectlResource(gvr)+" ("+readProblem(err)+")")
		}
	}
	if len(denied) > 0 {
		out = append(out, diagFinding{Code: "owner-chain-partial", Severity: diagIncomplete,
			Title:  "The owner chain is partial: " + strings.Join(denied, ", ") + " could not be read in " + namespace,
			Detail: "Pods controlled through an object of these types are not counted; the edge from this object to them is unknown."})
	}
	return out
}
