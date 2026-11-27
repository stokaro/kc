package kubeconfig_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/stokaro/kc/pkg/kubeconfig"
)

// kubeFile is a kubeconfig file of a test: its name, its current-context
// and the names of its contexts.
type kubeFile struct {
	name     string
	current  string
	contexts []string
}

// write writes f into dir. Every context names one cluster and user, which
// the file holds.
func (f kubeFile) write(t *testing.T, dir string) string {
	t.Helper()
	cfg := api.NewConfig()
	cfg.CurrentContext = f.current
	cfg.Clusters["c"] = &api.Cluster{Server: "https://127.0.0.1:1"}
	cfg.AuthInfos["u"] = &api.AuthInfo{Token: "t"}
	for _, name := range f.contexts {
		cfg.Contexts[name] = &api.Context{Cluster: "c", AuthInfo: "u"}
	}
	path := filepath.Join(dir, f.name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// setupEnv writes files into a temp dir and sets $KUBECONFIG to them in
// order. It returns the dir and the list.
func setupEnv(t *testing.T, files []kubeFile) (dir, list string) {
	t.Helper()
	dir = t.TempDir()
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.write(t, dir))
	}
	list = strings.Join(paths, string(os.PathListSeparator))
	t.Setenv("HOME", dir)
	t.Setenv("KUBECONFIG", list)
	return dir, list
}

// setupHome writes files into ~/.kube of a temp home (the file "config" is
// ~/.kube/config) and unsets $KUBECONFIG. It returns the ~/.kube dir.
func setupHome(t *testing.T, files []kubeFile) string {
	t.Helper()
	home := t.TempDir()
	kube := filepath.Join(home, ".kube")
	for _, f := range files {
		f.write(t, kube)
	}
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")
	return kube
}

func discover(t *testing.T) *kubeconfig.Manager {
	t.Helper()
	m := kubeconfig.NewManager()
	if err := m.DiscoverKubeconfigs(); err != nil {
		t.Fatalf("discover: %v", err)
	}
	return m
}

func contextNames(cs []*kubeconfig.Context) []string {
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, c.Name)
	}
	return names
}

// The contexts of a file come from a map. GetContexts returns them sorted,
// so the order is the same on every run; before, it changed from run to
// run, and so did the context kc fell back to.
func TestGetContextsOrderIsStable(t *testing.T) {
	names := make([]string, 0, 16)
	for i := range 16 {
		names = append(names, fmt.Sprintf("ctx-%02d", (i*7)%16))
	}
	want := slices.Sorted(slices.Values(names))
	file := kubeFile{name: "config", current: "ctx-03", contexts: names}

	t.Run("one file", func(t *testing.T) {
		setupEnv(t, []kubeFile{file})
		for run := range 20 {
			m := discover(t)
			if got := contextNames(m.GetContexts()); !slices.Equal(got, want) {
				t.Fatalf("run %d: GetContexts() = %v, want %v", run, got, want)
			}
			if got := m.ContextNames(); !slices.Equal(got, want) {
				t.Fatalf("run %d: ContextNames() = %v, want %v", run, got, want)
			}
		}
	})

	// The files under ~/.kube load in parallel; they are kept in the
	// walk's order, ~/.kube/config first, and a name that several of them
	// define is listed once per file, in that order.
	t.Run("files under ~/.kube", func(t *testing.T) {
		files := []kubeFile{
			{name: "zeta.yaml", contexts: []string{"shared", "zeta"}},
			{name: "config", contexts: []string{"shared", "main"}},
			{name: "team/inner.yaml", contexts: []string{"inner", "shared"}},
			{name: "alpha.yaml", contexts: []string{"alpha", "shared"}},
			{name: "beta.yaml", contexts: []string{"beta"}},
		}
		wantFiles := []string{"config", "alpha.yaml", "beta.yaml", "team/inner.yaml", "zeta.yaml"}
		wantContexts := []string{"alpha", "beta", "inner", "main", "shared", "shared", "shared", "shared", "zeta"}
		wantShared := []string{"config", "alpha.yaml", "team/inner.yaml", "zeta.yaml"}
		kube := setupHome(t, files)
		rel := func(path string) string {
			r, err := filepath.Rel(kube, path)
			if err != nil {
				t.Fatal(err)
			}
			return filepath.ToSlash(r)
		}
		for run := range 20 {
			m := discover(t)
			var gotFiles []string
			for _, kc := range m.GetKubeconfigs() {
				gotFiles = append(gotFiles, rel(kc.Path))
			}
			if !slices.Equal(gotFiles, wantFiles) {
				t.Fatalf("run %d: kubeconfigs %v, want %v", run, gotFiles, wantFiles)
			}
			if got := contextNames(m.GetContexts()); !slices.Equal(got, wantContexts) {
				t.Fatalf("run %d: GetContexts() = %v, want %v", run, got, wantContexts)
			}
			var gotShared []string
			for _, c := range m.GetContexts() {
				if c.Name == "shared" {
					gotShared = append(gotShared, rel(c.Kubeconfig.Path))
				}
			}
			if !slices.Equal(gotShared, wantShared) {
				t.Fatalf("run %d: shared comes from %v, want %v", run, gotShared, wantShared)
			}
			if got := rel(m.GetContextByName("shared").Kubeconfig.Path); got != "config" {
				t.Fatalf("run %d: GetContextByName(shared) is the one of %s, want config", run, got)
			}
			if got, want := m.ContextNames(), []string{"alpha", "beta", "inner", "main", "shared", "zeta"}; !slices.Equal(got, want) {
				t.Fatalf("run %d: ContextNames() = %v, want %v", run, got, want)
			}
		}
	})
}
