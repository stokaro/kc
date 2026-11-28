package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// Issue #377: a Describe or Manifest panel follows the row under the other
// panel's cursor however the cursor got there, and its frame names the
// connection and namespace its content was read from.

var followerModes = []struct {
	name string
	mode PanelViewMode
}{{"describe", PanelModeDescribe}, {"manifest", PanelModeManifest}}

// followPods is an App on the package control plane. Its left panel lists
// the pods of namespace shop with the cursor on the first pod; the right
// panel follows it. Namespace batch holds pod worker.
type followPods struct {
	app         *App
	client      crclient.Client
	shop, batch string
}

func newFollowPods(t *testing.T, mode PanelViewMode, shopPods ...string) *followPods {
	t.Helper()
	e := newFollowEnv(t, shopPods...)
	e.openFirstRow(t, "cd pods", shopPods[0])
	settle(t, e.app, e.app.setPanelMode(t.Context(), e.app.rightPanel, mode, "test"))
	if got := assertFollowsLeft(t, e.app, mode); got != shopPods[0] {
		t.Fatalf("the follower shows %q, want %s", got, shopPods[0])
	}
	return e
}

// newFollowEnv is followPods with the left panel at the root of namespace
// shop and the right panel in the mode it starts in.
func newFollowEnv(t *testing.T, shopPods ...string) *followPods {
	t.Helper()
	kcenvtest.Require(t, testCfg)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	t.Cleanup(cl.Stop)
	ctx := t.Context()
	e := &followPods{client: cl.GetClient(), shop: kctesting.UniqueName("shop"), batch: kctesting.UniqueName("batch")}
	for _, name := range []string{e.shop, e.batch} {
		ns := &corev1.Namespace{}
		ns.Name = name
		if err := e.client.Create(ctx, ns); err != nil {
			t.Fatalf("create namespace %s: %v", name, err)
		}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer cancel()
			_ = e.client.Delete(cleanupCtx, ns)
		})
	}
	for _, name := range shopPods {
		if err := e.client.Create(ctx, testPod(e.shop, name)); err != nil {
			t.Fatalf("create pod %s/%s: %v", e.shop, name, err)
		}
	}
	if err := e.client.Create(ctx, testPod(e.batch, "worker")); err != nil {
		t.Fatalf("create pod %s/worker: %v", e.batch, err)
	}

	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	model, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app = model.(*App)
	useTestCluster(app, &kubeconfig.Context{Name: "env", Namespace: e.shop}, cl)
	app.goToNamespace(e.shop)
	app.setActivePanel(0, "test")
	e.app = app
	return e
}

// openFirstRow runs line on the shell line and puts the left cursor on
// row id, the first row after "..".
func (e *followPods) openFirstRow(t *testing.T, line, id string) {
	t.Helper()
	shellCd(t, e.app, line)
	e.waitForRow(t, id)
	sendKey(t, e.app, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := e.app.leftPanel.currentSelectionID(t.Context()); got != id {
		t.Fatalf("the left cursor is on %q, want %s", got, id)
	}
}

// waitForRow waits until the left panel lists row id.
func (e *followPods) waitForRow(t *testing.T, id string) {
	t.Helper()
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		_, cmd := e.app.Update(FolderDirtyMsg{PanelIdx: 0})
		settle(t, e.app, cmd)
		for _, it := range e.app.leftPanel.Items(t.Context()) {
			if it.Name == id {
				return true
			}
		}
		return false
	})
}

// A cd to another namespace moves the left cursor to the ".." row of the
// new list: the follower clears instead of keeping the object of the old
// namespace under the new namespace's label, and follows the next row.
func TestFollowerFollowsCdToAnotherNamespace(t *testing.T) {
	for _, fm := range followerModes {
		t.Run(fm.name, func(t *testing.T) {
			mode := fm.mode
			e := newFollowPods(t, mode, "checkout")
			app := e.app

			shellCd(t, app, "cd -n "+e.batch+" pods")
			if app.panelScope[0] != e.batch || app.leftPanel.GetCurrentPath() != "/pods" {
				t.Fatalf("the left panel is at %s %s, want the pods of %s", app.panelScope[0], app.leftPanel.GetCurrentPath(), e.batch)
			}
			if got := assertFollowsLeft(t, app, mode); got != "" {
				t.Fatalf("the follower shows %s with the left cursor on ..", got)
			}
			if text := followerText(t, app); strings.Contains(text, "checkout") {
				t.Fatalf("the follower still shows the pod of %s:\n%s", e.shop, text)
			}

			e.waitForRow(t, "worker")
			sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
			if got := assertFollowsLeft(t, app, mode); got != "worker" {
				t.Fatalf("the follower shows %q, want worker", got)
			}
		})
	}
}

