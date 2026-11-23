package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/types"

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
	cmds := []tea.Cmd{a.selectLinkedRow(linkSelectMsg{panelIdx: idx, path: panel.GetCurrentPath(), ref: ref, reportMissing: missing == ""})}
	if missing != "" {
		cmds = append(cmds, a.ShowToast(missing, linkToastTTL))
	}
	return tea.Batch(cmds...)
}

// linkSelectMsg retries selecting a linked object in its list.
type linkSelectMsg struct {
	panelIdx int
	path     string
	ref      models.ObjectRef
	attempt  int
	// reportMissing says to tell the user when the object never shows up;
	// it is false when the link already said why it may be missing.
	reportMissing bool
}

// selectLinkedRow selects the linked object. The list may still be loading
// in the background, so it retries like the startup selection, until the
// row is selected, the attempts run out, or the panel shows another folder.
//
// A reference with a UID names one object instance. When the listed object
// of that name has another UID, the referenced object was deleted and the
// row is a replacement: the row is not selected, so the replacement is not
// shown as the object an event is about, and a message names both UIDs.
// Moving the cursor to the row is the explicit way to look at it.
func (a *App) selectLinkedRow(msg linkSelectMsg) tea.Cmd {
	panel := a.panelByIndex(msg.panelIdx)
	if panel == nil || panel.GetCurrentPath() != msg.path {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	ref := msg.ref
	last := msg.attempt+1 >= startupSelectAttempts
	if ref.UID != "" {
		uid, found := panel.listedObjectUID(ctx, ref.Name)
		switch {
		case !found && !last:
			return retryLinkSelect(msg)
		case found && uid == "":
			panel.SelectByRowID(ctx, ref.Name)
			return a.ShowToast(linkLabel(ref)+": kc cannot read the listed object's UID, so it may be a newer object with the same name", linkToastTTL)
		case found && uid != ref.UID:
			return a.ShowToast(linkReplacedMessage(ref, uid), linkToastTTL)
		}
	}
	panel.SelectByRowID(ctx, ref.Name)
	if panel.currentSelectionID(ctx) == ref.Name {
		return nil
	}
	if !last {
		return retryLinkSelect(msg)
	}
	if !msg.reportMissing {
		return nil
	}
	text := linkLabel(ref) + " is not in the list"
	if ref.UID != "" {
		text += "; the object may be deleted, or you may not be allowed to list it"
	}
	return a.ShowToast(text, linkToastTTL)
}

func retryLinkSelect(msg linkSelectMsg) tea.Cmd {
	next := msg
	next.attempt++
	return tea.Tick(startupSelectRetry, func(time.Time) tea.Msg { return next })
}

// linkLabel names a linked object for a message: resource, namespace and
// name.
func linkLabel(ref models.ObjectRef) string {
	name := ref.Name
	if ref.Namespace != "" {
		name = ref.Namespace + "/" + name
	}
	return ref.GVR.Resource + " " + name
}

// linkReplacedMessage says that the listed object is not the referenced
// instance.
func linkReplacedMessage(ref models.ObjectRef, listed types.UID) string {
	return fmt.Sprintf("%s was replaced: the reference is to UID %s, the listed object is UID %s; its row is not selected",
		linkLabel(ref), ref.UID, listed)
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
