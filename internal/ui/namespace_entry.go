package ui

import (
	"context"
	"errors"
	"slices"
	"strings"

	kccluster "github.com/sttts/kc/internal/cluster"
	"github.com/sttts/kc/internal/models"
	"k8s.io/apimachinery/pkg/util/validation"
)

// namespaceScopes is the app's state for namespace scopes that is not a
// panel's: the namespaces used in this session, and the holds on
// namespace-scoped caches. Nothing here is saved.
type namespaceScopes struct {
	holds  scopeHolds
	recent []string // most recent first
}

// maxRecentNamespaces bounds the in-memory list of used namespaces.
const maxRecentNamespaces = 10

// errNamespaceListForbidden is the picker's reason to offer typed names.
var errNamespaceListForbidden = errors.New("listing namespaces is not allowed")

// rememberNamespace puts ns first in recent, without duplicates and at most
// maxRecentNamespaces long. All namespaces is not a name and is skipped.
func rememberNamespace(recent []string, ns string) []string {
	ns = strings.TrimSpace(ns)
	if ns == models.AllNamespaces {
		return recent
	}
	out := make([]string, 0, min(len(recent)+1, maxRecentNamespaces))
	out = append(out, ns)
	for _, r := range recent {
		if r != ns && len(out) < maxRecentNamespaces {
			out = append(out, r)
		}
	}
	return out
}

// fallbackNamespaceNames are the namespaces the picker knows when it cannot
// list them: the kubeconfig context's namespace, the panels' scopes, then
// the recently used ones.
func fallbackNamespaceNames(contextNS string, scopes [2]string, recent []string) []string {
	var names []string
	add := func(ns string) {
		ns = strings.TrimSpace(ns)
		if ns != models.AllNamespaces && !slices.Contains(names, ns) {
			names = append(names, ns)
		}
	}
	add(contextNS)
	add(scopes[0])
	add(scopes[1])
	for _, ns := range recent {
		add(ns)
	}
	return names
}

// AllowTypedEntry lets the user pick a name that is not in the list by
// typing it: while the filter is a valid namespace name that no entry has,
// it is offered after the entries the filter matches.
func (m *ScopePickerModel) AllowTypedEntry() {
	m.typed = true
	m.refilter()
}

func (m *ScopePickerModel) typedAvailable() bool {
	if !m.typed || m.filter == "" || len(validation.IsDNS1123Label(m.filter)) > 0 {
		return false
	}
	for _, c := range m.choices {
		if c.Value == m.filter {
			return false
		}
	}
	return true
}

// choiceAt is the entry in visible row i.
func (m *ScopePickerModel) choiceAt(i int) scopeChoice {
	if m.visible[i] == typedEntry {
		return scopeChoice{Label: m.filter, Value: m.filter, Note: "typed"}
	}
	return m.choices[m.visible[i]]
}

// namespaceListAllowed asks whether the user of cl may list namespaces, so
// the picker does not wait for an informer that cannot sync.
func namespaceListAllowed(ctx context.Context, cl *kccluster.Cluster) error {
	ok, err := cl.CanListWatchAllNamespaces(ctx, namespacesGVR)
	if err != nil || ok {
		// A failed review is no denial; the list itself decides.
		return nil
	}
	return errNamespaceListForbidden
}

// rememberPanelNamespace records ns as recently used.
func (a *App) rememberPanelNamespace(ns string) {
	a.namespaceScopes.recent = rememberNamespace(a.namespaceScopes.recent, ns)
}

// contextNamespace is the namespace the kubeconfig context names, or "".
func (a *App) contextNamespace() string {
	if a.currentCtx == nil {
		return ""
	}
	return strings.TrimSpace(a.currentCtx.Namespace)
}