// The deletion of the pod under the left cursor moves the cursor to the
// next pod; the follower shows that pod instead of a "not found" error for
// the deleted one.
func TestFollowerFollowsDeletionOfSelectedPod(t *testing.T) {
	for _, fm := range followerModes {
		t.Run(fm.name, func(t *testing.T) {
			mode := fm.mode
			e := newFollowPods(t, mode, "web-a", "web-b")
			app, ctx := e.app, t.Context()

			if err := e.client.Delete(ctx, testPod(e.shop, "web-a"), crclient.GracePeriodSeconds(0)); err != nil {
				t.Fatalf("delete pod web-a: %v", err)
			}
			kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
				_, cmd := app.Update(FolderDirtyMsg{PanelIdx: 0})
				settle(t, app, cmd)
				return app.leftPanel.currentSelectionID(ctx) == "web-b"
			})
			if got := assertFollowsLeft(t, app, mode); got != "web-b" {
				t.Fatalf("the follower shows %q, want web-b", got)
			}
			if text := followerText(t, app); strings.Contains(text, "not found") {
				t.Fatalf("the follower reads the deleted pod:\n%s", text)
			}
		})
	}
}

// A context switch of the followed panel puts it at the root of the new
// context: the follower drops the object it read from the old context, and
// its frame and content come from the new connection. b-prod is another
// server with a web of other data; a-mirror is a second context on the
// first server, so only the connection tells its web from a-dev's.
func TestFollowerFollowsContextSwitch(t *testing.T) {
	e := newTwoClusters(t)
	app, ctx := e.app, t.Context()
	// newTwoClusters set the size before its WindowSizeMsg, which then
	// changed nothing; lay the panels out for the frame checks.
	settle(t, app, tea.Batch(app.resizePanelsForCurrentLayout()...))
	app.setActivePanel(0, "test")
	e.openWeb(t, 0)
	settle(t, app, app.setPanelMode(ctx, app.rightPanel, PanelModeDescribe, "test"))
	if got := assertFollowsLeft(t, app, PanelModeDescribe); got != "web" {
		t.Fatalf("the follower shows %q, want web", got)
	}
	if text := followerText(t, app); !strings.Contains(text, "served-by-a") {
		t.Fatalf("the follower does not show a-dev's web:\n%s", text)
	}

	for _, step := range []struct {
		target       *kubeconfig.Context
		served, gone string
	}{
		{e.prod, "served-by-b", "served-by-a"},
		{e.mirror, "served-by-a", "served-by-b"},
	} {
		before := app.ownConn(0)
		_, cmd := app.Update(switchResult(t, app, []int{0}, step.target))
		settle(t, app, cmd)
		if c := app.ownConn(0); c == before || c.name() != step.target.Name {
			t.Fatalf("the left panel is in %q after the switch to %s", c.name(), step.target.Name)
		}
		assertFollowsLeft(t, app, PanelModeDescribe)
		if text := followerText(t, app); strings.Contains(text, step.gone) {
			t.Fatalf("after the switch to %s the follower shows the old context's web:\n%s", step.target.Name, text)
		}

		shellCd(t, app, "cd configmaps")
		kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
			_, cmd := app.Update(FolderDirtyMsg{PanelIdx: 0})
			settle(t, app, cmd)
			_, ok := app.leftNav.Current().ItemByID(ctx, "web")
			return ok
		})
		sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
		if got := assertFollowsLeft(t, app, PanelModeDescribe); got != "web" {
			t.Fatalf("in %s the follower shows %q, want web", step.target.Name, got)
		}
		if text := followerText(t, app); !strings.Contains(text, step.served) {
			t.Fatalf("in %s the follower does not show that context's web:\n%s", step.target.Name, text)
		}
	}
}
