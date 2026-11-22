package workloadaction

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	deploymentutil "k8s.io/kubectl/pkg/util/deployment"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

func rollbackAction() Action {
	return Action{ID: "workload.rollback", Label: "Roll back...", Verb: "patch", Prepare: PrepareRollback}
}

// Revision is one ReplicaSet of a Deployment, numbered by the revision
// annotation the Deployment controller sets.
type Revision struct {
	Number     int64
	ReplicaSet string
	Images     []string
	// Current is true when the ReplicaSet's template is the Deployment's
	// template, ignoring the pod-template-hash label.
	Current bool

	template    corev1.PodTemplateSpec
	annotations map[string]string
}

// Revisions lists the ReplicaSets that deploy controls, newest revision
// first. ReplicaSets without a valid revision annotation are left out,
// as kubectl rollout history does.
func Revisions(deploy *appsv1.Deployment, replicaSets []appsv1.ReplicaSet) []Revision {
	var out []Revision
	for i := range replicaSets {
		rs := &replicaSets[i]
		if !metav1.IsControlledBy(rs, deploy) {
			continue
		}
		n, err := deploymentutil.Revision(rs)
		if err != nil || n <= 0 {
			continue
		}
		var images []string
		for _, c := range rs.Spec.Template.Spec.Containers {
			images = append(images, c.Image)
		}
		out = append(out, Revision{
			Number:      n,
			ReplicaSet:  rs.Name,
			Images:      images,
			Current:     equalIgnoreHash(&rs.Spec.Template, &deploy.Spec.Template),
			template:    *rs.Spec.Template.DeepCopy(),
			annotations: rs.Annotations,
		})
	}
	slices.SortFunc(out, func(a, b Revision) int { return cmp.Compare(b.Number, a.Number) })
	return out
}

// equalIgnoreHash compares pod templates without the pod-template-hash
// label, as kubectl does before a rollback.
func equalIgnoreHash(a, b *corev1.PodTemplateSpec) bool {
	a, b = a.DeepCopy(), b.DeepCopy()
	delete(a.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	delete(b.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	return apiequality.Semantic.DeepEqual(a, b)
}

// PrepareRollback reads the Deployment and its ReplicaSets from the shared
// cache. The returned change targets the newest revision that is not the
// current template, which is what kubectl rollout undo picks; the user
// may choose another one.
func PrepareRollback(ctx context.Context, env Env, ref Ref) (Change, error) {
	if ref.GVR != DeploymentsGVR {
		return nil, fmt.Errorf("rolling back %s is not supported", ref.GVR)
	}
	deploy := &appsv1.Deployment{}
	if err := getObject(ctx, env, ref, deploy); err != nil {
		return nil, err
	}
	if deploy.Spec.Paused {
		return nil, fmt.Errorf("can't roll back paused deployment %s (run rollout resume first)", ref.Name)
	}
	replicaSets, err := listReplicaSets(ctx, env, ref.Namespace)
	if err != nil {
		return nil, err
	}
	revs := Revisions(deploy, replicaSets)
	target := slices.IndexFunc(revs, func(r Revision) bool { return !r.Current })
	if target < 0 {
		return nil, fmt.Errorf("deployment %s has no previous revision", ref.Name)
	}
	return &RollbackChange{env: env, ref: ref, deploy: deploy, revisions: revs, target: target}, nil
}

func listReplicaSets(ctx context.Context, env Env, namespace string) ([]appsv1.ReplicaSet, error) {
	ctrllog.FromContext(ctx).WithName("workloadaction").Info("client list",
		"gvr", ReplicaSetsGVR.String(), "namespace", namespace)
	ul := &unstructured.UnstructuredList{}
	ul.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("ReplicaSetList"))
	if err := env.Client.List(ctx, ul, crclient.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing replicasets in %s: %w", namespace, err)
	}
	out := make([]appsv1.ReplicaSet, len(ul.Items))
	for i := range ul.Items {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(ul.Items[i].Object, &out[i]); err != nil {
			return nil, fmt.Errorf("decoding replicaset %s/%s: %w", namespace, ul.Items[i].GetName(), err)
		}
	}
	return out, nil
}

// RollbackChange copies the pod template of an earlier ReplicaSet into the
// Deployment, as kubectl rollout undo does.
type RollbackChange struct {
	env       Env
	ref       Ref
	deploy    *appsv1.Deployment
	revisions []Revision
	target    int
}

var (
	_ Change  = (*RollbackChange)(nil)
	_ Chooser = (*RollbackChange)(nil)
)

// Target is the revision the change restores.
func (c *RollbackChange) Target() Revision { return c.revisions[c.target] }

// Choices lists the revisions, newest first, with the current one marked.
func (c *RollbackChange) Choices() (string, []Choice, string) {
	choices := make([]Choice, 0, len(c.revisions))
	for _, r := range c.revisions {
		note := strings.Join(r.Images, ",")
		if r.Current {
			note = "current"
		}
		choices = append(choices, Choice{
			Label: fmt.Sprintf("%d  %s", r.Number, r.ReplicaSet),
			Value: strconv.FormatInt(r.Number, 10),
			Note:  note,
		})
	}
	return "Roll back " + c.ref.Name + " to revision", choices, strconv.FormatInt(c.Target().Number, 10)
}

// Choose returns the change for revision value.
func (c *RollbackChange) Choose(value string) (Change, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("bad revision %q", value)
	}
	i := slices.IndexFunc(c.revisions, func(r Revision) bool { return r.Number == n })
	if i < 0 {
		return nil, fmt.Errorf("deployment %s has no revision %d", c.ref.Name, n)
	}
	if c.revisions[i].Current {
		return nil, fmt.Errorf("revision %d is the current template of deployment %s", n, c.ref.Name)
	}
	next := *c
	next.target = i
	return &next, nil
}

