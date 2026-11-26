package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
)

// netpolKey completes C-x n: explain the NetworkPolicy decision from the
// pod under the active panel's cursor to the pod under the other's.
const netpolKey = "n"

// netpolLoadedMsg carries the evaluated pair back to the panel that asked.
type netpolLoadedMsg struct {
	panel  int
	origin models.Folder
	selID  string
	folder *models.NetworkPolicyPairFolder
}

// explainNetworkPolicy reads both pods and their namespaces' policies in a
// command and opens the result in the active panel. The pods are fixed by
// name and UID when the key is pressed, so moving a cursor afterwards does
// not change the result.
func (a *App) explainNetworkPolicy() tea.Cmd {
	src, dst := a.activePanelRef(), a.panelByIndex(1-a.activePanel)
	if src == nil || dst == nil || a.showTerminal || src.Mode() != PanelModeList || dst.Mode() != PanelModeList {
		return a.ShowToast(models.ErrNotPodPair.Error(), 4*time.Second)
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	srcItem, okSrc := src.SelectedNavItem(ctx)
	dstItem, okDst := dst.SelectedNavItem(ctx)
	srcRef, okSrcPod := models.PodRefOf(srcItem)
	dstRef, okDstPod := models.PodRefOf(dstItem)
	if !okSrc || !okDst || !okSrcPod || !okDstPod {
		return a.ShowToast(models.ErrNotPodPair.Error(), 4*time.Second)
	}
	// Traffic between two pods is a question about one cluster: the
	// policies of one cluster say nothing about a pod of another.
	if from, to := a.ownConn(a.activePanel), a.ownConn(1-a.activePanel); from.clusterKey() != to.clusterKey() {
		return a.ShowToast(fmt.Sprintf("NetworkPolicy check needs both pods in one cluster: the panels show %s and %s", from.name(), to.name()), 4*time.Second)
	}
	origin := src.Folder()
	deps, ok := a.explainDeps(origin)
	if !ok {
		return a.ShowToast("NetworkPolicy check needs a cluster connection", 3*time.Second)
	}
	msg := netpolLoadedMsg{panel: a.activePanel, origin: origin, selID: src.currentSelectionID(ctx)}
	root := a.ctx
	return a.withBusy("Reading NetworkPolicies", 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, requestTimeout)
		defer cancel()
		msg.folder = models.LoadNetworkPolicyPair(ctx, deps, srcRef, dstRef)
		return msg
	})
}

// handleNetpolLoaded opens the result in the panel the request came from,
// unless the user has left that folder meanwhile.
func (a *App) handleNetpolLoaded(msg netpolLoadedMsg) tea.Cmd {
	panel := a.panelByIndex(msg.panel)
	if panel == nil || msg.folder == nil {
		return nil
	}
	if panel.Folder() != msg.origin {
		msg.folder.Close()
		return a.ShowInfo("NetworkPolicies read after the panel moved; press C-x n again", 3*time.Second)
	}
	if a.activePanel != msg.panel {
		a.setActivePanel(msg.panel, "network policy loaded")
	}
	a.handleFolderNav(false, msg.selID, msg.folder)
	a.invalidateView("network policy opened")
	a.invalidateFunctionBar("network policy opened")
	return nil
}
