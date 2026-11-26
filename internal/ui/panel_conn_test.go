package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// testContext is a kubeconfig context of a file that is never read; each
// name gets its own cluster key.
func testContext(name string) *kubeconfig.Context {
	return &kubeconfig.Context{Name: name, Namespace: "shop", Kubeconfig: &kubeconfig.Kubeconfig{Path: "/nonexistent/" + name, Context: name}}
}

// A connection lives while anything holds it; a second release of one
// hold changes nothing.
func TestPanelConnHolders(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.cancel)
	dev := useTestCluster(app, testContext("dev"), nil, 0)
	if dev.holders() != 1 {
		t.Fatalf("panel holds %d, want 1", dev.holders())
	}
	release := dev.hold()
	useTestCluster(app, testContext("prod"), nil, 0)
	if dev.holders() != 1 {
		t.Fatalf("after the panel switched: %d holders, want the other hold", dev.holders())
	}
	release()
	release()
	if dev.holders() != 0 {
		t.Fatalf("after the release: %d holders, want 0", dev.holders())
	}
	if app.connByID(dev.connID()) != nil {
		t.Fatal("a connection nobody holds is still found by id")
	}
}

// A follower shows the other panel's connection, or its pin's; a pinned
// connection stays found by id after both panels left its context.
func TestTargetConnOfFollowersAndPins(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.cancel)
	dev := useTestCluster(app, testContext("dev"), nil, 0)
	prod := useTestCluster(app, testContext("prod"), nil, 1)
	if app.targetConn(0) != dev || app.targetConn(1) != prod {
		t.Fatal("list panels do not show their own connections")
	}
	app.setPanelMode(t.Context(), app.rightPanel, PanelModeDescribe, "test")
	if app.targetConn(1) != dev || app.ownConn(1) != prod {
		t.Fatal("a Describe panel does not show the other panel's connection")
	}
	app.syncPanelScopeLabels()
	if got := app.rightPanel.scope.Context; got != "dev" {
		t.Fatalf("the follower's frame names %q, want dev", got)
	}

	app.rightPanel.SetPin(&panelPin{conn: dev, release: dev.hold(), scope: "shop"})
	useTestCluster(app, testContext("stage"), nil, 0)
	if app.targetConn(1) != dev || app.connByID(dev.connID()) != dev {
		t.Fatal("the pin lost its connection when the followed panel switched")
	}
	app.rightPanel.ClearPin()
	if dev.holders() != 0 || app.targetConn(1) != app.ownConn(0) {
		t.Fatalf("after unpinning: %d holders on the pinned connection", dev.holders())
	}
}

// A list row carries the connection it was read from.
func TestSelectionCarriesTheConnection(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.cancel)
	ctx := t.Context()
	dev := useTestCluster(app, testContext("dev"), nil, 0)
	useTestCluster(app, testContext("prod"), nil, 1)
	row := pinPod("web", "uid-web")
	app.leftNav = navui.NewNavigator(modeltesting.NewStaticFolder("pods", []table.Column{{Title: " Name"}}, []table.Row{row}))
	app.leftPanel.UseFolder(ctx, true)
	app.syncPanelWithNavigator(0)
	app.leftPanel.SelectByRowID(ctx, "web")
	if got := app.leftPanel.CurrentSelection(ctx).Source; got != dev.connID() {
		t.Fatalf("selection source = %q, want %q", got, dev.connID())
	}
	item, conn := app.selectedRow(ctx, app.leftPanel)
	if item == nil || conn != dev {
		t.Fatalf("selectedRow = %v from %q, want web from dev", item, conn.name())
	}
}

