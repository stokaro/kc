package ui

import (
	"context"
	"path/filepath"
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
	s := waitDashboard(t, app, "no Prometheus (C-x m)")
	if !strings.Contains(s, "Deployment "+ns+"/web") {
		t.Fatalf("title lacks the target:\n%s", s)
	}
	s = waitDashboard(t, app, "now no pods that have not terminated")
	for l := range strings.SplitSeq(s, "\n") {
		if strings.Contains(l, "│") && strings.ContainsAny(l, "⡀⣀⣿▁█") {
			t.Fatalf("a line is drawn without measurements: %q\n%s", l, s)
		}
	}
	if lines := strings.Split(s, "\n"); len(lines) != 24 {
		t.Fatalf("%d lines at 80x24", len(lines))
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

	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.dashboardOpen() {
		t.Fatal("Esc did not close the dashboard")
	}
	if after := panelsOf(app); after != before {
		t.Fatalf("panels changed: %+v, want %+v", after, before)
	}
	if strings.Contains(dashboardScreen(app), "no Prometheus (C-x m)") {
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
	waitDashboard(t, app, "Deployment "+ns+"/web")
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
