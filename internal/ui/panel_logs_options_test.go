package ui

import (
	"io"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// openedLogsWidget returns a Logs widget whose stream of item opened with
// the containers app and sidecar.
func openedLogsWidget(t *testing.T, item models.Item) *logswidget.Widget {
	t.Helper()
	src := func(req logswidget.Request) logswidget.Open {
		return func() (logswidget.Stream, error) {
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			spec := req.Spec
			if spec.Container == "" && !req.AllContainers {
				spec.Container = "app"
			}
			return logswidget.Stream{Spec: spec, Containers: []string{"app", "sidecar"}, Body: pr, Cancel: func() {}}, nil
		}
	}
	w := logswidget.New(panelcontent.WidgetDeps{}, logswidget.Options{
		Source: src,
		After: func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			return func() tea.Msg { return fn(time.Time{}) }
		},
	})
	debounced := w.OnSelectionChanged(t.Context(), panelcontent.Selection{Item: item})()
	openCmd, _ := w.Update(t.Context(), debounced)
	w.Update(t.Context(), openCmd())
	return w
}

func choiceValues(choices []ViewOptionsChoice) []string {
	var out []string
	for _, c := range choices {
		out = append(out, c.Value+"="+c.Label)
	}
	return out
}

func TestPanelLogsOptionsContainerChoices(t *testing.T) {
	t.Parallel()
	w := openedLogsWidget(t, testPodItem())
	got := choiceValues((panelLogsOptions{w: w}).LogContainerChoices())
	want := []string{"=Default", "app=app", "sidecar=sidecar", "*=All containers"}
	if !slices.Equal(got, want) {
		t.Fatalf("choices = %v, want %v", got, want)
	}

	row := models.NewContainerItem("sidecar", []string{"/sidecar"}, []string{"namespaces", "ns", "pods", "web", "containers", "sidecar"}, nil, nil, nil)
	row.SetPod("ns", "web")
	w = openedLogsWidget(t, row)
	if got := (panelLogsOptions{w: w}).LogContainerChoices()[0].Label; got != "Default (sidecar)" {
		t.Fatalf("default label for a container row = %q", got)
	}

	idle := logswidget.New(panelcontent.WidgetDeps{}, logswidget.Options{})
	if got := (panelLogsOptions{w: idle}).LogContainerChoices(); got != nil {
		t.Fatalf("choices without a pod = %v, want none", got)
	}
}

func TestPanelLogsOptionsRoundTrip(t *testing.T) {
	t.Parallel()
	w := openedLogsWidget(t, testPodItem())
	o := panelLogsOptions{w: w}
	if got := o.LogOptions(); got != (LogOptions{TailLines: models.DefaultLogsTailLines}) {
		t.Fatalf("LogOptions() = %+v, want the default tail", got)
	}
	want := LogOptions{TailLines: -1, SinceSeconds: 600, Timestamps: true, Previous: true, Container: logswidget.ContainerAll}
	if cmd := o.SetLogOptions(want); cmd == nil {
		t.Fatal("changed options must restart the stream")
	}
	if got := o.LogOptions(); got != want {
		t.Fatalf("LogOptions() = %+v, want %+v", got, want)
	}
	if !w.AllContainers() {
		t.Fatal("the widget does not stream all containers")
	}
}

// logsPanelApp returns an app whose right panel is in Logs mode and follows
// a pod selected in the left panel. Without a cluster the open fails; the
// target stays.
func logsPanelApp(t *testing.T) (*App, *logswidget.Widget) {
	t.Helper()
	app := NewApp()
	app.width, app.height = 120, 40
	drainPanelLogs(t, app, app.setPanelMode(t.Context(), app.rightPanel, PanelModeLogs, "test"), 0)
	_, cmd := app.Update(PanelSelectionChangedMsg{
		Panel:     app.leftPanel,
		Selection: panelcontent.Selection{ID: "web", Item: testPodItem()},
	})
	drainPanelLogs(t, app, cmd, 0)
	w := app.rightPanel.logsWidget()
	if w == nil {
		t.Fatal("right panel has no Logs widget")
	}
	return app, w
}

func viewOptionsContent(t *testing.T, app *App) *ViewOptionsModel {
	t.Helper()
	modal := app.modalManager.modals["view_options"]
	if modal == nil || !modal.visible {
		t.Fatal("the View Options dialog is not open")
	}
	content, ok := modal.content.(*ViewOptionsModel)
	if !ok {
		t.Fatalf("dialog content is %T", modal.content)
	}
	return content
}

func hasOption(m *ViewOptionsModel, opt viewOptionKind) bool {
	return slices.ContainsFunc(m.entries, func(e viewOptionEntry) bool {
		return e.kind == viewOptionEntryOption && e.option == opt
	})
}

