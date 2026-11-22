package ui

import (
	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
)

// overviewKey opens /overview for the namespace the active panel lists.
// Esc i is the fallback when the terminal does not send Meta.
const overviewKey = "alt+i"

// overviewPath is the /overview row in the root of namespace's scope
// (models.AllNamespaces for the whole cluster).
func overviewPath(namespace string) PanelPath {
	return PanelPath{
		Anchor:    AnchorRoot,
		Rescope:   true,
		Namespace: namespace,
		Elems:     []PathElem{{ID: models.OverviewRowID}},
	}
}

// overviewNamespace is the namespace the overview opened from panel idx
// covers: the namespace of the object list it shows, else the panel's
// namespace scope.
func (a *App) overviewNamespace(idx int) string {
	ns := a.panelScope[idx]
	nav := a.navigatorFor(idx)
	if nav == nil {
		return ns
	}
	if lister, ok := nav.Current().(interface {
		ObjectListMeta() (schema.GroupVersionResource, string, bool)
	}); ok {
		if _, listNS, namespaced := lister.ObjectListMeta(); namespaced {
			return listNS
		}
	}
	if scoped, ok := nav.Current().(interface{ NamespaceScope() string }); ok {
		return scoped.NamespaceScope()
	}
	return ns
}

// openOverview opens /overview in the active panel, scoped to the namespace
// of the list it shows. Esc goes back to the panel root.
func (a *App) openOverview() tea.Cmd {
	idx := a.activePanel
	return a.NavigatePanel(idx, overviewPath(a.overviewNamespace(idx)), func(err error) tea.Cmd {
		if err != nil {
			a.notifyIntentErrorf("overview: %v", err)
		}
		return nil
	})
}

// applyStartupIntentOverview opens /overview in the left panel for
// kc overview: in the -n namespace, across all namespaces with -A, else in
// the context's namespace, like every other kc command.
func (a *App) applyStartupIntentOverview() tea.Cmd {
	if a.leftNav == nil {
		return nil
	}
	a.setActivePanel(0, "startup overview focus left")
	return a.NavigatePanel(0, overviewPath(a.startupIntentNamespace()), func(err error) tea.Cmd {
		if err != nil {
			a.notifyIntentErrorf("kc overview: %v", err)
		}
		return nil
	})
}
