package ui

import (
	"context"
	"testing"

	"github.com/stokaro/kc/internal/models"
)

// The gates below came with features that landed next to read-only mode:
// port forwards (#91), node shells (#96) and cluster actions (#80).

func TestReadOnlyRefusesPortForward(t *testing.T) {
	for _, writable := range []bool{false, true} {
		app, _ := readOnlyTestApp(t, writable)
		pod := models.NewObjectRow("p", []string{"web-0"}, nil, gvr("", "v1", "pods"), "ns", "web-0", nil)
		app.leftPanel = panelWith(t, &mockFolder{items: []models.Item{pod}}, "p")
		cmd := app.portForwardForPanel(app.leftPanel)
		if !writable {
			wantRefusal(t, cmd, "Port forward")
			continue
		}
		if cmd == nil {
			t.Fatal("writable: M-p returned no command")
		}
		if _, ok := cmd().(portForwardPlanMsg); !ok {
			t.Fatal("writable: M-p did not prepare the forward")
		}
	}
}

func TestReadOnlyRefusesNodeShellRequest(t *testing.T) {
	app, _ := readOnlyTestApp(t, false)
	wantRefusal(t, app.startNodeShell(models.NodeShellRequest{Context: "eu-prod", Node: "n1", Namespace: "default"}), "Node shell")
	if app.nodeShellStarting {
		t.Fatal("read-only mode started a node shell")
	}
}

type stubClusterAction struct{ ran *bool }

func (s stubClusterAction) Confirmation() models.Confirmation {
	return models.Confirmation{Title: "Run now?", Action: "Run"}
}

func (s stubClusterAction) Run(context.Context) (models.ActionResult, error) {
	*s.ran = true
	return models.ActionResult{}, nil
}

func TestReadOnlyRefusesClusterActions(t *testing.T) {
	t.Run("dialog is not opened", func(t *testing.T) {
		app, _ := readOnlyTestApp(t, false)
		ran := false
		wantRefusal(t, app.showClusterActionConfirm(stubClusterAction{ran: &ran}, 0), "Run now")
		if app.pendingConfirm != nil || app.modalManager.IsModalVisible() {
			t.Fatal("read-only mode opened the confirmation dialog")
		}
	})
	t.Run("approval after the context became read-only", func(t *testing.T) {
		app, _ := readOnlyTestApp(t, true)
		ran := false
		if cmd := app.showClusterActionConfirm(stubClusterAction{ran: &ran}, 0); isRefusal(cmd) || app.pendingConfirm == nil {
			t.Fatal("a writable context must open the dialog")
		}
		app.readOnlyOverride = ReadOnlyForced
		wantRefusal(t, app.handleConfirmResult(confirmResultMsg{ok: true}), "Run now")
		if ran {
			t.Fatal("the action ran in read-only mode")
		}
	})
}
