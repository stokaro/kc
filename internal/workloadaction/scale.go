package workloadaction

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const scaleSubresource = "scale"

func scalable(gvr schema.GroupVersionResource) bool {
	return gvr == DeploymentsGVR || gvr == StatefulSetsGVR || gvr == ReplicaSetsGVR
}

func scaleAction() Action {
	return Action{ID: "workload.scale", Label: "Scale...", Verb: "patch", Prepare: PrepareScale}
}

// typedScaleTarget is the typed object whose scale subresource is read.
func typedScaleTarget(ref Ref) (crclient.Object, error) {
	meta := metav1.ObjectMeta{Namespace: ref.Namespace, Name: ref.Name}
	switch ref.GVR {
	case DeploymentsGVR:
		return &appsv1.Deployment{ObjectMeta: meta}, nil
	case StatefulSetsGVR:
		return &appsv1.StatefulSet{ObjectMeta: meta}, nil
	case ReplicaSetsGVR:
		return &appsv1.ReplicaSet{ObjectMeta: meta}, nil
	}
	return nil, fmt.Errorf("scaling %s is not supported", ref.GVR)
}

// PrepareScale reads the current replica count through the scale
// subresource. The returned change keeps that count until the user
// answers its Question.
func PrepareScale(ctx context.Context, env Env, ref Ref) (Change, error) {
	obj, err := typedScaleTarget(ref)
	if err != nil {
		return nil, err
	}
	ctrllog.FromContext(ctx).WithName("workloadaction").Info("client get",
		"gvr", ref.GVR.String(), "subresource", scaleSubresource, "namespace", ref.Namespace, "name", ref.Name)
	scale := &autoscalingv1.Scale{}
	if err := env.Client.SubResource(scaleSubresource).Get(ctx, obj, scale); err != nil {
		return nil, fmt.Errorf("reading scale of %s %s/%s: %w", kindOf(ref.GVR), ref.Namespace, ref.Name, err)
	}
	c := &ScaleChange{env: env, ref: ref, current: scale.Spec.Replicas, replicas: scale.Spec.Replicas}
	if ref.GVR == ReplicaSetsGVR {
		// A ReplicaSet under a Deployment is scaled back by its controller;
		// the dialog says so. A failed read only drops that note.
		var rs appsv1.ReplicaSet
		if err := getObject(ctx, env, ref, &rs); err == nil {
			if owner := metav1.GetControllerOf(&rs); owner != nil {
				c.controller = owner.Kind + " " + owner.Name
			}
		}
	}
	return c, nil
}

// ScaleChange sets the replica count of a Deployment, StatefulSet or
// ReplicaSet through its scale subresource, as kubectl scale does.
type ScaleChange struct {
	env        Env
	ref        Ref
	current    int32
	replicas   int32
	controller string // "Deployment web" for a controlled ReplicaSet
}

var (
	_ Change = (*ScaleChange)(nil)
	_ Asker  = (*ScaleChange)(nil)
)

// Current is the replica count read when the action was prepared.
func (c *ScaleChange) Current() int32 { return c.current }

// Replicas is the replica count the change sets.
func (c *ScaleChange) Replicas() int32 { return c.replicas }

// Question asks for the replica count, prefilled with the current one.
func (c *ScaleChange) Question() Question {
	return Question{
		Title:       "Scale " + kindOf(c.ref.GVR),
		Label:       "Replicas for " + c.ref.Name + ":",
		Initial:     strconv.Itoa(int(c.current)),
		Description: fmt.Sprintf("Now %d", c.current),
	}
}

// Answer parses the replica count.
func (c *ScaleChange) Answer(value string) (Change, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || n < 0 {
		return nil, fmt.Errorf("enter a whole number, 0 or more")
	}
	next := *c
	next.replicas = int32(n)
	return &next, nil
}

func (c *ScaleChange) patch() []byte {
	return fmt.Appendf(nil, `{"spec":{"replicas":%d}}`, c.replicas)
}

func (c *ScaleChange) Confirmation() Confirmation {
	kind := kindOf(c.ref.GVR)
	var notes []string
	if c.replicas == 0 {
		notes = append(notes, "Scaling to 0 stops every Pod of the "+kind+".")
	}
	if c.controller != "" {
		notes = append(notes,
			"The ReplicaSet is controlled by "+c.controller+",",
			"which sets the replica count back.")
	}
	notes = append(notes,
		"A HorizontalPodAutoscaler that targets it may",
		"change the count again.")
	return Confirmation{
		Title: "Scale " + kind + "?",
		Details: [][2]string{
			{"Context", c.env.Context},
			{"Namespace", c.ref.Namespace},
			{kind, c.ref.Name},
			{"Replicas", fmt.Sprintf("%d -> %d", c.current, c.replicas)},
		},
		Notes: withCommand(notes, c.env.Kubectl("scale", kubectlTarget(c.ref),
			fmt.Sprintf("--replicas=%d", c.replicas), "-n", c.ref.Namespace)),
		Action: "Scale",
	}
}

// Run merge-patches spec.replicas on the scale subresource. The patch
// names the count, so sending it twice changes nothing.
func (c *ScaleChange) Run(ctx context.Context) (Result, error) {
	// The object is unstructured so that the response, a Scale, decodes
	// into it; a typed workload could not hold it.
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvkOf(c.ref.GVR))
	u.SetNamespace(c.ref.Namespace)
	u.SetName(c.ref.Name)
	ctrllog.FromContext(ctx).WithName("workloadaction").Info("client patch",
		"gvr", c.ref.GVR.String(), "subresource", scaleSubresource, "namespace", c.ref.Namespace, "name", c.ref.Name, "replicas", c.replicas)
	if err := c.env.Client.SubResource(scaleSubresource).Patch(ctx, u, crclient.RawPatch(types.MergePatchType, c.patch())); err != nil {
		return Result{}, fmt.Errorf("scaling %s %s/%s: %w", kindOf(c.ref.GVR), c.ref.Namespace, c.ref.Name, err)
	}
	return Result{Message: fmt.Sprintf("Scaled %s %s to %d", strings.ToLower(kindOf(c.ref.GVR)), c.ref.Name, c.replicas)}, nil
}