// Requests a widget sends carry the panel they came from, also inside a
// batch; other messages pass as they are.
func TestStampOriginWrapsPanelRequests(t *testing.T) {
	p := NewPanel("left")
	req := models.ConfirmRequest{}
	cmd := p.stampOrigin(func() tea.Msg {
		return tea.BatchMsg{
			func() tea.Msg { return req },
			func() tea.Msg { return showToastMsg{text: "x"} },
		}
	})
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("stamped batch = %#v", cmd())
	}
	if m, ok := batch[0]().(panelOriginMsg); !ok || m.panel != p || m.msg != req {
		t.Fatalf("request = %#v, want it stamped with the panel", batch[0]())
	}
	if _, ok := batch[1]().(showToastMsg); !ok {
		t.Fatalf("a toast was stamped: %#v", batch[1]())
	}
	if p.stampOrigin(nil) != nil {
		t.Fatal("a nil command became a command")
	}
}

// A confirmation asked for by a panel uses that panel's context and its
// read-only policy, whichever panel is active when it arrives.
func TestConfirmUsesTheOriginPanelContext(t *testing.T) {
	app, _ := readOnlyTestApp(t, false)
	useTestCluster(app, testContext("dev"), nil, 1)
	app.activePanel = 1
	confirmed := false
	action := stubConfirmed{confirmed: &confirmed}

	fromLeft := panelOriginMsg{panel: app.leftPanel, msg: models.ConfirmRequest{Action: action}}
	wantRefusal(t, app.handlePanelOrigin(fromLeft), "Start a debug helper")
	if app.pendingConfirm != nil {
		t.Fatal("eu-prod's request opened a confirmation in the writable panel")
	}

	app.activePanel = 0
	fromRight := panelOriginMsg{panel: app.rightPanel, msg: models.ConfirmRequest{Action: action}}
	if cmd := app.handlePanelOrigin(fromRight); isRefusal(cmd) {
		t.Fatal("dev's request was refused under eu-prod's policy")
	}
	if p := app.pendingConfirm; p == nil || p.kubeContext != "dev" || p.conn != app.ownConn(1) {
		t.Fatalf("pending confirmation = %+v, want dev's", p)
	}
	app.handleConfirmResult(confirmResultMsg{ok: true})
	if !confirmed {
		t.Fatal("the approved action did not run")
	}
}

// C-x n explains traffic within one cluster; pods of two clusters get a
// refusal that names both contexts.
func TestNetworkPolicyCheckNeedsOneCluster(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.cancel)
	ctx := t.Context()
	useTestCluster(app, testContext("dev"), nil, 0)
	useTestCluster(app, testContext("prod"), nil, 1)
	for idx, name := range []string{"web", "api"} {
		row := pinPod(name, types.UID("uid-"+name))
		nav := navui.NewNavigator(modeltesting.NewStaticFolder("pods", []table.Column{{Title: " Name"}}, []table.Row{row}))
		if idx == 0 {
			app.leftNav = nav
		} else {
			app.rightNav = nav
		}
		app.panelByIndex(idx).UseFolder(ctx, true)
		app.syncPanelWithNavigator(idx)
		app.panelByIndex(idx).SelectByRowID(ctx, name)
	}
	cmd := app.explainNetworkPolicy()
	toast, ok := cmd().(showToastMsg)
	if !ok || !strings.Contains(toast.text, "one cluster") || !strings.Contains(toast.text, "dev") || !strings.Contains(toast.text, "prod") {
		t.Fatalf("C-x n across clusters = %#v, want a refusal naming dev and prod", cmd())
	}
}

