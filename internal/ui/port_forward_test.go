package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/portforward"
)

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}

func panelWith(t *testing.T, folder models.Folder, id string) *Panel {
	t.Helper()
	ctx := t.Context()
	p := NewPanel("Test")
	p.UseFolder(ctx, true)
	if model, _ := p.Update(tea.WindowSizeMsg{Width: 80, Height: 24}); model != nil {
		p = model.(*Panel)
	}
	p.SetFolder(ctx, folder, false)
	p.Init()
	p.RefreshFolder(ctx)
	p.SelectByRowID(ctx, id)
	if item, ok := p.SelectedNavItem(ctx); !ok || item == nil {
		t.Fatalf("row %q not selected", id)
	}
	return p
}

func TestPortForwardRowStopsWithF8(t *testing.T) {
	ctx := t.Context()
	reg := portforward.NewRegistry(ctx)
	defer reg.StopAll(time.Second)
	runner := portforward.RunnerFunc(func(ctx context.Context, _ portforward.Spec, ready func(int32)) error {
		ready(8000)
		<-ctx.Done()
		return nil
	})
	spec := portforward.Spec{Context: "dev", Namespace: "ns", Kind: "pod", Name: "web-0", Pod: "web-0", Address: "127.0.0.1", RemotePort: 80}
	if _, err := reg.Start(ctx, spec, runner); err != nil {
		t.Fatalf("Start: %v", err)
	}
	folder := models.NewPortForwardsFolder(models.Deps{PortForwards: reg}, reg, []string{models.PortForwardsPathSegment})
	fwd := reg.List()[0]
	p := panelWith(t, folder, fwd.ID)

	app := &App{ctx: ctx, width: 100, height: 30, modalManager: NewModalManager(nil),
		leftPanel: p, rightPanel: NewPanel("r"), leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}
	p.SetActionHandlers(app.panelActionHandlers())

	// F8 is offered on a forward row although it is no Kubernetes object.
	if caps := p.Capabilities(ctx); !caps.CanDelete {
		t.Fatal("F8 not enabled on a port forward row")
	}
	p.InvokeAction(PanelActionDelete)
	if !app.modalManager.IsModalVisible() || app.pendingConfirm == nil || app.pendingConfirm.row == nil {
		t.Fatal("F8 did not ask for confirmation")
	}
	if c := app.pendingConfirm.row.Confirmation(); c.Title != "Stop port forward?" {
		t.Fatalf("confirmation title = %q", c.Title)
	}
	if reg.Len() != 1 {
		t.Fatal("the forward stopped before the user confirmed")
	}
	app.handleConfirmResult(confirmResultMsg{ok: true})
	if reg.Len() != 0 {
		t.Fatalf("forward still listed after confirming stop")
	}
}

func TestPortForwardKeyNeedsATarget(t *testing.T) {
	ctx := t.Context()
	app := &App{ctx: ctx, width: 100, height: 30, modalManager: NewModalManager(nil),
		rightPanel: NewPanel("r"), leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}

	deploy := models.NewObjectRow("d", []string{"web"}, nil, gvr("apps", "v1", "deployments"), "ns", "web", nil)
	app.leftPanel = panelWith(t, &mockFolder{items: []models.Item{deploy}}, "d")
	cmd := app.portForwardForPanel(app.leftPanel)
	if cmd == nil {
		t.Fatal("no feedback for a row that cannot forward")
	}
	if msg, ok := cmd().(showToastMsg); !ok || !strings.Contains(msg.text, "pods, containers and services") {
		t.Fatalf("message = %#v, want a toast naming what can forward", msg)
	}

	// A pod row in a folder without dependencies: preparation runs in the
	// command and reports that forwarding is unavailable there.
	pod := models.NewObjectRow("p", []string{"web-0"}, nil, gvr("", "v1", "pods"), "ns", "web-0", nil)
	app.leftPanel = panelWith(t, &mockFolder{items: []models.Item{pod}}, "p")
	cmd = app.portForwardForPanel(app.leftPanel)
	if cmd == nil {
		t.Fatal("M-p on a pod row returned no command")
	}
	msg, ok := cmd().(portForwardPlanMsg)
	if !ok || msg.err == nil {
		t.Fatalf("message = %#v, want a plan error", msg)
	}
	if !passesModal(portForwardStartedMsg{}) || !passesModal(portForwardInputMsg{}) || !passesModal(portForwardPlanMsg{}) {
		t.Fatal("port forward messages must reach the app behind a dialog")
	}
}
