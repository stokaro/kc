package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	models "github.com/sttts/kc/internal/models"
	panelcontent "github.com/sttts/kc/internal/ui/panelcontent"
	logswidget "github.com/sttts/kc/internal/ui/panelcontent/logs"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestPanelModeOrderHasLogsAfterManifest(t *testing.T) {
	if got := NextPanelMode(PanelModeManifest); got != PanelModeLogs {
		t.Fatalf("NextPanelMode(Manifest) = %v, want Logs", got)
	}
	if got := NextPanelMode(PanelModeLogs); got != PanelModeInfo {
		t.Fatalf("NextPanelMode(Logs) = %v, want Info", got)
	}
	if got := modeLabel(PanelModeLogs); got != "Logs" {
		t.Fatalf("modeLabel(Logs) = %q", got)
	}
	app := NewApp()
	found := false
	for _, m := range app.rightPanel.AvailableModes() {
		found = found || m == PanelModeLogs
	}
	if !found {
		t.Fatal("Logs mode is not registered on the panels")
	}
	if !passesModal(logsMsgOf(t, app)) {
		t.Fatal("Logs widget messages must pass an open dialog")
	}
}

// logsMsgOf returns a Logs widget message by following a pod in a fresh
// Logs widget.
func logsMsgOf(t *testing.T, app *App) tea.Msg {
	t.Helper()
	w := logswidget.New(panelcontent.WidgetDeps{}, logswidget.Options{
		Source: app.panelLogsSource(),
		After: func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			return func() tea.Msg { return fn(time.Time{}) }
		},
	})
	return w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: testPodItem()})()
}

func testPodItem() models.Item {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	return models.NewObjectRow("web", []string{"web"}, []string{"namespaces", "ns", "pods", "web"}, gvr, "ns", "web", nil)
}

// TestLogsPanelFollowsOtherPanel drives the app: a selection in the left
// panel reaches the right panel's Logs widget, and the widget's background
// messages come back through App.Update. Without a cluster the open fails,
// which the panel shows.
func TestLogsPanelFollowsOtherPanel(t *testing.T) {
	app := NewApp()
	app.width, app.height = 100, 30
	if cmd := app.setPanelMode(t.Context(), app.rightPanel, PanelModeLogs, "test"); cmd != nil {
		drainPanelLogs(t, app, cmd, 0)
	}
	w := app.rightPanel.logsWidget()
	if w == nil {
		t.Fatal("right panel has no Logs widget")
	}

	_, cmd := app.Update(PanelSelectionChangedMsg{
		Panel:     app.leftPanel,
		Selection: panelcontent.Selection{ID: "web", Item: testPodItem()},
	})
	if spec := w.Spec(); spec.Namespace != "ns" || spec.Pod != "web" {
		t.Fatalf("Logs widget follows %+v, want ns/web", spec)
	}
	drainPanelLogs(t, app, cmd, 0)
	if w.Streaming() {
		t.Fatal("the stream should have failed without a cluster")
	}
	if got := w.View(t.Context(), panelcontent.Frame{Size: panelcontent.Size{Width: 80, Height: 5}}).Content; !strings.Contains(got, "no active cluster") {
		t.Fatalf("expected the open error in the panel:\n%s", got)
	}
}

// drainPanelLogs runs cmd and feeds the Logs widget messages it yields back into the
// app until none are left.
func drainPanelLogs(t *testing.T, app *App, cmd tea.Cmd, depth int) {
	t.Helper()
	if cmd == nil {
		return
	}
	if depth > 10 {
		t.Fatal("Logs messages do not settle")
	}
	// Commands that are not the widget's may wait on app channels; give
	// each a bounded time.
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var out tea.Msg
	select {
	case out = <-done:
	case <-time.After(2 * time.Second):
		return
	}
	switch msg := out.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			drainPanelLogs(t, app, c, depth+1)
		}
	case panelcontent.BackgroundMsg:
		_, next := app.Update(msg)
		drainPanelLogs(t, app, next, depth+1)
	}
}
