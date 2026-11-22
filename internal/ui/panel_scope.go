package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sttts/kc/internal/models"
	navui "github.com/sttts/kc/internal/navigation"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// newScopedNavigator returns a navigator whose root is scoped to the panel's
// namespace.
func (a *App) newScopedNavigator(idx int) *navui.Navigator {
	panel := a.panelByIndex(idx)
	cfg := a.ensurePanelConfig(panel)
	a.syncPanelConfig(panel)
	deps := a.makeDeps(a.cl, cfg, a.currentClusterKey())
	return navui.NewNavigator(models.NewRootFolder(deps, a.panelScope[idx]))
}

// showNavigatorRoot puts the panel on its navigator's current folder.
func (a *App) showNavigatorRoot(idx int) {
	nav := a.navigatorFor(idx)
	panel := a.panelByIndex(idx)
	if nav == nil || panel == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	a.setPanelFolder(ctx, idx, nav.Current(), nav.HasBack())
	panel.SetCurrentPath(a.navigatorPath(nav))
	panel.UseFolder(ctx, true)
	a.applyResourceOptions(panel)
	panel.ResetSelectionTop(ctx)
	a.syncPanelScopeLabels()
}

func (a *App) navigatorFor(idx int) *navui.Navigator {
	if idx == 0 {
		return a.leftNav
	}
	return a.rightNav
}

// namespaceScopeLabel is how a namespace scope reads in the panel frame.
func namespaceScopeLabel(ns string) string {
	if ns == models.AllNamespaces {
		return "all namespaces"
	}
	return ns
}

// panelScopeLabel is what the panel frame shows as its scope.
type panelScopeLabel struct {
	Context   string
	Namespace string // models.AllNamespaces for all
}

// scopeHits are the frame columns of the context and namespace labels.
type scopeHits struct {
	ctxFrom, ctxTo       int
	nsFrom, nsTo         int
	filterFrom, filterTo int // the row filter label; a click clears it
}

var (
	scopeContextStyle   = lipgloss.NewStyle().Background(lipgloss.Blue).Foreground(lipgloss.BrightYellow).Bold(true)
	scopeNamespaceStyle = lipgloss.NewStyle().Background(lipgloss.Blue).Foreground(lipgloss.BrightCyan).Bold(true)
	scopeSepStyle       = lipgloss.NewStyle().Background(lipgloss.Blue).Foreground(lipgloss.White)
)

// SetScope sets the context and namespace drawn in the frame.
func (p *Panel) SetScope(scope panelScopeLabel) {
	if p.scope == scope {
		return
	}
	p.scope = scope
	p.invalidateRenderCache()
}

// renderScopeLabel renders "─ ctx : ns " after the bottom-left corner, which
// is offset columns wide, in at most maxWidth columns.
func renderScopeLabel(scope panelScopeLabel, dash string, offset, maxWidth int) (string, scopeHits) {
	if scope.Context == "" || maxWidth < 8 {
		return "", scopeHits{}
	}
	ctx := " " + scope.Context + " "
	ns := " " + namespaceScopeLabel(scope.Namespace) + " "
	room := maxWidth - lipgloss.Width(dash) - 1 // the ":" between them
	if lipgloss.Width(ctx)+lipgloss.Width(ns) > room {
		half := max(4, room/2)
		ctx = truncateStringToWidth(ctx, half)
		ns = truncateStringToWidth(ns, max(4, room-lipgloss.Width(ctx)))
	}
	var hits scopeHits
	hits.ctxFrom = offset + lipgloss.Width(dash)
	hits.ctxTo = hits.ctxFrom + lipgloss.Width(ctx)
	hits.nsFrom = hits.ctxTo + 1
	hits.nsTo = hits.nsFrom + lipgloss.Width(ns)
	out := dash + scopeContextStyle.Render(ctx) + scopeSepStyle.Render(":") + scopeNamespaceStyle.Render(ns)
	return out, hits
}

// scopeHit reports which scope label is at frame column x: "context",
// "namespace", "filter", or "".
func (p *Panel) scopeHit(x int) string {
	h := p.scopeHits
	switch {
	case x >= h.ctxFrom && x < h.ctxTo:
		return "context"
	case x >= h.nsFrom && x < h.nsTo:
		return "namespace"
	case x >= h.filterFrom && x < h.filterTo:
		return "filter"
	}
	return ""
}

// namespacesLoadedMsg carries the namespace names for a panel's picker.
type namespacesLoadedMsg struct {
	panel int
	names []string
	err   error
}

var namespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// loadNamespacesForPicker lists namespaces off the UI thread; the picker
// opens when they arrive.
func (a *App) loadNamespacesForPicker(idx int) tea.Cmd {
	cl := a.cl
	if cl == nil {
		return nil
	}
	parent := a.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, requestTimeout)
		defer cancel()
		list, err := cl.ListByGVR(ctx, namespacesGVR, "")
		if err != nil {
			return namespacesLoadedMsg{panel: idx, err: err}
		}
		names := make([]string, 0, len(list.Items))
		for i := range list.Items {
			names = append(names, list.Items[i].GetName())
		}
		sort.Strings(names)
		return namespacesLoadedMsg{panel: idx, names: names}
	}
}

