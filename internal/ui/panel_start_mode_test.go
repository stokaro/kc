package ui

// White-box: the panels, the configuration store and the startup intent of
// the App are unexported, and App.Init also starts the embedded shell, so
// the tests call initPanels, the part of Init that sets the start modes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/pkg/appconfig"
)

// newStartModeApp returns a 100x30 App that reads its configuration from a
// temporary file holding file ("" for no file), with the panels started as
// Init starts them.
func newStartModeApp(t *testing.T, file string) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if file != "" {
		if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	app.cfgStore = appconfig.NewStore(path)
	_ = app.loadConfig()
	model, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = model.(*App)
	_ = app.initPanels()
	return app
}

func TestPanelStartModes(t *testing.T) {
	tests := []struct {
		name      string
		file      string
		wantLeft  PanelViewMode
		wantRight PanelViewMode
	}{
		{name: "no config file", wantLeft: PanelModeList, wantRight: PanelModeInfo},
		{name: "two lists", file: "layout:\n  rightPanelMode: list\n", wantLeft: PanelModeList, wantRight: PanelModeList},
		{name: "info on the left", file: "layout:\n  leftPanelMode: info\n  rightPanelMode: list\n", wantLeft: PanelModeInfo, wantRight: PanelModeList},
		{name: "describe on the right", file: "layout:\n  rightPanelMode: describe\n", wantLeft: PanelModeList, wantRight: PanelModeDescribe},
		{name: "manifest on the right", file: "layout:\n  rightPanelMode: manifest\n", wantLeft: PanelModeList, wantRight: PanelModeManifest},
		{name: "logs on the right", file: "layout:\n  rightPanelMode: logs\n", wantLeft: PanelModeList, wantRight: PanelModeLogs},
		// A file that does not load leaves the defaults in place.
		{name: "invalid mode", file: "layout:\n  rightPanelMode: tree\n", wantLeft: PanelModeList, wantRight: PanelModeInfo},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := newStartModeApp(t, tc.file)
			if got := app.leftPanel.Mode(); got != tc.wantLeft {
				t.Fatalf("left mode = %v, want %v", modeLabel(got), modeLabel(tc.wantLeft))
			}
			if got := app.rightPanel.Mode(); got != tc.wantRight {
				t.Fatalf("right mode = %v, want %v", modeLabel(got), modeLabel(tc.wantRight))
			}
			if app.activePanel != 0 {
				t.Fatalf("active panel = %d, want the left panel", app.activePanel)
			}
		})
	}
}

// TestInfoStartModeWithoutCluster is kc started with no kubeconfig context:
// the right panel shows the Info mode's hint, not an error, and Tab moves
// into it as before.
func TestInfoStartModeWithoutCluster(t *testing.T) {
	app := newStartModeApp(t, "")
	view := strings.Join(screenLines(app), "\n")
	if !strings.Contains(view, "Not connected to a cluster.") {
		t.Fatalf("view has no Info hint:\n%s", view)
	}
	if strings.Contains(strings.ToLower(view), "error") {
		t.Fatalf("view shows an error:\n%s", view)
	}
	model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = model.(*App)
	if app.activePanel != 1 || app.rightPanel.Mode() != PanelModeInfo {
		t.Fatalf("after Tab: active panel = %d, right mode = %v; want 1, Info", app.activePanel, modeLabel(app.rightPanel.Mode()))
	}
}

// TestInfoStartModeSwitchesWithMenu checks that the start mode is an
// ordinary mode: the panel menu switches it back to List.
func TestInfoStartModeSwitchesWithMenu(t *testing.T) {
	app := newStartModeApp(t, "")
	var run func() tea.Cmd
	for _, item := range app.panelMenuItems(1) {
		switch item.Label {
		case "&List":
			run = item.Run
		case "&Info":
			if !item.Checked {
				t.Fatal("the Info entry is not checked for the right panel")
			}
		}
	}
	if run == nil {
		t.Fatal("the right panel menu has no List item")
	}
	_ = run()
	if got := app.rightPanel.Mode(); got != PanelModeList {
		t.Fatalf("right mode after the menu = %v, want List", modeLabel(got))
	}
}

// TestStartTargetsKeepTheRightStartModeEnvtest runs the start targets
// after the panels got their start modes, as kc does: kc overview and
// kc get open in the left panel and leave the right panel in Info mode;
// kc get TYPE NAME -o yaml needs the right panel and switches it to
// Manifest.
func TestStartTargetsKeepTheRightStartModeEnvtest(t *testing.T) {
	tests := []struct {
		name      string
		intent    func(ns string) StartupIntent
		wantLeft  string
		wantRight PanelViewMode
	}{
		{
			name:      "kc overview",
			intent:    func(ns string) StartupIntent { return StartupIntent{Verb: KubectlVerbOverview, Namespace: ns} },
			wantLeft:  "/overview",
			wantRight: PanelModeInfo,
		},
		{
			name: "kc get pods",
			intent: func(ns string) StartupIntent {
				return StartupIntent{Verb: KubectlVerbGet, Namespace: ns, Get: &GetIntent{
					Tokens: []GetToken{{Value: "pods", ExplicitResource: true}},
				}}
			},
			wantLeft:  "/pods",
			wantRight: PanelModeInfo,
		},
		{
			name: "kc get pods NAME -o yaml",
			intent: func(ns string) StartupIntent {
				return StartupIntent{Verb: KubectlVerbGet, Namespace: ns, Get: &GetIntent{
					OutputFormat: "yaml",
					Tokens:       []GetToken{{Value: "pods", ExplicitResource: true}, {Value: "start-pod"}},
				}}
			},
			wantLeft:  "/pods",
			wantRight: PanelModeManifest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, cl := setupAppWithCluster(t)
			ns := "start-mode"
			t.Cleanup(createNamespace(t, cl, ns))
			t.Cleanup(createPod(t, cl, ns, "start-pod", nil))
			waitForNamespaceReady(t, app, ns)
			app.cfgStore = appconfig.NewStore(filepath.Join(t.TempDir(), "config.yaml"))
			_ = app.loadConfig()
			app.goToNamespace(ns)
			_ = app.initPanels()
			if got := app.rightPanel.Mode(); got != PanelModeInfo {
				t.Fatalf("right mode before the start target = %v, want Info", modeLabel(got))
			}

			app.startupIntent = tc.intent(ns)
			app.startupIntentApplied = false
			runStartupSelect(app, app.applyStartupIntent())
			if f, ok := app.leftNav.Current().(interface{ Close() }); ok && tc.wantLeft == "/overview" {
				t.Cleanup(f.Close)
			}
			if got := app.leftPanel.GetCurrentPath(); got != tc.wantLeft {
				t.Fatalf("left path = %q, want %q", got, tc.wantLeft)
			}
			if got := app.leftPanel.Mode(); got != PanelModeList {
				t.Fatalf("left mode = %v, want List", modeLabel(got))
			}
			if got := app.rightPanel.Mode(); got != tc.wantRight {
				t.Fatalf("right mode = %v, want %v", modeLabel(got), modeLabel(tc.wantRight))
			}
			if app.activePanel != 0 {
				t.Fatalf("active panel = %d, want the left panel", app.activePanel)
			}
		})
	}
}
