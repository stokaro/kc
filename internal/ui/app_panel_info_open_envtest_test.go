package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"

	models "github.com/stokaro/kc/internal/models"
)

// TestInfoTopPodOpensInTheOtherPanelEnvtest opens a top pod of the Info
// panel as its Enter does (ADR 0176): the other panel lists pods of the
// pod's namespace with that pod selected, while the Info panel keeps its
// mode and the focus. A double click in Info mode does not enter the row
// of the list that the mode hides.
func TestInfoTopPodOpensInTheOtherPanelEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	const ns = "info-open"
	t.Cleanup(createNamespace(t, cl, ns))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pod := &corev1.Pod{Namespace: ns, Name: "web-0", Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}}}
	if err := cl.GetClient().Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns)
	_ = app.setPanelMode(t.Context(), app.rightPanel, PanelModeInfo, "test")
	if app.rightPanel.Mode() != PanelModeInfo {
		t.Fatalf("right panel mode = %v, want Info", app.rightPanel.Mode())
	}
	_, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app.activePanel = 1

	// Two clicks on the Info panel: the hidden list stays where it was.
	before := app.rightPanel.GetCurrentPath()
	click := tea.MouseClickMsg{Button: tea.MouseLeft, X: 100, Y: 8}
	for range 2 {
		_, _ = app.Update(click)
	}
	if got := app.rightPanel.GetCurrentPath(); got != before {
		t.Fatalf("a double click in Info mode moved the hidden list from %q to %q", before, got)
	}

	link := models.NewObjectRefLink(models.InfoConsumer{Namespace: ns, Name: "web-0"}.Ref())
	_, cmd := app.Update(models.OpenObjectRequest{Link: link})
	if toasts := runLinkCmds(t, app, cmd); len(toasts) > 0 {
		t.Fatalf("messages = %q, want none", toasts)
	}
	if got := app.leftPanel.GetCurrentPath(); got != "/pods" {
		t.Fatalf("left panel path = %q, want /pods", got)
	}
	if app.panelScope[0] != ns {
		t.Fatalf("left panel scope = %q, want %q", app.panelScope[0], ns)
	}
	sctx, scancel := context.WithTimeout(t.Context(), time.Second)
	sel := app.leftPanel.currentSelectionID(sctx)
	scancel()
	if sel != "web-0" {
		t.Fatalf("left panel selection = %q, want web-0", sel)
	}
	if app.activePanel != 1 || app.rightPanel.Mode() != PanelModeInfo {
		t.Fatalf("active panel %d, right mode %v; want the Info panel to keep the focus and its mode", app.activePanel, app.rightPanel.Mode())
	}
}
