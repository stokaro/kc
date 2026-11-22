package workloadaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// RestartedAtAnnotation is the pod template annotation kubectl rollout
// restart sets.
const RestartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

func restartable(gvr schema.GroupVersionResource) bool {
	return gvr == DeploymentsGVR || gvr == StatefulSetsGVR || gvr == DaemonSetsGVR
}

func restartAction() Action { return NewRestartAction(time.Now) }

// NewRestartAction returns the rollout restart action. now gives the
// restart time; it is read when the action is prepared, so the dialog
// shows the value that is written.
func NewRestartAction(now func() time.Time) Action {
	return Action{
		ID:    "workload.restart",
		Label: "Restart...",
		Verb:  "patch",
		Prepare: func(ctx context.Context, env Env, ref Ref) (Change, error) {
			return prepareRestart(ctx, env, ref, now())
		},
	}
}

func prepareRestart(ctx context.Context, env Env, ref Ref, now time.Time) (Change, error) {
	c := &RestartChange{env: env, ref: ref, at: now.Format(time.RFC3339)}
	switch ref.GVR {
	case DeploymentsGVR:
		var d appsv1.Deployment
		if err := getObject(ctx, env, ref, &d); err != nil {
			return nil, err
		}
		// Same refusal as kubectl: a paused Deployment would not roll out.
		if d.Spec.Paused {
			return nil, fmt.Errorf("can't restart paused deployment %s (run rollout resume first)", ref.Name)
		}
	case StatefulSetsGVR:
		var s appsv1.StatefulSet
		if err := getObject(ctx, env, ref, &s); err != nil {
			return nil, err
		}
		c.onDelete = s.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType
	case DaemonSetsGVR:
		var d appsv1.DaemonSet
		if err := getObject(ctx, env, ref, &d); err != nil {
			return nil, err
		}
		c.onDelete = d.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType
	default:
		return nil, fmt.Errorf("restarting %s is not supported", ref.GVR)
	}
	return c, nil
}

// RestartChange sets the restartedAt annotation on the pod template, as
// kubectl rollout restart does. The controller then replaces the Pods.
type RestartChange struct {
	env      Env
	ref      Ref
	at       string // RFC 3339, chosen when prepared
	onDelete bool
}

var _ Change = (*RestartChange)(nil)

// RestartedAt is the annotation value the change writes.
func (c *RestartChange) RestartedAt() string { return c.at }

func (c *RestartChange) patch() ([]byte, error) {
	return json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{RestartedAtAnnotation: c.at},
				},
			},
		},
	})
}

func (c *RestartChange) Confirmation() Confirmation {
	kind := kindOf(c.ref.GVR)
	notes := []string{
		"Sets the pod template annotation",
		RestartedAtAnnotation + ".",
		"The " + kind + " then replaces its Pods as its",
		"update strategy says.",
	}
	if c.onDelete {
		notes = append(notes,
			"The update strategy is OnDelete: Pods restart",
			"only when they are deleted.")
	}
	return Confirmation{
		Title: "Restart " + kind + "?",
		Details: [][2]string{
			{"Context", c.env.Context},
			{"Namespace", c.ref.Namespace},
			{kind, c.ref.Name},
			{"Restarted at", c.at},
		},
		Notes:  withCommand(notes, c.env.Kubectl("rollout", "restart", kubectlTarget(c.ref), "-n", c.ref.Namespace)),
		Action: "Restart",
	}
}

// Run merge-patches the annotation. The value was fixed when the change
// was prepared, so a repeated Run writes the same template and starts no
// second rollout.
func (c *RestartChange) Run(ctx context.Context) (Result, error) {
	patch, err := c.patch()
	if err != nil {
		return Result{}, err
	}
	if err := patchObject(ctx, c.env, c.ref, crclient.RawPatch(types.MergePatchType, patch), "rollout restart"); err != nil {
		return Result{}, err
	}
	return Result{Message: fmt.Sprintf("Restarted %s %s", strings.ToLower(kindOf(c.ref.GVR)), c.ref.Name)}, nil
}
