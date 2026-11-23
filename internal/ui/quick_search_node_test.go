package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/nodeaction"
	"github.com/stokaro/kc/internal/table"
)

// searchNodesFolder is a node list whose rows can arrive late, as a live
// list's do. It sits behind a root folder, so it has the "/.." row a real
// one has.
type searchNodesFolder struct{ namesFolder }

func (f *searchNodesFolder) NodeActions() (models.NodeActionTarget, bool) {
	return models.NodeActionTarget{Context: "kind-demo"}, true
}

// M-n right after a C-s quick search acts on the node the search found,
// not on the row the cursor was on before: also when the list refreshes
// between the keys, and when the rows arrive while the search is typed.
func TestNodeActionsAfterQuickSearch(t *testing.T) {
	t.Parallel()
	var rows []table.Row
	for _, n := range []string{"demo-control-plane", "demo-worker", "demo-worker2"} {
		node := models.NewObjectRow(n, []string{n}, []string{"nodes", n}, nodeaction.NodesGVR, "", n, models.WhiteStyle())
		node.SetResourceVerbs([]string{"get", "list", "patch"})
		rows = append(rows, node)
	}
	for _, tc := range []struct {
		name    string
		refresh bool // a refresh between the search and M-n
		late    bool // the rows arrive after the search text was typed
	}{
		{name: "loaded"},
		{name: "refresh between", refresh: true},
		{name: "rows arrive while searching", late: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nodes := &searchNodesFolder{namesFolder{rows: rows}}
			if tc.late {
				nodes.rows = nil
			}
			root := modeltesting.NewStaticFolder("/", nil, []table.Row{
				modeltesting.NewEnterableItem("nodes", []string{"/nodes"}, []string{"nodes"}, func() (models.Folder, error) { return nodes, nil }, models.WhiteStyle()),
			})
			a := NewApp()
			t.Cleanup(a.cancel)
			a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			a.leftNav = navui.NewNavigator(root)
			a.rightNav = navui.NewNavigator(modeltesting.NewStaticFolder("/", nil, nil))
			a.leftPanel.UseFolder(t.Context(), true)
			a.rightPanel.UseFolder(t.Context(), true)
			a.syncPanelWithNavigator(0)
			a.syncPanelWithNavigator(1)
			a.activePanel = 0
			enter(t, a, "nodes")
			// The cursor starts on "/..", which is not a node.
			a.leftPanel.SelectByRowID(t.Context(), "__back__")

			a.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			for _, r := range "demo-w" {
				a.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			if tc.late {
				nodes.rows = rows
				a.Update(FolderDirtyMsg{PanelIdx: 0})
			}
			if tc.refresh {
				a.leftPanel.RefreshFolder(t.Context())
			}
			if sel := a.leftPanel.CurrentSelection(t.Context()); sel.ID != "demo-worker" {
				t.Errorf("the panel's selection is %q after the search, want demo-worker", sel.ID)
			}
			_, cmd := a.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModAlt})
			if m := a.modalManager.GetActiveModal(); m == nil || !m.IsVisible() {
				var toast any
				if cmd != nil {
					toast = cmd()
				}
				t.Fatalf("M-n did not open the node menu: %#v", toast)
			}
			if target, _, _ := a.nodeTargetFor(a.leftPanel); len(target.nodes) != 1 || target.nodes[0] != "demo-worker" {
				t.Fatalf("node target = %v, want [demo-worker]", target.nodes)
			}
		})
	}
}
