package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/help"
	models "github.com/stokaro/kc/internal/models"
)

// pairFolder is a result read without a cluster: every source is
// unreadable, which is enough for the navigation tests.
func pairFolder(t *testing.T) *models.NetworkPolicyPairFolder {
	t.Helper()
	f := models.LoadNetworkPolicyPair(t.Context(), models.Deps{Ctx: t.Context()},
		models.PodRef{Namespace: "default", Name: "a", UID: "a"}, models.PodRef{Namespace: "default", Name: "b", UID: "b"})
	t.Cleanup(f.Close)
	return f
}

// C-x n on something other than two pods says so instead of reading.
func TestNetpolKeyNeedsTwoPods(t *testing.T) {
	a, _ := explainApp(t)
	var cmd tea.Cmd
	for _, k := range []tea.KeyPressMsg{{Code: 'x', Mod: tea.ModCtrl}, {Code: 'n', Text: "n"}} {
		_, cmd = a.Update(k)
	}
	if cmd == nil {
		t.Fatal("C-x n not handled")
	}
	msg, ok := cmd().(showToastMsg)
	if !ok || msg.text != models.ErrNotPodPair.Error() {
		t.Errorf("msg = %#v, want the not-a-pod-pair toast", msg)
	}
}

func TestNetpolLoadedAfterThePanelMoved(t *testing.T) {
	a, _ := explainApp(t)
	folder := pairFolder(t)
	cmd := a.handleNetpolLoaded(netpolLoadedMsg{panel: 0, origin: mkFolder("elsewhere"), folder: folder})
	if a.leftPanel.Folder() == models.Folder(folder) {
		t.Fatal("pushed onto a folder the request did not come from")
	}
	if cmd == nil {
		t.Fatal("no message for the dropped result")
	}
	if msg, ok := cmd().(showToastMsg); !ok || !msg.info {
		t.Errorf("msg = %#v, want an info toast", msg)
	}
}

func TestNetpolLoadedPushesAndBackRestores(t *testing.T) {
	a, origin := explainApp(t)
	folder := pairFolder(t)
	a.handleNetpolLoaded(netpolLoadedMsg{panel: 0, origin: origin, selID: "default/web", folder: folder})
	if a.leftPanel.Folder() != models.Folder(folder) {
		t.Fatal("result not opened in the active panel")
	}
	if page := folderHelpPage(folder); page != help.NetworkPolicy {
		t.Errorf("F1 page = %q", page)
	}
	a.handleFolderNav(true, "", nil)
	if a.leftPanel.Folder() != origin {
		t.Fatal("back did not return to the list")
	}
	if id := a.leftPanel.currentSelectionID(t.Context()); id != "default/web" {
		t.Errorf("selection after back = %q", id)
	}
}
