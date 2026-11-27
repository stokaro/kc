package ui

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	navui "github.com/stokaro/kc/internal/navigation"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// markedDeleteApp starts kc on the package control plane in context
// envtest, with the ConfigMaps of a new namespace ns open in the left
// panel: ux-a, ux-b, ux-c and web-config.
func markedDeleteApp(t *testing.T) (*App, crclient.Client, string, map[string]*corev1.ConfigMap) {
	t.Helper()
	kcenvtest.Require(t, testCfg)
	ns := kctesting.UniqueName("delete-marked")
	ctx := t.Context()
	c, err := crclient.New(testCfg, crclient.Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	nsObj := &corev1.Namespace{}
	nsObj.Name = ns
	if err := c.Create(ctx, nsObj); err != nil {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_ = c.Delete(cleanupCtx, nsObj)
	})
	objs := make(map[string]*corev1.ConfigMap)
	for _, name := range []string{"ux-a", "ux-b", "ux-c", "web-config"} {
		objs[name] = createConfigMap(t, c, ns, name)
	}

	// The pool builds clusters from kubeconfig files; never let it fall
	// back to the default kubeconfig.
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
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	app.width, app.height = 120, 30
	model, _ := app.Update(tea.WindowSizeMsg{Width: app.width, Height: app.height})
	app = model.(*App)
	app.clPool = kccluster.NewPool(time.Minute, 0)
	app.clPool.Start()
	t.Cleanup(app.clPool.Stop)
	usePoolContext(t, app, &kubeconfig.Context{Name: "envtest", Namespace: ns, Kubeconfig: &kubeconfig.Kubeconfig{Path: path, Context: "envtest"}})
	app.goToNamespace(ns)

	nav := app.navigatorFor(0)
	typeID := resourceRowID(ns, gvrConfigMaps, true)
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		_, err := navui.GoTo(ctx, nav, []navui.GoToStep{{SelectionID: typeID, Enter: true}})
		return err == nil
	}, "the root never listed configmaps in "+ns)
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		for name := range objs {
			if _, ok := nav.Current().ItemByID(ctx, name); !ok {
				return false
			}
		}
		return true
	}, "the configmaps folder never listed the four configmaps")
	app.syncPanelWithNavigator(0)
	app.setActivePanel(0, "test")
	return app, c, ns, objs
}

// confirmDelete answers the open delete dialog with M-y and returns the
// result the delete sends.
func confirmDelete(t *testing.T, app *App) deleteSetResultMsg {
	t.Helper()
	for _, msg := range pressApp(app, tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt}) {
		answer, ok := msg.(DeleteConfirmMsg)
		if !ok {
			continue
		}
		for _, res := range sendApp(app, answer) {
			if r, ok := res.(deleteSetResultMsg); ok {
				return r
			}
		}
	}
	t.Fatal("M-y in the delete dialog sent no delete of the marked set")
	return deleteSetResultMsg{}
}

// F8 on a marked set in one namespace deletes the marked objects only, each
// with its own UID precondition, and reports the ones it could not delete:
// one deleted meanwhile and one replaced by a new object of the same name,
// which stays (#373).
func TestDeleteMarkedSetEnvtest(t *testing.T) {
	app, c, ns, objs := markedDeleteApp(t)
	ctx := t.Context()

	// As in the issue: mark ux-* with + and move the cursor to an unmarked row.
	if cmd := app.markGroup(0, "ux-*", true); cmd != nil {
		t.Fatalf("markGroup said %v", collect(cmd))
	}
	pressApp(app, keyEnd)
	if got := markedRowIDs(app); !slices.Equal(got, []string{"ux-a", "ux-b", "ux-c"}) {
		t.Fatalf("marked %v, want ux-a, ux-b and ux-c", got)
	}
	if got := cursorObject(t, app); got != "web-config" {
		t.Fatalf("cursor on %q, want web-config", got)
	}

	pressApp(app, keyF8)
	d := app.deleteConfirm
	wantNames := []string{ns + "/ux-a", ns + "/ux-b", ns + "/ux-c"}
	if app.modalManager.TopName() != "delete_confirm" || d.title != "Delete 3 marked configmaps?" || !slices.Equal(d.names, wantNames) || d.context != "envtest" {
		t.Fatalf("dialog %q: %q %v in %q, want the three marked configmaps in envtest", app.modalManager.TopName(), d.title, d.names, d.context)
	}

	// Another client works while the dialog is open.
	if err := c.Delete(ctx, objs["ux-b"]); err != nil {
		t.Fatalf("delete ux-b: %v", err)
	}
	if err := c.Delete(ctx, objs["ux-c"]); err != nil {
		t.Fatalf("delete ux-c: %v", err)
	}
	replacement := createConfigMap(t, c, ns, "ux-c")

	res := confirmDelete(t, app)
	if len(res.results) != 3 {
		t.Fatalf("results %+v, want one per marked object", res.results)
	}
	for i, want := range []func(error) bool{
		func(err error) bool { return err == nil },
		apierrors.IsNotFound,
		apierrors.IsConflict,
	} {
		if r := res.results[i]; !want(r.err) {
			t.Fatalf("result %d for %s: %v", i, r.target.name, r.err)
		}
	}

	key := func(name string) crclient.ObjectKey { return crclient.ObjectKey{Namespace: ns, Name: name} }
	if err := c.Get(ctx, key("ux-a"), &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ux-a after the delete: %v, want NotFound", err)
	}
	for name, uid := range map[string]any{"ux-c": replacement.UID, "web-config": objs["web-config"].UID} {
		var got corev1.ConfigMap
		if err := c.Get(ctx, key(name), &got); err != nil || got.UID != uid {
			t.Fatalf("%s = uid %s (%v), want the untouched %s", name, got.UID, err, uid)
		}
	}

	msgs := sendApp(app, res)
	want := "Deleted 1 of 3 configmaps; not deleted: " + ns + "/ux-b (already gone), " + ns + "/ux-c (replaced by a new object)"
	var toasts []string
	for _, msg := range msgs {
		if toast, ok := msg.(showToastMsg); ok {
			toasts = append(toasts, toast.text)
		}
	}
	if !slices.Equal(toasts, []string{want}) {
		t.Fatalf("result line %q, want %q", toasts, want)
	}
	// The replacement carries the name of a marked row, but the user never
	// saw it: its mark goes with the rest, so the next F8 cannot take it.
	if got := markedRowIDs(app); len(got) != 0 {
		t.Fatalf("still marked after the delete: %v", got)
	}
}
