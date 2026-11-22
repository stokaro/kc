package ui

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/access"
	kccluster "github.com/sttts/kc/internal/cluster"
	models "github.com/sttts/kc/internal/models"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// accessResultMsg tells the UI that access reviews finished and cached
// answers changed: the function bar and an open access view must redraw.
type accessResultMsg struct{}

// accessState holds the review cache and the client the reviews use.
type accessState struct {
	cache *access.Cache

	mu        sync.Mutex
	clientFor *kccluster.Cluster
	reviewer  access.Reviewer
	// override replaces the API reviewer in tests.
	override access.Reviewer
}

func (a *App) accessState() *accessState {
	if a.access == nil {
		a.access = &accessState{cache: access.NewCache(access.DefaultTTL, nil)}
	}
	return a.access
}

// accessIdentity names who the reviews run as: the kubeconfig, context and
// impersonation settings the cluster client uses.
func (a *App) accessIdentity() access.Identity {
	key := a.currentClusterKey()
	imp := a.impersonation()
	return access.Identity{
		Kubeconfig: key.KubeconfigPath,
		Context:    key.ContextName,
		User:       imp.User,
		UID:        imp.UID,
		Groups:     imp.Groups,
		KubectlCtx: a.kubectlCtx,
	}
}

// accessPolicy is the user's own restriction on actions: the read-only
// mode (#42) refuses every mutating check.
func (a *App) accessPolicy() access.Policy {
	return access.Policy{ReadOnly: a.readOnly()}
}

// accessReviewer returns the reviewer for the current cluster. Its client
// uses the cluster's rest.Config, so reviews carry the same credentials and
// impersonation headers as every other request kc sends.
func (a *App) accessReviewer() access.Reviewer {
	st := a.accessState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.override != nil {
		return st.override
	}
	if a.cl == nil {
		return nil
	}
	if st.clientFor != a.cl || st.reviewer == nil {
		cs, err := kubernetes.NewForConfig(a.cl.GetConfig())
		if err != nil {
			ctrllog.FromContext(a.ctx).WithName("access").Error(err, "building access review client")
			return nil
		}
		st.clientFor = a.cl
		st.reviewer = access.SSARReviewer{Client: cs.AuthorizationV1()}
	}
	return st.reviewer
}

// permitAccess gates panel actions from cached answers only; it never sends
// a request. Missing answers count as Unknown and keep the action enabled.
func (a *App) permitAccess(checks ...access.Check) bool {
	st := a.accessState()
	id := a.accessIdentity()
	return a.accessPolicy().Permits(func(c access.Check) access.Decision {
		return st.cache.Decide(id, c)
	}, checks...)
}

func objectAccessTarget(obj models.ObjectItem) access.Target {
	return access.Target{Resource: obj.GVR(), Namespace: obj.Namespace(), Name: obj.Name()}
}

var podsResource = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// accessTargetForItem maps a row to what the access view inspects: an
// object, a resource type, or the pod behind a container's logs.
func accessTargetForItem(item models.Item) (access.Target, bool) {
	switch it := item.(type) {
	case nil:
		return access.Target{}, false
	case models.Back:
		return access.Target{}, false
	case models.ObjectItem:
		t := objectAccessTarget(it)
		if t.Resource.Resource == "" {
			return access.Target{}, false
		}
		return t, true
	case models.ResourceTypeItem:
		gvr, ns := it.ResourceType()
		if gvr.Resource == "" {
			return access.Target{}, false
		}
		return access.Target{Resource: gvr, Namespace: ns}, true
	case models.LogsProvider:
		spec := it.LogsSpec()
		if spec.Pod == "" {
			return access.Target{}, false
		}
		return access.Target{Resource: podsResource, Namespace: spec.Namespace, Name: spec.Pod}, true
	}
	return access.Target{}, false
}

// gateChecksForItem lists the checks panel actions need for a row: edit and
// delete for objects, the log subresource for container log rows.
func gateChecksForItem(item models.Item) []access.Check {
	var checks []access.Check
	if logs, ok := item.(models.LogsProvider); ok {
		spec := logs.LogsSpec()
		checks = append(checks, access.LogsChecks(spec.Namespace, spec.Pod)...)
	}
	if obj, ok := item.(models.ObjectItem); ok && obj.GVR().Resource != "" {
		t := objectAccessTarget(obj)
		if obj.SupportsVerb("update") {
			checks = append(checks, access.EditChecks(t)...)
		}
		if obj.SupportsVerb("delete") {
			checks = append(checks, access.DeleteChecks(t)...)
		}
	}
	return checks
}

// accessPrefetchCmd starts the reviews the active panel's selected row still
// needs for gating. It reads only the cache on the update goroutine; the
// reviews run in the returned command.
func (a *App) accessPrefetchCmd() tea.Cmd {
	if a == nil || a.ctx == nil || a.showTerminal {
		return nil
	}
	panel := a.activePanelRef()
	if panel == nil {
		return nil
	}
	reviewer := a.accessReviewer()
	if reviewer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := panel.SelectedNavItem(ctx)
	cancel()
	if !ok {
		return nil
	}
	checks := gateChecksForItem(item)
	if len(checks) == 0 {
		return nil
	}
	st := a.accessState()
	id := a.accessIdentity()
	var need []access.Check
	for _, c := range checks {
		need = append(need, st.cache.GateChecks(id, c)...)
	}
	return a.runAccessChecks(reviewer, id, need)
}

// runAccessChecks claims the checks and returns a command that reviews them,
// or nil when every answer is fresh or already being asked.
func (a *App) runAccessChecks(reviewer access.Reviewer, id access.Identity, checks []access.Check) tea.Cmd {
	st := a.accessState()
	claimed := st.cache.Claim(id, checks...)
	if len(claimed) == 0 {
		return nil
	}
	root := a.ctx
	return func() tea.Msg {
		st.cache.Run(root, reviewer, id, claimed)
		return accessResultMsg{}
	}
}

// handleAccessResult redraws what depends on access answers.
func (a *App) handleAccessResult() tea.Cmd {
	a.invalidateFunctionBar("access results")
	a.invalidateView("access results")
	return nil
}

// batchAccessPrefetch adds the prefetch command to what Update returns
// without wrapping a nil command in a batch.
func batchAccessPrefetch(cmd, prefetch tea.Cmd) tea.Cmd {
	switch {
	case prefetch == nil:
		return cmd
	case cmd == nil:
		return prefetch
	default:
		return tea.Batch(cmd, prefetch)
	}
}
