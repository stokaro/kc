// Package workloadaction holds kc's built-in changes for workloads: scale,
// rollout restart and rollback. Each action reads the object first
// (Prepare), may ask the user for a value or a choice, and returns a
// change that the UI confirms in the red dialog and then runs.
//
// The shapes follow the object-action registry proposed in PR #80
// (objectaction.Action, models.ClusterAction, models.ActionEnv), so that
// the actions can move into that registry with small changes.
package workloadaction

import (
	"context"
	"strings"

	"github.com/sttts/kc/internal/models"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	DeploymentsGVR  = appsv1.SchemeGroupVersion.WithResource("deployments")
	StatefulSetsGVR = appsv1.SchemeGroupVersion.WithResource("statefulsets")
	DaemonSetsGVR   = appsv1.SchemeGroupVersion.WithResource("daemonsets")
	ReplicaSetsGVR  = appsv1.SchemeGroupVersion.WithResource("replicasets")
)

// Env is what an action needs from the folder that shows the object. It
// matches models.ActionEnv from PR #80.
type Env struct {
	// Client reads from the shared cache and writes to the API server.
	Client crclient.Client
	// Context is the kubeconfig context name shown in the dialog.
	Context string
	// Kubectl formats the equivalent kubectl command.
	Kubectl func(args ...string) string
}

// Ref names one object by its full GVR. It matches models.ObjectRef from
// PR #80.
type Ref struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
}

// Confirmation is the text of the red dialog.
type Confirmation = models.Confirmation

// Result reports what a change did.
type Result struct {
	// Message is shown on the command line.
	Message string
}

// Change is a confirmed change to the cluster. It matches
// models.ClusterAction from PR #80. Run calls the API server, so the UI
// runs it off the update goroutine.
type Change interface {
	Confirmation() models.Confirmation
	Run(ctx context.Context) (Result, error)
}

// Question asks the user for one value before the confirmation.
type Question struct {
	Title       string
	Label       string
	Initial     string
	Description string
}

// Asker is a prepared change that wants a value from the user first, such
// as the replica count. Answer returns the change for that value, or an
// error the dialog shows next to the field.
type Asker interface {
	Question() Question
	Answer(value string) (Change, error)
}

// Choice is one entry of a pick list.
type Choice struct {
	Label string
	Value string
	Note  string
}

// Chooser is a prepared change that wants the user to pick one entry
// first, such as the revision to roll back to.
type Chooser interface {
	Choices() (title string, choices []Choice, selected string)
	Choose(value string) (Change, error)
}

// Action is one built-in change for objects of a resource. It matches
// objectaction.Action from PR #80, except that Prepare returns a Change.
type Action struct {
	ID    string
	Label string
	// Verb is the API verb the action needs on the object's resource.
	Verb string
	// Prepare reads the object and returns the change. It may block on the
	// cache or the API server, so the UI calls it off the update goroutine.
	Prepare func(ctx context.Context, env Env, ref Ref) (Change, error)
}

// Actions returns the actions for gvr in menu order.
func Actions(gvr schema.GroupVersionResource) []Action {
	var out []Action
	if scalable(gvr) {
		out = append(out, scaleAction())
	}
	if restartable(gvr) {
		out = append(out, restartAction())
	}
	if gvr == DeploymentsGVR {
		out = append(out, rollbackAction())
	}
	return out
}

// GVRs lists the resources that have actions.
func GVRs() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{DeploymentsGVR, StatefulSetsGVR, DaemonSetsGVR, ReplicaSetsGVR}
}

// kindOf names the kind of a supported resource for dialogs and errors.
func kindOf(gvr schema.GroupVersionResource) string {
	switch gvr {
	case DeploymentsGVR:
		return "Deployment"
	case StatefulSetsGVR:
		return "StatefulSet"
	case DaemonSetsGVR:
		return "DaemonSet"
	case ReplicaSetsGVR:
		return "ReplicaSet"
	}
	return gvr.Resource
}

// kubectlTarget names an object the way kubectl accepts it, with the group
// so that the name stays unique across API groups: deployments.apps/web.
func kubectlTarget(ref Ref) string {
	res := ref.GVR.Resource
	if ref.GVR.Group != "" {
		res += "." + ref.GVR.Group
	}
	return res + "/" + ref.Name
}

// withCommand appends the equivalent kubectl command to the notes,
// wrapped to the dialog width.
//
// Follow-up after #80: set models.Confirmation.Command instead.
func withCommand(notes []string, cmd string) []string {
	out := append([]string(nil), notes...)
	out = append(out, "", "Same as:")
	return append(out, wrapWords(cmd, noteWidth)...)
}

// noteWidth keeps note lines narrow enough for the red dialog.
const noteWidth = 50

// wrapWords breaks s into lines of at most width characters at spaces,
// indented by two spaces after the first line. A longer word gets a line
// of its own.
func wrapWords(s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) > width:
			lines = append(lines, line)
			line = "  " + w
		default:
			line += " " + w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
