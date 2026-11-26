package ui

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

// The folders kc builds at startup read the configuration file. initData
// makes them before the UI starts, and a folder keeps the panel
// configuration it was given; with the file read in Init, after them, the
// start panels listed types by the defaults, so a context binding or a
// default profile from the file took effect only after a namespace
// change.
func TestStartupFoldersReadTheConfigFile(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	home := t.TempDir()
	kcfg := clientcmdapi.NewConfig()
	kcfg.CurrentContext = "envtest"
	kcfg.Clusters["envtest"] = &clientcmdapi.Cluster{Server: testCfg.Host, CertificateAuthorityData: testCfg.CAData}
	kcfg.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: testCfg.CertData, ClientKeyData: testCfg.KeyData, Token: testCfg.BearerToken}
	kcfg.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest", Namespace: "default"}
	kcfgPath := filepath.Join(home, "kubeconfig")
	if err := clientcmd.WriteToFile(*kcfg, kcfgPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kcfgPath)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".kc"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `resources:
  visibility:
    profiles:
      leases:
        mode: only
        show:
          - {group: coordination.k8s.io, resource: leases}
    contexts:
      envtest: leases
`
	if err := os.WriteFile(filepath.Join(home, ".kc", "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	t.Cleanup(func() {
		app.releasePanelConns()
		if app.clPool != nil {
			app.clPool.Stop()
		}
		app.closeKubeconfigFiles()
		app.cancel()
	})
	if err := app.initConfigAndData(t.Context()); err != nil {
		t.Fatalf("startup: %v", err)
	}
	root := app.leftNav.Current()
	root.Len(t.Context())
	source, ok := root.(models.TypeCatalogSource)
	if !ok {
		t.Fatalf("the start folder %T has no type catalog", root)
	}
	if got := source.TypeCatalog().Profile; got.Name != "leases" || got.Err != nil {
		t.Fatalf("the start panel lists under %q (err %v), want the context's profile leases", got.Name, got.Err)
	}
	if got := app.panelVisibility(app.leftPanel); got.Name != "leases" {
		t.Fatalf("the panel's settings give %q, want leases", got.Name)
	}
}
