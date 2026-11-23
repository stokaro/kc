// Package nodeaction holds kc's built-in node changes: cordon, uncordon and
// drain. Each action is prepared first (it reads the nodes and their pods
// from the shared cache and returns what it would do), the UI confirms
// it, and only then Run writes to the API server.
//
// The shape follows the object-action registry proposed in PR #80
// (Prepare, then Confirmation and Run), so the actions can be registered
// there for core/v1 nodes with a few lines once it lands.
package nodeaction

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// NodesGVR is the resource the node actions apply to.
var NodesGVR = corev1.SchemeGroupVersion.WithResource("nodes")

// PodLister returns the pods scheduled on a node. kc backs it with the
// node-scoped shared cache that the node's /pods folder uses, so a drain
// starts no cluster-wide pod watch.
type PodLister func(ctx context.Context, node string) ([]corev1.Pod, error)

// Env is what the node actions need from the folder that lists the nodes.
// It mirrors models.ActionEnv from PR #80 and adds the pod lister.
type Env struct {
	// Client reads from the shared cache and writes to the API server.
	Client crclient.Client
	// Context is the kubeconfig context name shown in the dialogs.
	Context string
	// Kubectl formats the equivalent kubectl command, adding --context
	// when needed.
	Kubectl func(args ...string) string
	// Pods lists the pods on a node. Drain needs it; cordon does not.
	Pods PodLister
	// Budgets lists a namespace's PodDisruptionBudgets for the drain's
	// impact preview. Without it the preview names no budgets.
	Budgets BudgetLister
}

func (e Env) kubectl(args ...string) string {
	if e.Kubectl != nil {
		return e.Kubectl(args...)
	}
	return "kubectl " + strings.Join(args, " ")
}

// Result reports what a finished action did.
type Result struct {
	// Message is shown on the command line.
	Message string
}

// nodeList names nodes for a dialog line: all of them when there are few,
// else the first ones and a count.
func nodeList(nodes []string) string {
	const shown = 3
	if len(nodes) <= shown {
		return strings.Join(nodes, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(nodes[:shown], ", "), len(nodes)-shown)
}

// plural returns "1 pod" or "3 pods".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
