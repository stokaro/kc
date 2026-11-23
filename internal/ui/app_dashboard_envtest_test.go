package ui

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// pooledTestApp is an App on the envtest API server through a cluster
// pool, as kc runs: the overview engine's namespace caches need it.
func pooledTestApp(t *testing.T, ns string) *App {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: testCfg.Host, CertificateAuthorityData: testCfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: testCfg.CertData, ClientKeyData: testCfg.KeyData, Token: testCfg.BearerToken}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest", Namespace: ns}
	kc.CurrentContext = "envtest"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(t.Context())
	t.Cleanup(app.cancel)
	app.clPool = kccluster.NewPool(time.Minute, 0)
	app.clPool.Start()
	t.Cleanup(app.clPool.Stop)
	app.currentCtx = &kubeconfig.Context{Name: "envtest", Namespace: ns, Kubeconfig: &kubeconfig.Kubeconfig{Path: path, Context: "envtest"}}
	var err error
	app.clLease, err = app.clPool.Acquire(t.Context(), app.currentClusterKey(), nil)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(app.clLease.Release)
	app.cl = app.clLease.Cluster()
	return app
}

// panelState is what opening and closing the dashboard must not change.
type panelState struct {
	paths  [2]string
	modes  [2]PanelViewMode
	scopes [2]string
	active int
}

func panelsOf(a *App) panelState {
	return panelState{
		paths:  [2]string{a.leftPanel.GetCurrentPath(), a.rightPanel.GetCurrentPath()},
		modes:  [2]PanelViewMode{a.leftPanel.Mode(), a.rightPanel.Mode()},
		scopes: [2]string{a.panelScope[0], a.panelScope[1]},
		active: a.activePanel,
	}
}

func dashboardScreen(a *App) string { return ansi.Strip(viewString(a.View())) }

// waitDashboard refreshes the open dashboard until its screen holds want.
func waitDashboard(t *testing.T, a *App, want string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_ = a.dash.model.Start() // reads the engine now; the tick it returns is dropped
		a.invalidateView("test")
		s := dashboardScreen(a)
		if strings.Contains(s, want) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("dashboard never showed %q; last screen:\n%s", want, s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestDashboardOpensOverUnchangedPanelsEnvtest opens the dashboard with
// C-x g on a Deployment row. Without metrics-server and Prometheus it
// says so and draws nothing; Esc returns to the panels as they were; C-x
// g reopens it; o shows the Deployment in the other panel.
func TestDashboardOpensOverUnchangedPanelsEnvtest(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ns := kctesting.UniqueName("dash")
	admin, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	kctesting.RunInBackground(t, "admin", admin.Start)
	t.Cleanup(admin.Stop)
	adminCreate(t, admin, &corev1.Namespace{Name: ns})
	labels := map[string]string{"app": "web"}
	dep := &appsv1.Deployment{
		Namespace: ns, Name: "web",
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
			},
		},
	}
	adminCreate(t, admin, dep)
	app := pooledTestApp(t, ns)
	app.goToNamespace(ns)
	_, _ = app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app.activePanel = 0
	deployments := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	runLinkCmds(t, app, app.openObjectRef(models.ObjectRef{GVR: deployments, Namespaced: true, Namespace: ns, Name: "web"}, ""))
	app.activePanel = 1
	before := panelsOf(app)

	_, _ = app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = app.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if !app.dashboardOpen() {
		t.Fatal("C-x g did not open the dashboard")
	}
	if got := app.dash.model.Root(); got.Kind != dashboard.KindWorkload || got.Name != "web" || got.Workload != deployments {
		t.Fatalf("target = %+v, want the Deployment under the cursor", got)
	}
	s := waitDashboard(t, app, "Metrics API, no samples yet")
	if !strings.Contains(s, "› Deployment web") || !strings.Contains(s, "Pods of Deployment web") {
		t.Fatalf("the top bar or the list lacks the target:\n%s", s)
	}
	s = waitDashboard(t, app, "usage no pods that have not t")
	for l := range strings.SplitSeq(s, "\n") {
		if strings.Contains(l, "│") && strings.ContainsAny(l, "⡀⣀⣿▁█") && !strings.Contains(l, "usage") {
			t.Fatalf("a line is drawn without measurements: %q\n%s", l, s)
		}
	}
	if lines := strings.Split(s, "\n"); len(lines) != 24 {
		t.Fatalf("%d lines at 80x24", len(lines))
	}
	// After a resize the frame is the window's height to the row, with
	// the F-key bar last, at every size.
	for _, size := range [][2]int{{150, 42}, {100, 30}, {80, 24}} {
		_, _ = app.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(dashboardScreen(app), "\n")
		if len(lines) != size[1] || !strings.Contains(lines[len(lines)-1], "10Close") {
			t.Fatalf("%d lines at %dx%d, last %q", len(lines), size[0], size[1], lines[len(lines)-1])
		}
	}

	// F1 shows the dashboard's help over it; Esc closes the help only.
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	if !strings.Contains(dashboardScreen(app), "Dashboard") || !app.modalManager.IsModalVisible() {
		t.Fatal("F1 did not show the help over the dashboard")
	}
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !app.dashboardOpen() || app.modalManager.IsModalVisible() {
		t.Fatal("Esc in the help must close the help and keep the dashboard")
	}

	// F2 drops down the Range menu over the dashboard; Esc closes the
	// menu only. The F-key bar is the last row.
	if lines := strings.Split(dashboardScreen(app), "\n"); !strings.Contains(lines[len(lines)-1], "10Close") {
		t.Fatalf("no F-key bar: %q", lines[len(lines)-1])
	}
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if !app.menuBar.IsOpen() || !strings.Contains(dashboardScreen(app), "Last 1 hour") {
		t.Fatalf("F2 did not open the Range menu:\n%s", dashboardScreen(app))
	}
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !app.dashboardOpen() || app.menuBar.IsOpen() {
		t.Fatal("Esc in the menu must close the menu and keep the dashboard")
	}

	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.dashboardOpen() {
		t.Fatal("Esc did not close the dashboard")
	}
	if after := panelsOf(app); after != before {
		t.Fatalf("panels changed: %+v, want %+v", after, before)
	}
	if strings.Contains(dashboardScreen(app), "Metrics API, no samples yet") {
		t.Fatal("the dashboard still shows after closing")
	}
	// A message of the closed dashboard does nothing.
	if _, cmd := app.Update(dashboard.TickMsg{}); cmd != nil {
		if _, ok := cmd().(dashboard.TickMsg); ok {
			t.Fatal("a closed dashboard keeps ticking")
		}
	}

	// C-x g on the same row restores it; o opens the Deployment in the
	// other panel and gives it the focus.
	_, _ = app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = app.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if !app.dashboardOpen() {
		t.Fatal("C-x g did not reopen the dashboard")
	}
	waitDashboard(t, app, "Pods of Deployment web")
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	runLinkCmds(t, app, cmd)
	if app.dashboardOpen() {
		t.Fatal("o did not leave the dashboard")
	}
	if app.activePanel != 0 || app.leftPanel.GetCurrentPath() != "/deployments" {
		t.Fatalf("active panel %d at %q, want the left panel at /deployments", app.activePanel, app.leftPanel.GetCurrentPath())
	}
	sctx, scancel := context.WithTimeout(t.Context(), time.Second)
	defer scancel()
	if sel := app.leftPanel.currentSelectionID(sctx); sel != "web" {
		t.Fatalf("selection = %q, want web", sel)
	}
}

