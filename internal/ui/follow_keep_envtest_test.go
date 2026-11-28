package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kctesting "github.com/stokaro/kc/internal/testing"
)

// Issue #377: Logs and Info keep the object they show while the other
// panel's cursor is on a row that names no object, and their frame names
// the context and namespace the object was read in. A row that names
// another object replaces it at once.

// A cd to another namespace puts the left cursor on "..": Logs goes on
// streaming the pod of the old namespace, under a frame that names that
// namespace, and acts on that pod; its shell line runs there too. The
// next pod replaces the stream.
func TestLogsKeepsItsStreamAfterCdToAnotherNamespace(t *testing.T) {
	e := newFollowEnv(t, "checkout")
	app, ctx := e.app, t.Context()
	streams := useFakeLogStreams(t, app.rightPanel)
	e.openFirstRow(t, "cd pods", "checkout")
	settle(t, app, app.setPanelMode(ctx, app.rightPanel, PanelModeLogs, "test"))
	conn := app.ownConn(0)
	assertLogsStream(t, app, streams, conn, e.shop, "checkout", 1, 0)

	shellCd(t, app, "cd -n "+e.batch+" pods")
	assertLeftOnBack(t, app, e.batch)
	assertLogsStream(t, app, streams, conn, e.shop, "checkout", 1, 0)
	assertShellOfRightPanel(t, app, conn, e.shop)

	e.waitForRow(t, "worker")
	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	assertLogsStream(t, app, streams, conn, e.batch, "worker", 2, 1)
	assertShellOfRightPanel(t, app, conn, e.batch)
}

// Info keeps the workload it summarizes while the left cursor names no
// object, and a pin takes that workload. A pod is not an object Info
// shows: on a pod Info shows the pod's namespace, and after a cd it
// follows the new namespace.
func TestInfoKeepsItsWorkloadAfterCdToAnotherNamespace(t *testing.T) {
	e := newFollowEnv(t, "checkout")
	app, ctx := e.app, t.Context()
	labels := map[string]string{"app": "storefront"}
	dep := &appsv1.Deployment{
		Namespace: e.shop, Name: "storefront",
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.9"}}},
			},
		},
	}
	if err := e.client.Create(ctx, dep); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	e.openFirstRow(t, "cd deployments", "storefront")
	settle(t, app, app.setPanelMode(ctx, app.rightPanel, PanelModeInfo, "test"))
	conn := app.ownConn(0)
	assertInfoShows(t, app, conn, e.shop, "storefront")

	shellCd(t, app, "cd -n "+e.batch+" pods")
	assertLeftOnBack(t, app, e.batch)
	assertInfoShows(t, app, conn, e.shop, "storefront")

	settle(t, app, app.pin(1, app.rightPanel))
	pin := app.rightPanel.pinned()
	if pin == nil || followedName(pin.item) != "storefront" || pin.scope != e.shop || pin.conn != conn {
		t.Fatalf("the pin keeps %+v, want storefront in %s", pin, e.shop)
	}
	settle(t, app, app.unpin(app.rightPanel))
	assertInfoShows(t, app, conn, e.shop, "storefront")

	e.waitForRow(t, "worker")
	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	assertInfoShows(t, app, conn, e.batch, "")

	shellCd(t, app, "cd -n "+e.shop+" pods")
	assertLeftOnBack(t, app, e.shop)
	assertInfoShows(t, app, conn, e.shop, "")
}

