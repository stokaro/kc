package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/types"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

func TestResolveReadOnly(t *testing.T) {
	t.Parallel()
	prodOnly := appconfig.ReadOnlyConfig{Contexts: []string{"*prod*"}}
	all := appconfig.ReadOnlyConfig{Enabled: true}
	tests := []struct {
		name     string
		override ReadOnlyOverride
		cfg      appconfig.ReadOnlyConfig
		context  string
		want     bool
	}{
		{name: "no flag, no config", override: ReadOnlyFromConfig, context: "eu-prod", want: false},
		{name: "no flag, matching context", override: ReadOnlyFromConfig, cfg: prodOnly, context: "eu-prod", want: true},
		{name: "no flag, other context", override: ReadOnlyFromConfig, cfg: prodOnly, context: "kind-dev", want: false},
		{name: "no flag, enabled", override: ReadOnlyFromConfig, cfg: all, context: "kind-dev", want: true},
		{name: "--readonly without config", override: ReadOnlyForced, context: "kind-dev", want: true},
		{name: "--readonly beats a non-matching config", override: ReadOnlyForced, cfg: prodOnly, context: "kind-dev", want: true},
		{name: "--write beats a matching pattern", override: ReadOnlyWriteForced, cfg: prodOnly, context: "eu-prod", want: false},
		{name: "--write beats enabled", override: ReadOnlyWriteForced, cfg: all, context: "eu-prod", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveReadOnly(tt.override, tt.cfg, tt.context); got != tt.want {
				t.Fatalf("resolveReadOnly = %v, want %v", got, tt.want)
			}
		})
	}
}

// readOnlyTestObject knows its instance, as list rows do: delete asks for
// the UID and the cluster the row was read from.
var readOnlyTestObject = stubInstance{
	id:        "group/v1/tests/foo",
	namespace: "ns",
	name:      "foo",
	verbs:     []string{"get", "list", "update", "delete"},
}

type stubInstance struct{ stubObject }

func (stubInstance) UID() types.UID { return "uid-foo" }

func (stubInstance) Origin() models.ObjectOrigin {
	return models.ObjectOrigin{Cluster: &kccluster.Cluster{}, Context: "eu-prod"}
}

// readOnlyTestApp returns an app on context "eu-prod", which the config
// makes read-only, with an editable and deletable object selected in the
// left panel at /namespaces. writable passes --write.
func readOnlyTestApp(t *testing.T, writable bool) (*App, *Panel) {
	t.Helper()
	app := NewApp()
	app.width, app.height = 120, 30
	model, _ := app.Update(tea.WindowSizeMsg{Width: app.width, Height: app.height})
	app = model.(*App)
	app.currentCtx = &kubeconfig.Context{Name: "eu-prod", Kubeconfig: &kubeconfig.Kubeconfig{Path: "/nonexistent/kubeconfig"}}
	app.cfg.ReadOnly = appconfig.ReadOnlyConfig{Contexts: []string{"*prod*"}}
	if writable {
		app.readOnlyOverride = ReadOnlyWriteForced
	}
	panel := app.leftPanel
	// The app allows delete only with a cluster client; tests have none.
	panel.SetEnvironmentSupplier(func() PanelEnvironment {
		env := app.panelEnvironment()
		env.AllowDeleteObjects = true
		return env
	})
	ctx := t.Context()
	panel.UseFolder(ctx, true)
	panel.SetFolder(ctx, newStubFolder([]models.Item{readOnlyTestObject}), false)
	panel.SelectByRowID(ctx, readOnlyTestObject.id)
	panel.SetCurrentPath("/namespaces")
	app.activePanel = 0
	if got := app.readOnly(); got == writable {
		t.Fatalf("readOnly() = %v with writable = %v", got, writable)
	}
	return app, panel
}

// wantRefusal asserts that cmd shows the read-only message for action.
func wantRefusal(t *testing.T, cmd tea.Cmd, action string) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("%s: no command, want a read-only refusal", action)
	}
	toast, ok := cmd().(showToastMsg)
	if !ok {
		t.Fatalf("%s: command returned %T, want a toast", action, cmd())
	}
	if want := "Read-only mode: " + action + " is disabled"; toast.text != want {
		t.Fatalf("%s: toast %q, want %q", action, toast.text, want)
	}
}

// isRefusal reports whether cmd is a read-only refusal, without running
// commands that are not plain toasts.
func isRefusal(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	toast, ok := cmd().(showToastMsg)
	return ok && strings.HasPrefix(toast.text, "Read-only mode:")
}

