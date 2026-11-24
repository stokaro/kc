package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// splitEnvtestKubeconfig writes the envtest cluster and user, with a
// context "base", to one file, and a context "split" that names them to a
// second file. The second file also has a "base" of its own, pointing
// nowhere, that the first file shadows. It sets KUBECONFIG to both and
// returns the two paths.
func splitEnvtestKubeconfig(t *testing.T) (first, second string) {
	t.Helper()
	cfg := testCfg
	base := clientcmdapi.NewConfig()
	base.CurrentContext = "base"
	base.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	base.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
	base.Contexts["base"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest"}
	split := clientcmdapi.NewConfig()
	split.Clusters["nowhere"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1"}
	split.Contexts["split"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest", Namespace: "kube-system"}
	split.Contexts["base"] = &clientcmdapi.Context{Cluster: "nowhere", AuthInfo: "envtest"}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "base.yaml"), filepath.Join(dir, "split.yaml")
	for path, c := range map[string]*clientcmdapi.Config{a: base, b: split} {
		if err := clientcmd.WriteToFile(*c, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KUBECONFIG", a+string(os.PathListSeparator)+b)
	t.Setenv("HOME", dir)
	return a, b
}

// A context whose cluster and user live in another file of $KUBECONFIG
// connects, as it does for kubectl. Before, kc loaded the context's own
// file only and failed with "invalid configuration".
func TestSwitchToContextSplitAcrossKubeconfigs(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	a, b := splitEnvtestKubeconfig(t)
	mgr := kubeconfig.NewManager()
	if err := mgr.DiscoverKubeconfigs(); err != nil {
		t.Fatal(err)
	}
	pool := kccluster.NewPool(time.Minute, 0)
	pool.Start()
	t.Cleanup(pool.Stop)
	app := &App{ctx: t.Context(), kubeMgr: mgr, clPool: pool}

	for _, name := range []string{"split", "base"} {
		t.Run(name, func(t *testing.T) {
			target := mgr.GetContextByName(name)
			if target == nil {
				t.Fatalf("context %q not discovered", name)
			}
			var got *contextSwitchedMsg
			for _, msg := range flattenCmd(app.switchContext(target)) {
				if m, ok := msg.(contextSwitchedMsg); ok {
					got = &m
				} else if cmd, ok := msg.(tea.Cmd); ok {
					if m, ok := cmd().(contextSwitchedMsg); ok {
						got = &m
					}
				}
			}
			if got == nil {
				t.Fatal("no contextSwitchedMsg")
			}
			if got.err != nil {
				t.Fatalf("switch to %s: %v", name, got.err)
			}
			t.Cleanup(got.lease.Release)
			if len(got.infos) == 0 {
				t.Error("no resource types read from the cluster")
			}
		})
	}

	// The picker lists "base" once: the first file's, as kubectl uses it.
	var names []string
	for _, c := range mgr.GetContexts() {
		names = append(names, c.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"base", "split"}) {
		t.Errorf("contexts %v, want base and split once each", names)
	}
	if c := mgr.GetContextByName("split"); c.Kubeconfig.Path != b {
		t.Errorf("split is held by %s, want %s", c.Kubeconfig.Path, b)
	}
	if c := mgr.GetContextByName("base"); c.Kubeconfig.Path != a {
		t.Errorf("base is held by %s, want %s", c.Kubeconfig.Path, a)
	}

	// kubectl started by kc loads the same merged list.
	app.currentCtx = mgr.GetContextByName("split")
	cmd, line := kubectlEditCommand(app.execContext("kube-system"), "configmap/x")
	if strings.Contains(line, "--kubeconfig") {
		t.Errorf("kubectl edit names one file: %s", line)
	}
	if want := "KUBECONFIG=" + a + string(os.PathListSeparator) + b; !slices.Contains(cmd.Env, want) {
		t.Errorf("kubectl edit env lacks %s", want)
	}
}

// Shell and attach run kubectl with a merged list in KUBECONFIG, since
// --kubeconfig takes one file.
func TestExecSessionKubeconfigList(t *testing.T) {
	list := "/k/a" + string(os.PathListSeparator) + "/k/b"
	target := models.ExecTarget{Cluster: kccluster.Key{KubeconfigPath: list, ContextName: "split"}, Namespace: "ns", Pod: "p", Container: "c"}
	s := newExecSession(target, models.ExecShell)
	if s.kubeconfig != list || slices.Contains(s.args, "--kubeconfig") {
		t.Errorf("kubeconfig %q, args %q", s.kubeconfig, s.args)
	}
}