// The shell follows the active panel's context, and a follower's target;
// a command started for one panel keeps its own kubeconfig.
func TestTerminalFollowsTheActivePanel(t *testing.T) {
	e := newExecTestApp(t, appconfig.TerminalModeOverlay)
	app := e.app
	useTestCluster(app, e.ctxA, nil, 0)
	useTestCluster(app, e.ctxB, nil, 1)
	shell := func() string {
		t.Helper()
		v, ok := envValue(app.terminal.env, "KUBECONFIG")
		if !ok {
			t.Fatal("shell environment has no KUBECONFIG")
		}
		return resolveKubeconfig(t, v).context
	}

	app.setActivePanel(0, "test")
	app.ensureTermContext()
	if got := shell(); got != "ctx-a" {
		t.Fatalf("shell with the left panel active targets %q, want ctx-a", got)
	}
	run, err := app.commandExecFor(app.leftPanel, app.leftPanel, app.ownConn(0)).CommandExec("cmd-left", "")
	if err != nil {
		t.Fatal(err)
	}

	app.setActivePanel(1, "test")
	if got := shell(); got != "ctx-b" {
		t.Fatalf("shell with the right panel active targets %q, want ctx-b", got)
	}
	gotKubeconfig, _ := envValue(run.Env, "KUBECONFIG")
	if got := resolveKubeconfig(t, gotKubeconfig).context; got != "ctx-a" {
		t.Fatalf("the left panel's command moved to %q, want ctx-a", got)
	}

	// A Describe panel follows the left panel: the shell targets ctx-a.
	app.setPanelMode(t.Context(), app.rightPanel, PanelModeDescribe, "test")
	if got := shell(); got != "ctx-a" {
		t.Fatalf("shell with the right panel following the left targets %q, want ctx-a", got)
	}
	e.assertKubeconfigsUnchanged(t)
}

// TestLogsViewerReleasesItsHold: the full screen logs viewer holds the
// connection of the row it shows until it closes, by F3, F10, Esc Esc or
// another viewer taking its place.
func TestLogsViewerReleasesItsHold(t *testing.T) {
	closeWith := map[string]func(*App){
		"F3":  func(a *App) { helpPress(a, tea.KeyF3) },
		"F10": func(a *App) { helpPress(a, tea.KeyF10) },
		"Esc Esc": func(a *App) {
			helpPress(a, tea.KeyEscape)
			helpPress(a, tea.KeyEscape)
		},
		"another viewer": func(a *App) {
			a.presentLogsViewer(NewLogsViewer("t2", testLogsSpec(), nil, "dracula"), "t2", func() {})
		},
	}
	for name, closeViewer := range closeWith {
		t.Run(name, func(t *testing.T) {
			app := NewApp()
			t.Cleanup(app.cancel)
			app.width, app.height = 100, 30
			conn := useTestCluster(app, testContext("dev"), nil, 0)
			before := conn.holders()
			app.presentLogsViewer(NewLogsViewer("t", testLogsSpec(), nil, "dracula"), "t", conn.hold())
			if got := conn.holders(); got != before+1 {
				t.Fatalf("holders with the viewer open = %d, want %d", got, before+1)
			}
			closeViewer(app)
			if got := conn.holders(); got != before {
				t.Fatalf("holders after closing the viewer = %d, want %d", got, before)
			}
		})
	}
}

// toastText runs cmd and returns the toast it shows, "" for none.
func toastText(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		return ""
	}
	if toast, ok := cmd().(showToastMsg); ok {
		return toast.text
	}
	return ""
}

// TestNamespaceCreateKeepsTheDialogsConnection: F7 creates the namespace
// in the context its panel showed when the dialog opened. A switch that
// lands while the name is typed does not carry the namespace elsewhere.
func TestNamespaceCreateKeepsTheDialogsConnection(t *testing.T) {
	app, panel := readOnlyTestApp(t, true)
	if cmd := app.createNamespaceForPanel(panel); isRefusal(cmd) {
		t.Fatal("F7 refused in a writable context")
	}
	if !app.modalManager.IsModalVisible() {
		t.Fatal("F7 opened no dialog")
	}
	useTestCluster(app, testContext("dev"), nil, 0)
	got := toastText(t, app.createNamespaceWithName("new-ns"))
	if want := "Namespace new-ns not created: the left panel switched from eu-prod to dev"; got != want {
		t.Fatalf("confirm after a switch shows %q, want %q", got, want)
	}
	if app.namespaceCreateConn != nil || app.namespaceCreatePanel != -1 {
		t.Fatal("the refused dialog left its target behind")
	}
}

