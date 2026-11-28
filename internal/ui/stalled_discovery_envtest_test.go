package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

// A panel and the command line draw from what the folder and the cluster
// already hold. Before, a discovery refresh emptied the RESTMapper's cache
// and the command line asked the mapper whether the type is namespaced, so
// with the API server stalled after a refresh the next frame waited for
// the server.
func TestStalledDiscoveryRefreshDoesNotBlockDrawing(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	proxy := kctesting.NewStallProxy(t, testCfg)
	cl, err := kccluster.New(proxy.Config())
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(cl.Stop)
	ctx := t.Context()
	kctesting.RunInBackground(t, "cluster", cl.Start)
	ns := kctesting.UniqueName("stalled-discovery")
	createConfigMaps(t, testCfg, ns, map[string]string{"one": "1"})
	deps := models.Deps{
		Cl: cl, Ctx: ctx, CtxName: "envtest",
		KubeConfig: clientcmdapi.Config{CurrentContext: "envtest", Contexts: map[string]*clientcmdapi.Context{"envtest": {Namespace: ns}}},
		AppConfig:  appconfig.Default(),
	}
	folder := models.NewNamespacedObjectsFolder(deps, configMapsGVR, ns, []string{"namespaces", ns, "configmaps"}, nil)
	t.Cleanup(folder.Close)
	kctesting.Eventually(t, 20*time.Second, 50*time.Millisecond, func() bool {
		for _, r := range folder.Lines(ctx, 0, folder.Len(ctx)) {
			if id, _, _, ok := r.Columns(); ok && strings.HasSuffix(id, "one") {
				return true
			}
		}
		return false
	}, "the config map list never showed config map one")

	p := NewPanel("")
	p.UseFolder(ctx, true)
	p.SetFolder(ctx, folder, true)
	a := &App{ctx: ctx, leftPanel: p, width: 120}
	_ = p.Render(ctx, 100, 20, true)
	_ = a.renderCommandLine()

	proxy.Stall(kctesting.DiscoveryRequest)
	refreshed := make(chan struct{})
	go func() {
		cl.RefreshDiscovery()
		close(refreshed)
	}()
	kctesting.Eventually(t, 10*time.Second, 10*time.Millisecond, func() bool {
		select {
		case <-refreshed:
			return true
		default:
			return proxy.Held() > 0
		}
	}, "the discovery refresh neither sent a request nor returned")

	draw := func(what string, fn func() string) string {
		t.Helper()
		done := make(chan string, 1)
		start := time.Now()
		go func() { done <- fn() }()
		select {
		case s := <-done:
			t.Logf("%s drawn in %v with discovery stalled", what, time.Since(start))
			return s
		case <-time.After(2 * time.Second):
			t.Fatalf("drawing the %s waited for the stalled API server (%d requests held)", what, proxy.Held())
			return ""
		}
	}
	draw("panel", func() string {
		dctx, cancel := context.WithTimeout(ctx, panelContextTimeout)
		defer cancel()
		folder.Refresh()
		p.RefreshFolder(dctx)
		return p.Render(dctx, 101, 21, true)
	})
	line := draw("command line", a.renderCommandLine)
	if want := "get configmaps -n " + ns; !strings.Contains(line, want) {
		t.Errorf("command line = %q, want it to contain %q", line, want)
	}
	proxy.Resume()
}