// namespaceChoices puts "All namespaces" first, then the names.
func namespaceChoices(names []string, current string) []scopeChoice {
	choices := []scopeChoice{{Label: "All namespaces", Value: models.AllNamespaces, Note: "-A"}}
	for _, n := range names {
		c := scopeChoice{Label: n, Value: n}
		choices = append(choices, c)
	}
	for i := range choices {
		if choices[i].Value == current {
			choices[i].Note = strings.TrimSpace(choices[i].Note + " current")
		}
	}
	return choices
}

// showNamespacePicker opens the namespace picker for a panel.
func (a *App) showNamespacePicker(msg namespacesLoadedMsg) tea.Cmd {
	if msg.err != nil {
		return a.ShowToast("Could not list namespaces: "+msg.err.Error(), 3*time.Second)
	}
	idx := msg.panel
	current := a.panelScope[idx]
	picker := NewScopePickerModel(namespaceChoices(msg.names, current), current, func(c scopeChoice) tea.Cmd {
		a.hideModal()
		a.setPanelScope(idx, c.Value)
		return nil
	}, func() tea.Cmd {
		a.hideModal()
		return nil
	})
	modal := NewModal("Namespace", picker)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register("namespace_picker", modal)
	a.configureModalWindow(modal, picker, idx, "", 40, min(len(msg.names)+3, 20))
	a.showModal("namespace_picker")
	return nil
}

// setPanelScope changes one panel's namespace scope. A panel showing a
// namespaced resource list stays on that resource in the new scope; anything
// deeper is specific to the old namespace, so the panel goes to its root.
func (a *App) setPanelScope(idx int, ns string) {
	if idx < 0 || idx > 1 {
		return
	}
	nav := a.navigatorFor(idx)
	var keep *schema.GroupVersionResource
	if nav != nil && a.navigatorDepth(nav) == 2 {
		if g, ok := nav.Current().(interface {
			GVR() schema.GroupVersionResource
			Namespace() string
		}); ok && g.Namespace() == a.panelScope[idx] {
			gvr := g.GVR()
			keep = &gvr
		}
	}
	a.panelScope[idx] = ns
	newNav := a.newScopedNavigator(idx)
	a.replaceNavigator(idx, newNav)
	if keep != nil {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		_, _ = navui.GoTo(ctx, newNav, []navui.GoToStep{{SelectionID: resourceRowID(ns, *keep, true), Enter: true}})
		cancel()
	}
	a.showNavigatorRoot(idx)
	a.syncPanelScopeLabels()
	if idx == a.activePanel {
		a.updateTermContextNamespace(ns)
	}
}

// syncPanelScopeLabels updates the scope drawn in both panel frames.
func (a *App) syncPanelScopeLabels() {
	ctxName := ""
	if a.currentCtx != nil {
		ctxName = a.currentCtx.Name
	}
	for idx, panel := range []*Panel{a.leftPanel, a.rightPanel} {
		if panel != nil {
			panel.SetScope(panelScopeLabel{Context: ctxName, Namespace: a.panelScope[idx]})
		}
	}
}

// handleScopeClick opens a picker when a click lands on a panel's scope
// label. It reports whether the click was on a label.
func (a *App) handleScopeClick(m tea.Mouse) (tea.Cmd, bool) {
	idx, hit := a.scopeHitAt(m)
	switch hit {
	case "namespace":
		a.setActivePanel(idx, "scope click")
		return a.loadNamespacesForPicker(idx), true
	case "context":
		a.setActivePanel(idx, "scope click")
		return a.showContextPicker(), true
	case "filter":
		a.setActivePanel(idx, "filter click")
		a.clearRowFilter(idx)
		return nil, true
	}
	return nil, false
}

// scopeHitAt returns the panel and the scope label ("context",
// "namespace", "filter" or "") under the mouse.
func (a *App) scopeHitAt(m tea.Mouse) (int, string) {
	leftWidth, rightWidth, panelHeight, _ := a.panelAreaMetrics()
	if m.Y != panelHeight-1 {
		return 0, ""
	}
	idx, x := 0, m.X
	if leftWidth <= 0 || (rightWidth > 0 && m.X >= leftWidth) {
		idx, x = 1, m.X-max(0, leftWidth)
	}
	panel := a.panelByIndex(idx)
	if panel == nil {
		return idx, ""
	}
	return idx, panel.scopeHit(x)
}

// scopeLabelAt reports whether the mouse is on a scope label.
func (a *App) scopeLabelAt(m tea.Mouse) bool {
	_, hit := a.scopeHitAt(m)
	return hit != ""
}

// replaceNavigator installs nav for a panel and closes the old one, which
// stops the background work of its folders.
func (a *App) replaceNavigator(idx int, nav *navui.Navigator) {
	old := a.navigatorFor(idx)
	if idx == 0 {
		a.leftNav = nav
	} else {
		a.rightNav = nav
	}
	if old != nil && old != nav {
		old.Close()
	}
}

// syncActivity pauses background refreshes while a dialog or menu is open.
func (a *App) syncActivity() {
	if a.activity == nil || a.modalManager == nil {
		return
	}
	open := a.modalManager.IsModalVisible()
	if open == a.activity.Paused() {
		return
	}
	if open {
		a.activity.Pause()
	} else {
		a.activity.Resume()
	}
}