// TestNamespaceCommandStaysInItsContext: a namespace command runs in the
// context of the panel that named the namespace, says so in its frame,
// and neither starts nor restarts in a context the user did not choose.
func TestNamespaceCommandStaysInItsContext(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.cancel)
	app.width, app.height = 120, 30
	dev := useTestCluster(app, testContext("dev"), nil, 0)
	useTestCluster(app, testContext("prod"), nil, 1)
	cfg := appconfig.CommandConfig{Name: "Top Pods", Type: appconfig.CommandTypeNamespace}

	// Without a namespace the panel shows a placeholder and runs nothing.
	app.startNamespaceCommand(1, cfg, "", dev)
	if got := commandConnOf(app.rightPanel); got != dev {
		t.Fatalf("command connection = %q, want dev", got.name())
	}
	if app.rightPanel.Mode() != PanelModeCommand {
		t.Fatalf("right panel mode = %v, want Command", app.rightPanel.Mode())
	}
	if got := app.rightPanel.scope.Context; got != "dev" {
		t.Fatalf("command panel frame names %q, want dev", got)
	}

	useTestCluster(app, testContext("staging"), nil, 0)
	token := app.commandWatchToken[1]
	app.restartNamespaceCommand(1, cfg, "web")
	if got := app.namespaceCommandTarget[1]; got == "web" {
		t.Fatal("the command restarted for a namespace of another context")
	}
	if app.commandWatchToken[1] == token {
		t.Fatal("the refused restart left the watch running")
	}
	got := toastText(t, app.startNamespaceCommand(1, cfg, "web", dev))
	if want := "Top Pods not started: " + errContextChanged.Error(); got != want {
		t.Fatalf("start with a released connection shows %q, want %q", got, want)
	}
}

// TestListPanelHasNoBorrowedRow: a List panel is told about the other
// panel's selection too. With nothing under its own cursor it must not act
// on that row, which lives in the other panel's cluster.
func TestListPanelHasNoBorrowedRow(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.cancel)
	ctx := t.Context()
	useTestCluster(app, testContext("dev"), nil, 0)
	useTestCluster(app, testContext("prod"), nil, 1)
	web := pinPod("web", "uid-web")
	app.leftNav = navui.NewNavigator(modeltesting.NewStaticFolder("pods", []table.Column{{Title: " Name"}}, []table.Row{web}))
	app.rightNav = navui.NewNavigator(modeltesting.NewStaticFolder("pods", []table.Column{{Title: " Name"}}, nil))
	app.leftPanel.UseFolder(ctx, true)
	app.rightPanel.UseFolder(ctx, true)
	app.syncPanelWithNavigator(0)
	app.syncPanelWithNavigator(1)
	moveLeft(t, app, web)
	app.rightPanel.RefreshFolder(ctx)

	if item, ok := app.rightPanel.SelectedNavItem(ctx); ok {
		t.Fatalf("right SelectedNavItem = %v, want none", item)
	}
	if sel := app.rightPanel.CurrentSelection(ctx); sel.Item != nil {
		t.Fatalf("right CurrentSelection = %v from %q, want no row", sel.Item, sel.Source)
	}
	if item, conn := app.selectedRow(ctx, app.rightPanel); item != nil {
		t.Fatalf("right selectedRow = %v from %q, want none", item, conn.name())
	}
	app.activePanel = 1
	app.deleteResourceForPanel(app.rightPanel)
	if app.pendingDelete != nil {
		t.Fatalf("delete prepared %+v from a panel with no row", *app.pendingDelete)
	}
	if item, conn := app.selectedRow(ctx, app.leftPanel); item == nil || conn.name() != "dev" {
		t.Fatalf("left selectedRow = %v from %q, want web from dev", item, conn.name())
	}
}
