package nodeaction

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/models"
)

// Patches for spec.unschedulable. They name the value, so sending one twice
// changes nothing. Uncordon removes the field, as kubectl uncordon does.
const (
	cordonPatch   = `{"spec":{"unschedulable":true}}`
	uncordonPatch = `{"spec":{"unschedulable":null}}`
)

// nodeUnschedulable reads spec.unschedulable of a node from the shared
// cache. It reads the node as unstructured so it shares the informer that
// the node list uses.
func nodeUnschedulable(ctx context.Context, env Env, name string) (bool, error) {
	ctrllog.FromContext(ctx).WithName("nodeaction").V(1).Info("cache get",
		"gvr", NodesGVR.String(), "name", name)
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	if err := env.Client.Get(ctx, crclient.ObjectKey{Name: name}, u); err != nil {
		return false, fmt.Errorf("reading node %s: %w", name, err)
	}
	v, _, _ := unstructured.NestedBool(u.Object, "spec", "unschedulable")
	return v, nil
}

// patchUnschedulable cordons (true) or uncordons (false) one node.
func patchUnschedulable(ctx context.Context, env Env, name string, unschedulable bool) error {
	patch := uncordonPatch
	if unschedulable {
		patch = cordonPatch
	}
	node := &corev1.Node{}
	node.Name = name
	ctrllog.FromContext(ctx).WithName("nodeaction").Info("client patch",
		"gvr", NodesGVR.String(), "name", name, "unschedulable", unschedulable)
	if err := env.Client.Patch(ctx, node, crclient.RawPatch(types.MergePatchType, []byte(patch))); err != nil {
		verb := "uncordoning"
		if unschedulable {
			verb = "cordoning"
		}
		return fmt.Errorf("%s node %s: %w", verb, name, err)
	}
	return nil
}

// CordonAction sets spec.unschedulable on one or more nodes.
type CordonAction struct {
	env    Env
	cordon bool
	nodes  []string // to change, in the order given
	same   []string // already in the target state
}

// PrepareCordon reads the nodes and returns the change that cordons
// (cordon=true) or uncordons them. Nodes that are already in that state are
// listed in the dialog and left alone.
func PrepareCordon(ctx context.Context, env Env, nodes []string, cordon bool) (*CordonAction, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no node selected")
	}
	a := &CordonAction{env: env, cordon: cordon}
	for _, n := range nodes {
		current, err := nodeUnschedulable(ctx, env, n)
		if err != nil {
			return nil, err
		}
		if current == cordon {
			a.same = append(a.same, n)
			continue
		}
		a.nodes = append(a.nodes, n)
	}
	return a, nil
}

// Nodes returns the nodes the action changes.
func (a *CordonAction) Nodes() []string { return slices.Clone(a.nodes) }

func (a *CordonAction) verb() string {
	if a.cordon {
		return "cordon"
	}
	return "uncordon"
}

// Command is the equivalent kubectl command.
func (a *CordonAction) Command() string {
	nodes := a.nodes
	if len(nodes) == 0 {
		nodes = a.same
	}
	return a.env.kubectl(append([]string{a.verb()}, nodes...)...)
}

// Confirmation describes the change for the red dialog.
func (a *CordonAction) Confirmation() models.Confirmation {
	c := models.Confirmation{Details: [][2]string{{"Context", a.env.Context}}}
	if len(a.nodes) > 0 {
		label := "Node"
		if len(a.nodes) > 1 {
			label = "Nodes"
		}
		c.Details = append(c.Details, [2]string{label, nodeList(a.nodes)})
	}
	if a.cordon {
		c.Title = "Cordon node?"
		if len(a.nodes) > 1 {
			c.Title = fmt.Sprintf("Cordon %d nodes?", len(a.nodes))
		}
		c.Notes = []string{
			"Marks the node unschedulable: no new pods are",
			"placed on it. Pods that run there go on running.",
		}
		c.Action = "Cordon"
	} else {
		c.Title = "Uncordon node?"
		if len(a.nodes) > 1 {
			c.Title = fmt.Sprintf("Uncordon %d nodes?", len(a.nodes))
		}
		c.Notes = []string{"Marks the node schedulable again."}
		c.Action = "Uncordon"
	}
	if len(a.same) > 0 {
		state := "cordoned"
		if !a.cordon {
			state = "schedulable"
		}
		c.Details = append(c.Details, [2]string{"Already " + state, nodeList(a.same)})
		if len(a.nodes) == 0 {
			c.Title = fmt.Sprintf("Nothing to do: already %s", state)
			c.Notes = nil
			c.Action = "OK"
		}
	}
	return c
}

// Run patches every node that is not in the target state yet. It stops at
// the first failure; nodes patched before it stay patched.
func (a *CordonAction) Run(ctx context.Context) (Result, error) {
	for i, n := range a.nodes {
		if err := patchUnschedulable(ctx, a.env, n, a.cordon); err != nil {
			if i > 0 {
				return Result{}, fmt.Errorf("%w (%s done: %s)", err, a.verb(), nodeList(a.nodes[:i]))
			}
			return Result{}, err
		}
	}
	past := "Cordoned"
	if !a.cordon {
		past = "Uncordoned"
	}
	switch {
	case len(a.nodes) == 0:
		return Result{Message: fmt.Sprintf("Nothing to %s: %s", a.verb(), nodeList(a.same))}, nil
	case len(a.nodes) == 1:
		return Result{Message: fmt.Sprintf("%s node %s", past, a.nodes[0])}, nil
	default:
		return Result{Message: fmt.Sprintf("%s %d nodes: %s", past, len(a.nodes), nodeList(a.nodes))}, nil
	}
}
