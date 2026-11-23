package kubeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

func writeConfig(t *testing.T, path string, cfg *api.Config) {
	t.Helper()
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// splitConfigs writes base (the cluster, the user and a context "base")
// and extra (a context "mirror" that names base's cluster and user, and a
// "base" of its own that base shadows) into dir.
func splitConfigs(t *testing.T, dir string) (first, second string) {
	t.Helper()
	base := api.NewConfig()
	base.CurrentContext = "base"
	base.Clusters["c"] = &api.Cluster{Server: "https://base.example:6443"}
	base.AuthInfos["u"] = &api.AuthInfo{Token: "t"}
	base.Contexts["base"] = &api.Context{Cluster: "c", AuthInfo: "u"}
	extra := api.NewConfig()
	extra.Clusters["other"] = &api.Cluster{Server: "https://other.example:6443"}
	extra.Contexts["mirror"] = &api.Context{Cluster: "c", AuthInfo: "u", Namespace: "shop"}
	extra.Contexts["base"] = &api.Context{Cluster: "other", AuthInfo: "u"}
	a, b := filepath.Join(dir, "base.yaml"), filepath.Join(dir, "extra.yaml")
	writeConfig(t, a, base)
	writeConfig(t, b, extra)
	return a, b
}

// The files of $KUBECONFIG are one kubeconfig, as for kubectl: a context
// in one file may name the cluster and user of another, and a name that
// two files define comes from the first.
func TestDiscoverMergesKubeconfigEnv(t *testing.T) {
	dir := t.TempDir()
	a, b := splitConfigs(t, dir)
	list := a + string(os.PathListSeparator) + b
	t.Setenv("HOME", dir)
	t.Setenv("KUBECONFIG", list)

	m := NewManager()
	if err := m.DiscoverKubeconfigs(); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(m.GetContexts()))
	for _, c := range m.GetContexts() {
		names = append(names, c.Name)
	}
	if len(names) != 2 {
		t.Fatalf("contexts %v, want base and mirror once each", names)
	}

	mirror := m.GetContextByName("mirror")
	if mirror == nil || mirror.Kubeconfig.Path != b {
		t.Fatalf("mirror = %+v, want it held by %s", mirror, b)
	}
	if got := mirror.LoadPath(); got != list {
		t.Errorf("mirror loads %q, want %q", got, list)
	}
	cfg, err := m.CreateClientConfig(mirror)
	if err != nil {
		t.Fatalf("client config for a context whose cluster is in another file: %v", err)
	}
	if cfg.Host != "https://base.example:6443" || cfg.BearerToken != "t" {
		t.Errorf("mirror connects to %q with token %q", cfg.Host, cfg.BearerToken)
	}

	base := m.GetContextByName("base")
	if base == nil || base.Kubeconfig.Path != a {
		t.Fatalf("base = %+v, want the one in %s", base, a)
	}
	cfg, err = m.CreateClientConfig(base)
	if err != nil || cfg.Host != "https://base.example:6443" {
		t.Errorf("base connects to %v (%v), want the first file's server", cfg, err)
	}
	if mirror.LoadedConfig().Clusters["c"] == nil {
		t.Error("the merged config has no cluster c")
	}
}

// Without $KUBECONFIG, the files kc finds under ~/.kube stand alone, as
// each would with kubectl --kubeconfig: a context there cannot use an
// entry of ~/.kube/config.
func TestDiscoverKubeDirFilesStandAlone(t *testing.T) {
	home := t.TempDir()
	kube := filepath.Join(home, ".kube")
	if err := os.Mkdir(kube, 0o700); err != nil {
		t.Fatal(err)
	}
	a, b := splitConfigs(t, kube)
	if err := os.Rename(a, filepath.Join(kube, "config")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")

	m := NewManager()
	if err := m.DiscoverKubeconfigs(); err != nil {
		t.Fatal(err)
	}
	mirror := m.GetContextByName("mirror")
	if mirror == nil || mirror.LoadPath() != b {
		t.Fatalf("mirror = %+v, want it loaded from %s alone", mirror, b)
	}
	if _, err := m.CreateClientConfig(mirror); err == nil {
		t.Error("a context of a standalone file used an entry of ~/.kube/config")
	}
	bases := 0
	for _, c := range m.GetContexts() {
		if c.Name == "base" {
			bases++
		}
	}
	if bases != 2 {
		t.Errorf("base listed %d times, want once per standalone file", bases)
	}
}

func TestLoadingRules(t *testing.T) {
	if r := LoadingRules("/a"); r.ExplicitPath != "/a" || len(r.Precedence) != 0 {
		t.Errorf("one file: %+v", r)
	}
	list := strings.Join([]string{"/a", "/b"}, string(os.PathListSeparator))
	if r := LoadingRules(list); r.ExplicitPath != "" || len(r.Precedence) != 2 {
		t.Errorf("list: %+v", r)
	}
	if IsList("/a") || !IsList(list) {
		t.Error("IsList")
	}
}

// A file of $KUBECONFIG that does not exist is skipped, as kubectl skips
// it; the others still merge. A file that exists but does not parse still
// stops discovery.
func TestDiscoverSkipsMissingKubeconfigEnvFile(t *testing.T) {
	dir := t.TempDir()
	a, b := splitConfigs(t, dir)
	missing := filepath.Join(dir, "missing.yaml")
	sep := string(os.PathListSeparator)
	t.Setenv("HOME", dir)
	t.Setenv("KUBECONFIG", a+sep+missing+sep+b)

	m := NewManager()
	if err := m.DiscoverKubeconfigs(); err != nil {
		t.Fatalf("a missing file stopped discovery: %v", err)
	}
	if n := len(m.GetKubeconfigs()); n != 2 {
		t.Fatalf("%d kubeconfigs loaded, want 2", n)
	}
	mirror := m.GetContextByName("mirror")
	if mirror == nil || mirror.LoadPath() != a+sep+b {
		t.Fatalf("mirror = %+v, want it to load %s", mirror, a+sep+b)
	}
	if _, err := m.CreateClientConfig(mirror); err != nil {
		t.Errorf("mirror: %v", err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("contexts: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", a+sep+bad)
	if err := NewManager().DiscoverKubeconfigs(); err == nil {
		t.Error("a kubeconfig that does not parse was skipped")
	}
}
