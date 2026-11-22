// Package objectaction holds the built-in changes kc offers for single
// objects, such as running a CronJob now. Actions are registered per full
// GroupVersionResource; the UI lists those that match the selected object
// in the F9 menu, confirms the change and then runs it.
package objectaction

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
)

// Action is one built-in change for objects of a resource.
type Action struct {
	// ID is unique within the resource, for example "cronjob.trigger".
	ID string
	// Label is the menu entry.
	Label string
	// Verb is the API verb the action needs on the object's resource. The
	// menu hides the action when the server does not offer the verb.
	// Empty means no check.
	Verb string
	// Prepare reads the object and returns the change for the user to
	// confirm. It may block on the cache, so the UI calls it off the update
	// goroutine.
	Prepare func(ctx context.Context, env models.ActionEnv, obj models.ObjectRef) (models.ClusterAction, error)
}

// Registry maps a full GVR to the actions offered for its objects.
type Registry struct {
	byGVR map[schema.GroupVersionResource][]Action
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byGVR: make(map[schema.GroupVersionResource][]Action)}
}

// Register adds actions for gvr in menu order. It panics on an incomplete
// action or a duplicate ID, which is a programming error.
func (r *Registry) Register(gvr schema.GroupVersionResource, actions ...Action) {
	for _, a := range actions {
		if a.ID == "" || a.Label == "" || a.Prepare == nil {
			panic(fmt.Sprintf("objectaction: incomplete action %q for %s", a.ID, gvr))
		}
		if slices.ContainsFunc(r.byGVR[gvr], func(b Action) bool { return b.ID == a.ID }) {
			panic(fmt.Sprintf("objectaction: duplicate action %q for %s", a.ID, gvr))
		}
		r.byGVR[gvr] = append(r.byGVR[gvr], a)
	}
}

// For returns the actions for gvr whose verb supports allows. A nil
// supports allows every verb.
func (r *Registry) For(gvr schema.GroupVersionResource, supports func(verb string) bool) []Action {
	if r == nil {
		return nil
	}
	var out []Action
	for _, a := range r.byGVR[gvr] {
		if a.Verb != "" && supports != nil && !supports(a.Verb) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Default returns the registry with all built-in actions.
func Default() *Registry {
	r := NewRegistry()
	registerCronJobActions(r)
	return r
}
