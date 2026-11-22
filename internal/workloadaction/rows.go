package workloadaction

import (
	"context"
	"sync"

	"github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
)

// Request is what an action row sends to the UI on Enter.
type Request struct {
	Action Action
	Env    Env
	Ref    Ref
}

// Prepare runs the action's Prepare for the row's object.
func (r Request) Prepare(ctx context.Context) (Change, error) {
	return r.Action.Prepare(ctx, r.Env, r.Ref)
}

// EnvFor builds the Env for objects shown with deps.
func EnvFor(deps models.Deps) Env {
	return Env{
		Client:  deps.Cl.GetClient(),
		Context: deps.CtxName,
		Kubectl: func(args ...string) string { return models.KubectlCommand(deps, args...) },
	}
}

// actionRow is an entry like "Scale..." shown when entering a workload.
type actionRow struct {
	*models.SimpleItem
	req Request
}

var _ models.MessageRow = (*actionRow)(nil)

func (r *actionRow) EnterMessage() any { return r.req }

// Rows returns one row per action for the object.
func Rows(env Env, ref Ref, path []string) []table.Row {
	var rows []table.Row
	for _, a := range Actions(ref.GVR) {
		id := ref.GVR.GroupVersion().String() + "/" + ref.GVR.Resource + "/" + ref.Namespace + "/" + ref.Name + "/action/" + a.ID
		item := &actionRow{
			SimpleItem: models.NewSimpleItem(id, []string{a.Label}, path, models.WhiteStyle()),
			req:        Request{Action: a, Env: env, Ref: ref},
		}
		item.SetDetails(rowDetails[a.ID])
		rows = append(rows, item)
	}
	return rows
}

// rowDetails is the status line text of each action row.
var rowDetails = map[string]string{
	"workload.scale":    "Enter asks for a replica count and scales through the scale subresource",
	"workload.restart":  "Enter restarts the Pods, as kubectl rollout restart does",
	"workload.rollback": "Enter lists the revisions to roll back to, as kubectl rollout undo does",
}

var registerOnce sync.Once

// RegisterRows offers the actions as rows inside workload objects. It is
// safe to call more than once.
//
// Follow-up after #80: register the actions in objectaction.Default()
// instead and drop these rows.
func RegisterRows() {
	registerOnce.Do(func() {
		for _, gvr := range GVRs() {
			models.RegisterObjectRows(gvr, func(deps models.Deps, namespace, name string, path []string) []table.Row {
				if deps.Cl == nil {
					return nil
				}
				return Rows(EnvFor(deps), Ref{GVR: gvr, Namespace: namespace, Name: name}, path)
			})
		}
	})
}