func (c *RollbackChange) currentNumber() string {
	for _, r := range c.revisions {
		if r.Current {
			return strconv.FormatInt(r.Number, 10)
		}
	}
	return c.deploy.Annotations[deploymentutil.RevisionAnnotation]
}

func (c *RollbackChange) Confirmation() Confirmation {
	t := c.Target()
	notes := []string{
		"Copies the pod template of ReplicaSet",
		t.ReplicaSet + " into the Deployment, as",
		"kubectl rollout undo does. The Deployment rolls",
		"out new Pods; the restored template gets the",
		"next revision number.",
	}
	if len(t.Images) > 0 {
		notes = append(notes, "")
		notes = append(notes, wrapWords("Images: "+strings.Join(t.Images, " "), noteWidth)...)
	}
	return Confirmation{
		Title: "Roll back Deployment?",
		Details: [][2]string{
			{"Context", c.env.Context},
			{"Namespace", c.ref.Namespace},
			{"Deployment", c.ref.Name},
			{"From revision", c.currentNumber()},
			{"To revision", strconv.FormatInt(t.Number, 10)},
		},
		Notes: withCommand(notes, c.env.Kubectl("rollout", "undo", kubectlTarget(c.ref),
			"--to-revision="+strconv.FormatInt(t.Number, 10), "-n", c.ref.Namespace)),
		Action: "Roll back",
	}
}

// annotationsToSkip are kept from the Deployment and not copied from the
// ReplicaSet, the same list kubectl uses
// (k8s.io/kubectl/pkg/polymorphichelpers/rollback.go).
var annotationsToSkip = map[string]bool{
	corev1.LastAppliedConfigAnnotation:       true,
	deploymentutil.RevisionAnnotation:        true,
	deploymentutil.RevisionHistoryAnnotation: true,
	deploymentutil.DesiredReplicasAnnotation: true,
	deploymentutil.MaxReplicasAnnotation:     true,
	appsv1.DeprecatedRollbackTo:              true,
}

// Patch is the JSON patch that restores the target revision: the
// ReplicaSet's template without the pod-template-hash label, and the
// Deployment's annotations with the ReplicaSet's copied over.
func (c *RollbackChange) Patch() ([]byte, error) {
	t := c.Target()
	template := t.template.DeepCopy()
	delete(template.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	annotations := map[string]string{}
	for k := range annotationsToSkip {
		if v, ok := c.deploy.Annotations[k]; ok {
			annotations[k] = v
		}
	}
	for k, v := range t.annotations {
		if !annotationsToSkip[k] {
			annotations[k] = v
		}
	}
	// kubectl uses "replace" for both. "add" sets an existing member the
	// same way and also works when the Deployment has no annotations.
	return json.Marshal([]map[string]any{
		{"op": "replace", "path": "/spec/template", "value": template},
		{"op": "add", "path": "/metadata/annotations", "value": annotations},
	})
}

// Run sends the patch. Sending it twice writes the same template again,
// which starts no second rollout.
func (c *RollbackChange) Run(ctx context.Context) (Result, error) {
	patch, err := c.Patch()
	if err != nil {
		return Result{}, err
	}
	t := c.Target()
	if err := patchObject(ctx, c.env, c.ref, crclient.RawPatch(types.JSONPatchType, patch), "rollback to revision "+strconv.FormatInt(t.Number, 10)); err != nil {
		return Result{}, err
	}
	return Result{Message: fmt.Sprintf("Rolled back deployment %s to revision %d", c.ref.Name, t.Number)}, nil
}
