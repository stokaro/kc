package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	navui "github.com/stokaro/kc/internal/navigation"
)

// linkToastTTL is how long a message about a linked object stays.
const linkToastTTL = 6 * time.Second

// openLinkedObject shows a linked object (for example an object of a Helm
// release manifest) in the other panel: that panel moves to the object's
// namespace scope and type list, with the object selected. Focus stays on
// the panel with the link, so the user can walk through the links.
func (a *App) openLinkedObject(link models.ObjectLink) tea.Cmd {
	ref, missing := link.LinkTarget()
	if ref.GVR.Empty() {
		if missing == "" {
			missing = "the object's type is not known to kc"
		}
		return a.ShowToast(missing, linkToastTTL)
	}
	idx := 1 - a.activePanel
	if ref.Namespaced && a.panelScope[idx] != ref.Namespace {
		a.panelScope[idx] = ref.Namespace
		a.replaceNavigator(idx, a.newScopedNavigator(idx))
	}
	nav := a.navigatorFor(idx)
	panel := a.panelByIndex(idx)
	if nav == nil || panel == nil {
		return nil
	}
	for nav.HasBack() {
		nav.Back()
	}
	ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
	defer cancel()
	rowID := resourceRowID(ref.Namespace, ref.GVR, ref.Namespaced)
	if _, err := navui.GoTo(ctx, nav, []navui.GoToStep{{SelectionID: rowID, Enter: true}}); err != nil {
		// The root hides the type (for example while empty types are
		// filtered); open its list directly.
		folder := a.linkedTypeFolder(panel, ref)
		if folder == nil {
			return a.ShowToast("cannot list "+ref.GVR.Resource+": "+err.Error(), linkToastTTL)
		}
		nav.SetSelectionID(rowID)
		nav.Push(folder)
	}
	a.syncPanelWithNavigator(idx)
	a.syncPanelScopeLabels()
	cmds := []tea.Cmd{a.selectLinkedRow(linkSelectMsg{panelIdx: idx, path: panel.GetCurrentPath(), id: ref.Name})}
	if missing != "" {
		cmds = append(cmds, a.ShowToast(missing, linkToastTTL))
	}
	return tea.Batch(cmds...)
}

// linkSelectMsg retries selecting a linked object in its list.
type linkSelectMsg struct {
	panelIdx int
	path     string
	id       string
	attempt  int
}

// selectLinkedRow selects the linked object. The list may still be loading
// in the background, so it retries like the startup selection, until the
// row is selected, the attempts run out, or the panel shows another folder.
func (a *App) selectLinkedRow(msg linkSelectMsg) tea.Cmd {
	panel := a.panelByIndex(msg.panelIdx)
	if panel == nil || panel.GetCurrentPath() != msg.path {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	panel.SelectByRowID(ctx, msg.id)
	selected := panel.currentSelectionID(ctx) == msg.id
	cancel()
	if selected || msg.attempt+1 >= startupSelectAttempts {
		return nil
	}
	next := msg
	next.attempt++
	return tea.Tick(startupSelectRetry, func(time.Time) tea.Msg { return next })
}

// linkedTypeFolder builds the object list of ref's type as the root would.
func (a *App) linkedTypeFolder(panel *Panel, ref models.ObjectRef) models.Folder {
	if a.cl == nil {
		return nil
	}
	infos, err := a.cl.GetResourceInfos()
	if err != nil {
		return nil
	}
	for _, info := range infos {
		if info.Resource != ref.GVR.Resource || info.GVK.Group != ref.GVR.Group || info.GVK.Version != ref.GVR.Version {
			continue
		}
		cfg := a.ensurePanelConfig(panel)
		deps := a.makeDeps(a.cl, cfg, a.currentClusterKey())
		verbs := append([]string(nil), info.Verbs...)
		path := []string{info.Resource}
		if ref.Namespaced {
			return models.NewNamespacedObjectsFolder(deps, ref.GVR, ref.Namespace, path, verbs)
		}
		return models.NewClusterObjectsFolder(deps, ref.GVR, path, verbs)
	}
	return nil
}
