package models

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// NodeActionTarget is what the node actions (cordon, uncordon, drain) need
// from the folder that lists the nodes.
type NodeActionTarget struct {
	// Client reads from the folder's shared cache and writes to the API
	// server.
	Client crclient.Client
	// Context is the kubeconfig context name.
	Context string
	// Kubectl formats an equivalent kubectl command, adding --context when
	// needed.
	Kubectl func(args ...string) string
	// Pods lists the pods on a node from the node-scoped cache that the
	// node's /pods folder uses.
	Pods func(ctx context.Context, node string) ([]corev1.Pod, error)
	// Budgets lists the PodDisruptionBudgets of a namespace with the list
	// scope a folder would use: the shared cache, a namespace cache, or a
	// Forbidden error without a request.
	Budgets func(ctx context.Context, namespace string) ([]policyv1.PodDisruptionBudget, error)
}

// NodeActionProvider is a folder that may list nodes, such as
// ClusterObjectsFolder through its embedded ObjectsFolder.
type NodeActionProvider interface {
	NodeActions() (NodeActionTarget, bool)
}

var _ NodeActionProvider = (*ClusterObjectsFolder)(nil)

// NodeActions returns the node action target when the folder lists
// core/v1 nodes.
func (o *ObjectsFolder) NodeActions() (NodeActionTarget, bool) {
	if o == nil || o.BaseFolder == nil || o.gvr != nodeGVR || o.Deps.Cl == nil {
		return NodeActionTarget{}, false
	}
	deps := o.Deps
	return NodeActionTarget{
		Client:  deps.Cl.GetClient(),
		Context: deps.CtxName,
		Kubectl: func(args ...string) string { return kubectlCommand(deps, args...) },
		Pods:    func(ctx context.Context, node string) ([]corev1.Pod, error) { return nodePods(ctx, deps, node) },
		Budgets: func(ctx context.Context, ns string) ([]policyv1.PodDisruptionBudget, error) {
			return namespaceBudgets(ctx, deps, ns)
		},
	}, true
}

// nodePods lists the pods on node from the node-scoped shared cache. It
// reads them as unstructured, the form the /pods folder's informer holds.
// It holds the scoped cluster only for the list; the pool keeps an unused
// cluster for kubernetes.clusters.ttl, so the next round of a drain finds
// it running.
func nodePods(ctx context.Context, deps Deps, node string) ([]corev1.Pod, error) {
	scoped, release := deps.ForNodePods(node)
	defer release()
	ctrllog.FromContext(ctx).WithName("nodeaction").V(1).Info("cache list",
		"gvr", podGVR.String(), "fieldSelector", "spec.nodeName="+node)
	list, err := scoped.Cl.ListByGVR(ctx, podGVR, "")
	if err != nil {
		return nil, fmt.Errorf("listing pods on node %s: %w", node, err)
	}
	pods := make([]corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &pod); err != nil {
			return nil, fmt.Errorf("decoding pod %s/%s: %w", list.Items[i].GetNamespace(), list.Items[i].GetName(), err)
		}
		// The cache is scoped by the field selector; the check guards
		// against a wider cache, as the /pods folder does.
		if pod.Spec.NodeName == node {
			pods = append(pods, pod)
		}
	}
	return pods, nil
}

// namespaceBudgets lists the PodDisruptionBudgets of namespace from the
// cache a folder of them would use. A user who may not list and watch them
// there gets the Forbidden error and no request is sent. The namespace
// cache, when one is needed, is released at once; the pool keeps it for
// kubernetes.clusters.ttl.
func namespaceBudgets(ctx context.Context, deps Deps, namespace string) ([]policyv1.PodDisruptionBudget, error) {
	scoped, scope, release := deps.resolveListScope(ovPDBsGVR, namespace)
	defer release()
	if scope == scopeDenied || scoped.Cl == nil {
		return nil, deniedError(ovPDBsGVR, namespace, "")
	}
	ctrllog.FromContext(ctx).WithName("nodeaction").V(1).Info("cache list",
		"gvr", ovPDBsGVR.String(), "namespace", namespace, "scope", scope.String())
	list, err := scoped.Cl.ListByGVR(ctx, ovPDBsGVR, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]policyv1.PodDisruptionBudget, 0, len(list.Items))
	for i := range list.Items {
		var pdb policyv1.PodDisruptionBudget
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &pdb); err != nil {
			return nil, fmt.Errorf("decoding PodDisruptionBudget %s/%s: %w", list.Items[i].GetNamespace(), list.Items[i].GetName(), err)
		}
		out = append(out, pdb)
	}
	return out, nil
}