// TestDashboardEventsAndLogsAroundEnvtest: an Event of the Deployment
// shows in the dashboard's Events; o opens the Deployment and points at
// its /rollout for the revisions; l sets the investigation window around the
// Event and shows the Deployment's logs, with the dashboard's panel in
// Logs mode.
func TestDashboardEventsAndLogsAroundEnvtest(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ns := kctesting.UniqueName("dash-ev")
	admin, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	kctesting.RunInBackground(t, "admin", admin.Start)
	t.Cleanup(admin.Stop)
	adminCreate(t, admin, &corev1.Namespace{Name: ns})
	labels := map[string]string{"app": "web"}
	dep := &appsv1.Deployment{
		Namespace: ns, Name: "web",
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
			},
		},
	}
	adminCreate(t, admin, dep)
	at := metav1.NewTime(time.Now().Add(-2 * time.Minute).Truncate(time.Second))
	adminCreate(t, admin, &corev1.Event{
		Namespace: ns, Name: "web.scale",
		InvolvedObject: corev1.ObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: ns, Name: "web", UID: dep.UID},
		Type:           corev1.EventTypeNormal, Reason: "ScalingReplicaSet", Message: "Scaled up replica set web-abc to 1",
		FirstTimestamp: at, LastTimestamp: at, Count: 1,
	})
	app := pooledTestApp(t, ns)
	app.goToNamespace(ns)
	_, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app.activePanel = 0
	deployments := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	runLinkCmds(t, app, app.openObjectRef(models.ObjectRef{GVR: deployments, Namespaced: true, Namespace: ns, Name: "web"}, ""))
	app.activePanel = 1
	openDash := func() {
		t.Helper()
		_, _ = app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		_, _ = app.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
		if !app.dashboardOpen() {
			t.Fatal("C-x g did not open the dashboard")
		}
	}
	openDash()
	s := waitDashboard(t, app, "◆ Rollout ScalingReplicaSet")
	if !strings.Contains(s, "Events in the last 15 minutes: 1") {
		t.Fatalf("events title:\n%s", s)
	}
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if !app.dash.model.EventsFocused() {
		t.Fatalf("S-Tab did not reach the Events:\n%s", dashboardScreen(app))
	}

	_, cmd := app.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	toasts := runLinkCmds(t, app, cmd)
	if app.dashboardOpen() || app.activePanel != 0 || app.leftPanel.GetCurrentPath() != "/deployments" {
		t.Fatalf("o: open %v, active %d at %q; want the Deployment in the left panel", app.dashboardOpen(), app.activePanel, app.leftPanel.GetCurrentPath())
	}
	if !slices.ContainsFunc(toasts, func(s string) bool { return strings.Contains(s, "its revisions are in /rollout") }) {
		t.Errorf("toasts = %q, want the Deployment's revisions pointed at /rollout", toasts)
	}

	// From the left panel, C-x g on the same row restores the dashboard
	// with the Events in focus; l asks for the logs around the Event.
	openDash()
	if s := waitDashboard(t, app, "◆ Rollout ScalingReplicaSet"); !app.dash.model.EventsFocused() {
		t.Fatalf("the restored dashboard lost the focus:\n%s", s)
	}
	_, _ = app.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if app.dashboardOpen() {
		t.Fatal("l did not leave the dashboard")
	}
	// The Event was last seen at its lastTimestamp or, later, when the API
	// server created it: the window starts five minutes before that.
	if lo, hi := at.Add(-5*time.Minute), time.Now().Add(-5*time.Minute); app.window.Start.Before(lo) || app.window.Start.After(hi) || !app.window.End.IsZero() {
		t.Errorf("window = %+v, want from between %v and %v up to now", app.window, lo, hi)
	}
	if app.leftPanel.Mode() != PanelModeLogs || app.rightPanel.GetCurrentPath() != "/deployments" {
		t.Errorf("left mode %v, right at %q; want the left panel in Logs following the Deployment on the right", app.leftPanel.Mode(), app.rightPanel.GetCurrentPath())
	}
}
