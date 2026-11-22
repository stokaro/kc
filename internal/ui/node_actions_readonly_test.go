package ui

import (
	"testing"

	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/internal/nodeaction"
)

// nodeListFolder is a node list that offers node actions.
type nodeListFolder struct{ mockFolder }

func (f *nodeListFolder) NodeActions() (models.NodeActionTarget, bool) {
	return models.NodeActionTarget{Context: "eu-prod"}, true
}

// Read-only mode refuses M-n on a node list before the menu opens; a
// writable context opens it.
func TestReadOnlyRefusesNodeActions(t *testing.T) {
	for _, writable := range []bool{false, true} {
		app, _ := readOnlyTestApp(t, writable)
		node := models.NewObjectRow("n1", []string{"n1"}, nil, nodeaction.NodesGVR, "", "n1", nil)
		node.SetResourceVerbs([]string{"get", "list", "patch"})
		app.leftPanel = panelWith(t, &nodeListFolder{mockFolder{items: []models.Item{node}}}, "n1")
		app.activePanel = 0
		cmd, handled := app.openNodeActions()
		if !handled {
			t.Fatal("M-n on a node list must be handled")
		}
		if !writable {
			wantRefusal(t, cmd, "Node actions")
			if app.modalManager.IsModalVisible() {
				t.Fatal("read-only mode opened the node action menu")
			}
			continue
		}
		if isRefusal(cmd) || !app.modalManager.IsModalVisible() {
			t.Fatal("writable: M-n did not open the node action menu")
		}
	}
}