func TestReadOnlyCapabilitiesAndFunctionBar(t *testing.T) {
	for _, writable := range []bool{false, true} {
		name := "read-only"
		if writable {
			name = "writable"
		}
		t.Run(name, func(t *testing.T) {
			app, panel := readOnlyTestApp(t, writable)
			caps := panel.Capabilities(t.Context())
			if !caps.CanView {
				t.Fatalf("F3 must stay enabled: %+v", caps)
			}
			if caps.CanEdit != writable || caps.CanDelete != writable || caps.CanCreateNS != writable {
				t.Fatalf("edit/delete/create namespace = %v/%v/%v, want %v", caps.CanEdit, caps.CanDelete, caps.CanCreateNS, writable)
			}
			enabled := make(map[functionKeyID]bool)
			for _, k := range panelFunctionKeys(app.functionBarState()) {
				enabled[k.ID] = k.Enabled
			}
			for _, id := range []functionKeyID{functionKeyEdit, functionKeyNamespace, functionKeyDelete} {
				if enabled[id] != writable {
					t.Errorf("function key %d enabled = %v, want %v", id, enabled[id], writable)
				}
			}
			if !enabled[functionKeyView] || !enabled[functionKeyCopy] {
				t.Errorf("F3 and F5 must stay enabled: %v", enabled)
			}
		})
	}
}

func TestReadOnlyHandlersRefuse(t *testing.T) {
	target := deleteTarget{gvr: readOnlyTestObject.GVR(), namespace: "ns", name: "foo"}
	tests := []struct {
		name   string
		action string
		run    func(a *App, p *Panel) tea.Cmd
		// sideEffect reports whether the handler went past the gate.
		sideEffect func(a *App) bool
	}{
		{name: "F4 handler", action: "Edit", run: func(a *App, p *Panel) tea.Cmd { return a.editSelectionForPanel(p) }},
		{name: "kubectl edit", action: "Edit", run: func(a *App, p *Panel) tea.Cmd { return a.runKubectlEdit(0, "/", readOnlyTestObject) }},
		{
			name: "F8 handler", action: "Delete",
			run:        func(a *App, p *Panel) tea.Cmd { return a.deleteResourceForPanel(p) },
			sideEffect: func(a *App) bool { return a.pendingDelete != nil || a.modalManager.IsModalVisible() },
		},
		{name: "delete request", action: "Delete", run: func(a *App, p *Panel) tea.Cmd { return a.performDelete(target) }},
		{
			name: "F7 handler", action: "Create namespace",
			run:        func(a *App, p *Panel) tea.Cmd { return a.createNamespaceForPanel(p) },
			sideEffect: func(a *App) bool { return a.modalManager.IsModalVisible() },
		},
		{name: "namespace create request", action: "Create namespace", run: func(a *App, p *Panel) tea.Cmd { return a.createNamespaceWithName("new-ns") }},
		{name: "F4 key on a grayed key", action: "Edit", run: func(a *App, p *Panel) tea.Cmd { return p.invokeActionIfAllowed(t.Context(), PanelActionEdit) }},
		{name: "F8 key on a grayed key", action: "Delete", run: func(a *App, p *Panel) tea.Cmd { return p.invokeActionIfAllowed(t.Context(), PanelActionDelete) }},
		{name: "F7 key on a grayed key", action: "Create namespace", run: func(a *App, p *Panel) tea.Cmd {
			return p.invokeActionIfAllowed(t.Context(), PanelActionCreateNamespace)
		}},
		{name: "Esc 8", action: "Delete", run: func(a *App, p *Panel) tea.Cmd {
			a.escPressed = true
			_, cmd := a.handleKeyMsg(tea.KeyPressMsg{Code: '8', Text: "8"}, nil)
			return cmd
		}},
		{name: "click on grayed F4", action: "Edit", run: func(a *App, p *Panel) tea.Cmd { return a.handleFunctionKeyClick(functionKeyX(a, functionKeyEdit)) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, panel := readOnlyTestApp(t, false)
			wantRefusal(t, tt.run(app, panel), tt.action)
			if tt.sideEffect != nil && tt.sideEffect(app) {
				t.Fatal("the handler went past the read-only gate")
			}
		})
	}
	// The same handlers run when the context is writable.
	for _, tt := range tests {
		if tt.sideEffect == nil {
			continue
		}
		t.Run(tt.name+" writable", func(t *testing.T) {
			app, panel := readOnlyTestApp(t, true)
			cmd := tt.run(app, panel)
			if isRefusal(cmd) {
				t.Fatal("refused in a writable context")
			}
			if !tt.sideEffect(app) {
				t.Fatal("the handler did nothing in a writable context")
			}
		})
	}
}

// functionKeyX returns a column inside the function bar slot of id.
func functionKeyX(a *App, id functionKeyID) int {
	state := a.functionBarState()
	keys := panelFunctionKeys(state)
	starts := functionKeySlotStarts(len(keys), state.Width)
	for i, k := range keys {
		if k.ID == id {
			return starts[i] + 1
		}
	}
	return -1
}

type stubConfirmed struct {
	stubObject
	confirmed *bool
}

func (s stubConfirmed) Confirmation() models.Confirmation {
	return models.Confirmation{Title: "Start a debug helper?", Action: "Start"}
}

func (s stubConfirmed) Confirm() error {
	*s.confirmed = true
	return nil
}

