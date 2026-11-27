package ui

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

// With no current-context and one context, kc starts in it and says so.
// The panels connect to that context, and the notice is queued for the
// first frame.
func TestStartupOpensTheOnlyContextAndSaysSo(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	home := t.TempDir()
	kcfg := clientcmdapi.NewConfig()
	kcfg.Clusters["envtest"] = &clientcmdapi.Cluster{Server: testCfg.Host, CertificateAuthorityData: testCfg.CAData}
	kcfg.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: testCfg.CertData, ClientKeyData: testCfg.KeyData, Token: testCfg.BearerToken}
	kcfg.Contexts["only"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest", Namespace: "default"}
	kcfgPath := filepath.Join(home, "kubeconfig")
	if err := clientcmd.WriteToFile(*kcfg, kcfgPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kcfgPath)
	t.Setenv("HOME", home)

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
	for idx := range 2 {
		if got := app.ownConn(idx).name(); got != "only" {
			t.Fatalf("the %s panel starts in %q, want only", panelName(idx), got)
		}
	}

	want := "No current-context set; opened the only context, only"
	if got := pendingInfoToasts(t, app, want); !slices.Contains(got, want) {
		t.Fatalf("startup notices %q, want %q among them", got, want)
	}
}

// pendingInfoToasts runs the commands queued for the first Update and
// returns the texts of the info toasts they return, until want is among
// them. Some commands wait for events (the discovery watch); they run in
// goroutines that end when the app's context is canceled, and the toasts,
// which return at once, are collected for at most a second.
func pendingInfoToasts(t *testing.T, a *App, want string) []string {
	t.Helper()
	a.pendingCmdsMu.Lock()
	cmds := slices.Clone(a.pendingCmds)
	a.pendingCmdsMu.Unlock()
	msgs := make(chan tea.Msg, len(cmds))
	for _, cmd := range cmds {
		go func() { msgs <- cmd() }()
	}
	var texts []string
	deadline := time.After(time.Second)
	for range cmds {
		select {
		case msg := <-msgs:
			if toast, ok := msg.(showToastMsg); ok && toast.info {
				texts = append(texts, toast.text)
				if toast.text == want {
					return texts
				}
			}
		case <-deadline:
			return texts
		}
	}
	return texts
}
