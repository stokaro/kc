package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

var jobsGVR = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}

// gvrFolder is an object list as setPanelScope sees it.
type gvrFolder struct {
	*models.BaseFolder
	gvr       schema.GroupVersionResource
	namespace string
}

func (f *gvrFolder) GVR() schema.GroupVersionResource { return f.gvr }
func (f *gvrFolder) Namespace() string                { return f.namespace }

// newHiddenTypeApp returns an App on the envtest cluster whose panels list
// namespace nsA and show non-empty resource types only, plus a second
// namespace nsB. Jobs has no objects in either.
func newHiddenTypeApp(t *testing.T) (app *App, nsA, nsB string) {
	t.Helper()
	kcenvtest.Require(t, testCfg)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	ctx := t.Context()
	kctesting.RunInBackground(t, "cluster", cl.Start)
	t.Cleanup(cl.Stop)

	nsA, nsB = kctesting.UniqueName("hidden-a"), kctesting.UniqueName("hidden-b")
	for _, name := range []string{nsA, nsB} {
		ns := &corev1.Namespace{}
		ns.Name = name
		if err := cl.GetClient().Create(ctx, ns); err != nil {
			t.Fatalf("create namespace %s: %v", name, err)
		}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
			defer cancel()
			_ = cl.GetClient().Delete(cleanupCtx, ns)
		})
	}

	app = NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	app.cfg = appconfig.Default()
	app.leftConfig, app.rightConfig = nil, nil
	app.leftPanel.SetResourceViewOptions(true, string(appconfig.OrderAlpha))
	useTestCluster(app, &kubeconfig.Context{Name: "env", Namespace: nsA}, cl)
	app.goToNamespace(nsA)

	root := app.leftNav.Current()
	if !app.ensurePanelConfig(app.leftPanel).Resources.ShowNonEmptyOnly {
		t.Fatalf("the panel shows empty resource types; the test needs them hidden")
	}
	jobsID := resourceRowID(nsA, jobsGVR, true)
	root.Len(ctx)
	if _, listed := root.ItemByID(ctx, jobsID); listed {
		t.Fatalf("the root lists the empty type %s", jobsID)
	}
	return app, nsA, nsB
}

// atList fails unless panel 0 shows the object list of gvr in ns.
func atList(t *testing.T, app *App, gvr schema.GroupVersionResource, ns string) {
	t.Helper()
	cur, ok := app.leftNav.Current().(interface {
		GVR() schema.GroupVersionResource
		Namespace() string
	})
	if !ok || cur.GVR() != gvr || cur.Namespace() != ns {
		t.Fatalf("panel is at %v (%T), want the %s list of %s", app.leftNav.Current().Path(), app.leftNav.Current(), gvr, ns)
	}
}

func TestJumpOpensHiddenType(t *testing.T) {
	app, nsA, _ := newHiddenTypeApp(t)
	jobsID := resourceRowID(nsA, jobsGVR, true)
	loc := navui.Location{Context: "env", Namespace: nsA, Steps: []string{jobsID}, Path: "/jobs"}

	start := time.Now()
	runJump(t, app, app.jumpTo(0, loc, true))
	atList(t, app, jobsGVR, nsA)
	// The jump enters at once; it does not wait for the hidden row to be
	// listed.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the jump took %v", elapsed)
	}

	// Back at the root, the type stays hidden: the jump did not change the
	// filter.
	runJump(t, app, app.historyBack())
	if app.leftNav.HasBack() {
		t.Fatalf("history back is at %v, want the root", app.leftNav.Current().Path())
	}
	root := app.leftNav.Current()
	root.Len(t.Context())
	if _, listed := root.ItemByID(t.Context(), jobsID); listed {
		t.Fatalf("the root lists %s after the jump", jobsID)
	}
	runJump(t, app, app.historyForward())
	atList(t, app, jobsGVR, nsA)
}

func TestNamespaceChangeKeepsHiddenType(t *testing.T) {
	app, nsA, nsB := newHiddenTypeApp(t)
	runJump(t, app, app.jumpTo(0, navui.Location{Context: "env", Namespace: nsA, Steps: []string{resourceRowID(nsA, jobsGVR, true)}}, true))
	atList(t, app, jobsGVR, nsA)

	if cmd := app.setPanelScope(0, nsB); cmd != nil {
		t.Fatalf("setPanelScope reported %v", cmd())
	}
	atList(t, app, jobsGVR, nsB)
}

func TestNamespaceChangeReportsUnservedType(t *testing.T) {
	app, nsA, nsB := newHiddenTypeApp(t)
	widgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	nav := app.leftNav
	nav.Push(&gvrFolder{BaseFolder: modeltesting.NewStaticFolder("widgets", nil, nil), gvr: widgets, namespace: nsA})

	cmd := app.setPanelScope(0, nsB)
	if cmd == nil {
		t.Fatalf("setPanelScope reported nothing for a type the cluster does not serve")
	}
	msg, ok := cmd().(showToastMsg)
	if !ok || !strings.Contains(msg.text, "widgets") || !strings.Contains(msg.text, "no longer serves") {
		t.Fatalf("toast = %#v, want one that names widgets", msg)
	}
	if app.leftNav.HasBack() {
		t.Fatalf("panel is at %v, want the root of %s", app.leftNav.Current().Path(), nsB)
	}
	if app.panelScope[0] != nsB {
		t.Fatalf("scope = %q, want %q", app.panelScope[0], nsB)
	}
}