func TestReadOnlyRefusesConfirmedActions(t *testing.T) {
	t.Run("dialog is not opened", func(t *testing.T) {
		app, _ := readOnlyTestApp(t, false)
		confirmed := false
		wantRefusal(t, app.showConfirm(stubConfirmed{confirmed: &confirmed}), "Start a debug helper")
		if app.pendingConfirm != nil || app.modalManager.IsModalVisible() {
			t.Fatal("read-only mode opened the confirmation dialog")
		}
	})
	t.Run("approval after the context became read-only", func(t *testing.T) {
		app, _ := readOnlyTestApp(t, true)
		confirmed := false
		if cmd := app.showConfirm(stubConfirmed{confirmed: &confirmed}); isRefusal(cmd) || app.pendingConfirm == nil {
			t.Fatal("a writable context must open the dialog")
		}
		app.readOnlyOverride = ReadOnlyForced
		wantRefusal(t, app.handleConfirmResult(confirmResultMsg{ok: true}), "Start a debug helper")
		if confirmed {
			t.Fatal("Confirm ran in read-only mode")
		}
	})
	t.Run("writable context confirms", func(t *testing.T) {
		app, _ := readOnlyTestApp(t, true)
		confirmed := false
		app.showConfirm(stubConfirmed{confirmed: &confirmed})
		app.handleConfirmResult(confirmResultMsg{ok: true})
		if !confirmed {
			t.Fatal("Confirm did not run in a writable context")
		}
	})
}

func TestReadOnlyDangerousCommands(t *testing.T) {
	restart := appconfig.CommandConfig{Name: "Restart", Command: "kubectl rollout restart deploy", Type: appconfig.CommandTypeGlobal, Dangerous: true}
	top := appconfig.CommandConfig{Name: "Top Nodes", Command: "kubectl top nodes", Type: appconfig.CommandTypeGlobal}
	menu := func(a *App) []string {
		a.showContextMenuForPanel(a.leftPanel)
		var names []string
		if a.activeCommandSelector != nil {
			for _, e := range a.activeCommandSelector.entries {
				if e.command != nil {
					names = append(names, e.command.Name)
				}
			}
		}
		return names
	}
	t.Run("read-only hides and refuses dangerous commands", func(t *testing.T) {
		app, _ := readOnlyTestApp(t, false)
		app.cfg.Commands = []appconfig.CommandConfig{restart, top}
		if got := menu(app); len(got) != 1 || got[0] != "Top Nodes" {
			t.Fatalf("F9 menu = %v, want only Top Nodes", got)
		}
		app.cfg.Commands = []appconfig.CommandConfig{restart}
		if app.hasApplicableCommands(app.leftPanel) {
			t.Fatal("F9 enabled with only a dangerous command")
		}
		wantRefusal(t, app.refuseCommand(restart), `command "Restart"`)
		wantRefusal(t, app.startNamespaceCommand(1, restart, "ns"), `command "Restart"`)
		if app.refuseCommand(top) != nil {
			t.Fatal("a command not marked dangerous was refused")
		}
	})
	t.Run("writable shows every command", func(t *testing.T) {
		app, _ := readOnlyTestApp(t, true)
		app.cfg.Commands = []appconfig.CommandConfig{restart, top}
		if got := menu(app); len(got) != 2 {
			t.Fatalf("F9 menu = %v, want both commands", got)
		}
		if app.refuseCommand(restart) != nil {
			t.Fatal("writable context refused a dangerous command")
		}
	})
}

func TestReadOnlyMarkerInFrame(t *testing.T) {
	out, hits := renderScopeLabel(panelScopeLabel{Context: "mba", Namespace: "kube-system", ReadOnly: true}, "─", 1, 60, nil)
	if got := ansi.Strip(out); got != "─ RO  mba : kube-system " {
		t.Fatalf("label = %q", got)
	}
	// "└─ RO  mba : kube-system ": the context label starts after the marker.
	if hits.ctxFrom != 6 || hits.ctxTo != 11 || hits.nsFrom != 12 {
		t.Fatalf("hits = %+v", hits)
	}

	app, panel := readOnlyTestApp(t, false)
	app.syncPanelScopeLabels()
	if !panel.scope.ReadOnly || panel.scope.Context != "eu-prod" {
		t.Fatalf("scope = %+v, want read-only eu-prod", panel.scope)
	}
	app.readOnlyOverride = ReadOnlyWriteForced
	app.syncPanelScopeLabels()
	if panel.scope.ReadOnly {
		t.Fatal("writable context still marked RO")
	}
}

// makeDeps passes read-only mode to the folders of a context, so the pod
// filesystem offers no debug helper.
func TestReadOnlyDeps(t *testing.T) {
	app, _ := readOnlyTestApp(t, false)
	app.cfg.ReadOnly = appconfig.ReadOnlyConfig{Contexts: []string{"*prod*"}}
	if !app.makeDeps(nil, nil, keyFor("eu-prod")).ReadOnly {
		t.Fatal("deps of a read-only context are writable")
	}
	if app.makeDeps(nil, nil, keyFor("kind-dev")).ReadOnly {
		t.Fatal("deps of a writable context are read-only")
	}
}

func keyFor(contextName string) kccluster.Key {
	return kccluster.Key{ContextName: contextName}
}