// A context switch puts the left panel at the root of the new context:
// Logs goes on streaming the pod of the old context and names that
// context, which it holds. A pod of the same name in the new context
// replaces the stream at once, and the old connection is let go.
func TestLogsSwitchesToPodOfAnotherContext(t *testing.T) {
	e := newTwoClusters(t)
	app, ctx := e.app, t.Context()
	if err := e.clientA.Create(ctx, testPod(e.ns, "web")); err != nil {
		t.Fatalf("create pod web on a: %v", err)
	}
	if err := e.clientB.Create(ctx, testPod(e.ns, "web")); err != nil {
		t.Fatalf("create pod web on b: %v", err)
	}
	settle(t, app, tea.Batch(app.resizePanelsForCurrentLayout()...))
	app.setActivePanel(0, "test")
	streams := useFakeLogStreams(t, app.rightPanel)
	openPodWeb(t, app)
	settle(t, app, app.setPanelMode(ctx, app.rightPanel, PanelModeLogs, "test"))
	dev := app.ownConn(0)
	assertLogsStream(t, app, streams, dev, e.ns, "web", 1, 0)

	_, cmd := app.Update(switchResult(t, app, []int{0}, e.prod))
	settle(t, app, cmd)
	prod := app.ownConn(0)
	if prod == dev || prod.name() != e.prod.Name {
		t.Fatalf("the left panel is in %q after the switch to %s", prod.name(), e.prod.Name)
	}
	if item, ok := app.leftPanel.SelectedNavItem(ctx); ok && namesObject(item) {
		t.Fatalf("the left cursor is on object %v after the switch, want a row without one", item)
	}
	assertLogsStream(t, app, streams, dev, e.ns, "web", 1, 0)
	assertShellOfRightPanel(t, app, dev, e.ns)

	openPodWeb(t, app)
	assertLogsStream(t, app, streams, prod, e.ns, "web", 2, 1)
	if n := dev.holders(); n != 0 {
		t.Fatalf("the old connection has %d holders after Logs left it", n)
	}
}

// openPodWeb opens the pods of the left panel's namespace and puts its
// cursor on pod web.
func openPodWeb(t *testing.T, app *App) {
	t.Helper()
	ctx := t.Context()
	shellCd(t, app, "cd pods")
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		_, cmd := app.Update(FolderDirtyMsg{PanelIdx: 0})
		settle(t, app, cmd)
		_, ok := app.leftNav.Current().ItemByID(ctx, "web")
		return ok
	})
	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	if id := app.leftPanel.currentSelectionID(ctx); id != "web" {
		t.Fatalf("the left cursor is on %q, want web", id)
	}
}

// assertShellOfRightPanel fails unless the shell line runs in context
// conn and namespace ns while the right panel is active.
func assertShellOfRightPanel(t *testing.T, app *App, conn *panelConn, ns string) {
	t.Helper()
	app.setActivePanel(1, "test")
	c, got := app.termTarget()
	app.setActivePanel(0, "test")
	if c != conn || got != ns {
		t.Fatalf("the shell line of the right panel runs in %s : %s, want %s : %s", c.name(), got, conn.name(), ns)
	}
}

// assertLeftOnBack fails unless the left panel lists the pods of
// namespace ns with its cursor on "..".
func assertLeftOnBack(t *testing.T, app *App, ns string) {
	t.Helper()
	if app.panelScope[0] != ns || app.leftPanel.GetCurrentPath() != "/pods" {
		t.Fatalf("the left panel is at %s %s, want the pods of %s", app.panelScope[0], app.leftPanel.GetCurrentPath(), ns)
	}
	if item, ok := app.leftPanel.SelectedNavItem(t.Context()); ok {
		t.Fatalf("the left cursor is on %v, want ..", item)
	}
}

// assertInfoShows fails unless the right panel's Info summarizes
// namespace ns of conn, narrowed to Deployment workload unless it is "",
// under a frame that names conn and ns.
func assertInfoShows(t *testing.T, app *App, conn *panelConn, ns, workload string) {
	t.Helper()
	target := app.infoTarget(t.Context(), app.rightPanel)
	if !strings.HasPrefix(target.Key, conn.connID()+"|") || !strings.HasSuffix(target.Key, "|"+ns) {
		t.Fatalf("Info summarizes %q, want namespace %s of connection %q", target.Key, ns, conn.connID())
	}
	switch w := target.Workload; {
	case workload == "" && w != nil:
		t.Fatalf("Info shows workload %s/%s, want the namespace %s", w.Namespace, w.Name, ns)
	case workload != "" && (w == nil || w.GVR.Resource != "deployments" || w.Namespace != ns || w.Name != workload):
		t.Fatalf("Info shows workload %+v, want deployment %s/%s", w, ns, workload)
	}
	assertFrameNames(t, app, conn, ns)
}
