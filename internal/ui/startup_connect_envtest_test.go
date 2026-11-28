package ui

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

// useEnvtestKubeconfig points KUBECONFIG and HOME at a temporary directory
// with one context, only, on the envtest control plane, and no
// current-context.
func useEnvtestKubeconfig(t *testing.T) {
	t.Helper()
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
	t.Setenv("SHELL", "/bin/sh")
}

// Run's path: the connecting screen first, then, once the start context
// answers, both panels on it. The notice that kc opened the only context
// comes with the panels, so it does not expire behind the connecting
// screen.
func TestStartupConnectsWhileTheScreenDraws(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	useEnvtestKubeconfig(t)

	app := newStartApp(t)
	if err := app.beginConfigAndStartup(t.Context()); err != nil {
		t.Fatalf("startup: %v", err)
	}
	app.Init()
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if frame := startupFrame(app); !strings.Contains(frame, "Connecting to only ("+testCfg.Host+")…") {
		t.Fatalf("first frame is not the connecting screen:\n%s", frame)
	}
	want := "No current-context set; opened the only context, only"
	app.pendingCmdsMu.Lock()
	queued := len(app.pendingCmds)
	app.pendingCmdsMu.Unlock()
	if queued != 0 {
		t.Fatalf("%d commands queued while connecting; the notice belongs to the panels", queued)
	}

	var msg startupConnectedMsg
	within(t, 30*time.Second, "connecting to envtest", func() { msg = app.connectStartup(app.startup)() })
	if err := msg.err(); err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if _, cmd := app.Update(msg); cmd == nil || app.startup != nil {
		t.Fatal("a successful connection did not open the panels")
	}
	for idx := range 2 {
		if got := app.ownConn(idx).name(); got != "only" {
			t.Errorf("the %s panel opened on %q, want only", panelName(idx), got)
		}
	}
	if got := pendingInfoToasts(t, app, want); !slices.Contains(got, want) {
		t.Errorf("notices after the panels opened: %q, want %q among them", got, want)
	}
	if frame := startupFrame(app); strings.Contains(frame, "Connecting to") {
		t.Errorf("the panels screen still shows the connecting screen:\n%s", frame)
	}
}

// The error on the connecting screen is the connect attempt's. The cluster
// retries discovery by itself meanwhile, and once a retry has succeeded,
// Enter opens the panels without the stale message that discovery failed.
func TestStartupDropsTheDiscoveryErrorARetryFixed(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	useEnvtestKubeconfig(t)
	app := newStartApp(t)
	if err := app.beginConfigAndStartup(t.Context()); err != nil {
		t.Fatalf("startup: %v", err)
	}
	app.Init()
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	var msg startupConnectedMsg
	within(t, 30*time.Second, "connecting to envtest", func() { msg = app.connectStartup(app.startup)() })
	if err := msg.err(); err != nil {
		t.Fatalf("connecting: %v", err)
	}
	// The connect attempt's read failed; the cluster's read since did not.
	msg.infos, msg.discoveryErr = nil, errors.New("the server was not ready")
	if _, cmd := app.Update(msg); cmd != nil || app.startup == nil {
		t.Fatal("a connection with a discovery error did not stay on the error screen")
	}
	if want, frame := describeError(msg.discoveryErr), startupFrame(app); !strings.Contains(frame, want) {
		t.Fatalf("the error screen lacks the discovery error %q:\n%s", want, frame)
	}
	if _, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || app.startup != nil {
		t.Fatal("Enter on the error did not open the panels")
	}
	toasts := pendingToasts(t, app)
	// The only-context notice shows that the queue was read.
	if !slices.Contains(toasts, "No current-context set; opened the only context, only") {
		t.Fatalf("toasts after opening the panels: %q, want the only-context notice among them", toasts)
	}
	if slices.ContainsFunc(toasts, isDiscoveryErrorToast) {
		t.Errorf("toasts after opening the panels: %q; the cluster has read discovery since the error", toasts)
	}
}