func TestF2InLogsModeChangesStreamOptions(t *testing.T) {
	app, w := logsPanelApp(t)
	app.showViewOptionsModalForPanel(app.rightPanel)
	m := viewOptionsContent(t, app)
	for _, opt := range []viewOptionKind{viewOptionPanelMode, viewOptionViewerWrapMode, viewOptionLogsContainer, viewOptionLogsTail, viewOptionLogsPrevious} {
		if !hasOption(m, opt) {
			t.Fatalf("option %d missing from the Logs mode dialog", opt)
		}
	}
	if l, v := m.optionLabelAndValue(viewOptionLogsContainer); l != "Container" || v != "Default" {
		t.Fatalf("container row shows %q: %q", l, v)
	}

	focusOption(t, m, viewOptionLogsContainer)
	m.adjustCurrent(1) // Default -> All containers (no container list without a cluster)
	focusOption(t, m, viewOptionLogsTimestamps)
	m.adjustCurrent(0)
	_, cmd := app.Update(m.commit(true, false))
	drainPanelLogs(t, app, cmd, 0)

	s := w.Settings()
	if s.Container != logswidget.ContainerAll || !s.Timestamps {
		t.Fatalf("Settings() = %+v, want all containers with timestamps", s)
	}
	if !w.AllContainers() {
		t.Fatal("the stream was not restarted for all containers")
	}
	if app.rightPanel.Mode() != PanelModeLogs {
		t.Fatalf("panel mode = %v, want Logs", app.rightPanel.Mode())
	}
	if app.modalManager.IsModalVisible() {
		t.Fatal("the dialog stays open after Enter")
	}
	if app.viewerOptionsTarget != nil {
		t.Fatal("the dialog target was not cleared")
	}
}

func TestF2InLogsModeSwitchesPanelMode(t *testing.T) {
	app, w := logsPanelApp(t)
	app.showViewOptionsModalForPanel(app.rightPanel)
	m := viewOptionsContent(t, app)
	m.panelModeIndex = slices.Index(m.panelModes, PanelModeList)
	if m.panelModeIndex < 0 {
		t.Fatal("List is not among the panel modes")
	}
	_, cmd := app.Update(m.commit(true, false))
	drainPanelLogs(t, app, cmd, 0)
	if app.rightPanel.Mode() != PanelModeList {
		t.Fatalf("panel mode = %v, want List", app.rightPanel.Mode())
	}
	if w.Streaming() {
		t.Fatal("the Logs stream runs after the panel left Logs mode")
	}
}

func TestF2KeyInLogsModeOpensOptions(t *testing.T) {
	app, _ := logsPanelApp(t)
	app.activePanel = 1
	ctx := t.Context()
	app.rightPanel.invokeActionIfAllowed(ctx, PanelActionMenu)
	m := viewOptionsContent(t, app)
	if !hasOption(m, viewOptionLogsTail) {
		t.Fatal("F2 in Logs mode did not open the log options")
	}

	if caps := maskCapabilitiesForMode(PanelModeLogs, false, PanelCapabilities{HasOptions: true, HasContextMenu: true}); caps.HasContextMenu || !caps.HasOptions {
		t.Fatalf("Logs mode capabilities = %+v, want options without the user menu", caps)
	}
	keys := panelFunctionKeys(functionBarState{PanelMode: PanelModeLogs, Capabilities: PanelCapabilities{HasOptions: true}})
	if keys[1].Label != "Options" || !keys[1].Enabled {
		t.Fatalf("F2 in Logs mode = %+v, want enabled Options", keys[1])
	}
	keys = panelFunctionKeys(functionBarState{PanelMode: PanelModeList, Capabilities: PanelCapabilities{HasContextMenu: true}})
	if keys[1].Label != "Menu" {
		t.Fatalf("F2 in List mode = %+v, want Menu", keys[1])
	}
}

func TestF2InListModeKeepsPanelDialog(t *testing.T) {
	app := NewApp()
	app.width, app.height = 120, 40
	app.showViewOptionsModalForPanel(app.rightPanel)
	m := viewOptionsContent(t, app)
	if hasOption(m, viewOptionLogsTail) || hasOption(m, viewOptionViewerTheme) {
		t.Fatal("the List mode dialog shows viewer or log options")
	}
	if app.viewerOptionsTarget != nil {
		t.Fatal("the List mode dialog set a viewer target")
	}
}

func TestViewOptionsContainerRow(t *testing.T) {
	t.Parallel()
	m := newLogsOptionsModel(ViewOptionsLogsConfig{TailLines: 200, Container: "keep"})
	if hasOption(m, viewOptionLogsContainer) {
		t.Fatal("the Container row is shown without choices")
	}
	if got := m.commit(true, false).Logs.Container; got != "keep" {
		t.Fatalf("Container = %q, want the configured value kept", got)
	}

	m = newLogsOptionsModel(ViewOptionsLogsConfig{
		TailLines:  200,
		Container:  "sidecar",
		Containers: []ViewOptionsChoice{{Value: "", Label: "Default"}, {Value: "sidecar", Label: "sidecar"}, {Value: "*", Label: "All containers"}},
	})
	if _, v := m.optionLabelAndValue(viewOptionLogsContainer); v != "sidecar" {
		t.Fatalf("Container row shows %q, want sidecar", v)
	}
	focusOption(t, m, viewOptionLogsContainer)
	m.adjustCurrent(1)
	if got := m.commit(true, false).Logs.Container; got != "*" {
		t.Fatalf("Container = %q after next, want *", got)
	}
	m.adjustCurrent(1)
	if got := m.commit(true, false).Logs.Container; got != "" {
		t.Fatalf("Container = %q after wrapping, want the default", got)
	}
}
