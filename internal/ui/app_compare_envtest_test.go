package ui

import (
	"strings"
	"testing"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

// C-x C-d reads the two manifests with one GET each. On a local API
// server that takes well under a second, also right after discovery was
// refreshed, which the cluster does every 30 seconds.
func TestCompareObjectsReadsBothManifestsFast(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	go cl.Start(ctx)
	createConfigMaps(t, testCfg, "cmp-objects", map[string]string{"left": "1", "right": "2"})
	deps := models.Deps{
		Cl: cl, Ctx: ctx, CtxName: "envtest",
		KubeConfig: clientcmdapi.Config{CurrentContext: "envtest", Contexts: map[string]*clientcmdapi.Context{"envtest": {Namespace: "cmp-objects"}}},
		AppConfig:  appconfig.Default(),
	}
	folder := models.NewNamespacedObjectsFolder(deps, configMapsGVR, "cmp-objects", []string{"namespaces", "cmp-objects", "configmaps"}, nil)
	t.Cleanup(folder.Close)
	sources := map[string]models.ManifestSource{}
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		for _, r := range folder.Lines(ctx, 0, folder.Len(ctx)) {
			if src, ok := r.(models.ManifestSource); ok {
				sources[src.Name()] = src
			}
		}
		return sources["left"] != nil && sources["right"] != nil
	})

	a := &App{ctx: ctx}
	for _, step := range []string{"warm", "after a discovery refresh"} {
		if step != "warm" {
			cl.RefreshDiscovery()
		}
		start := time.Now()
		msg, ok := a.readComparison(nil, 1, sources["left"], sources["right"], "envtest", manifestdiff.Options{})().(compareResultMsg)
		took := time.Since(start)
		if !ok || msg.err != nil {
			t.Fatalf("%s: compare = %#v", step, msg)
		}
		if !strings.Contains(msg.result.Text, "name: left") || !strings.Contains(msg.result.Text, "name: right") {
			t.Fatalf("%s: the diff misses the data:\n%s", step, msg.result.Text)
		}
		t.Logf("%s: read and compared both manifests in %v", step, took)
		if took > time.Second {
			t.Errorf("%s: reading two manifests took %v, want well under a second", step, took)
		}
	}
}
