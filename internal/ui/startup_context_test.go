package ui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

const startupKubeconfig = `apiVersion: v1
kind: Config
current-context: dev
clusters:
- name: c1
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: dev
  context:
    cluster: c1
    user: u1
    namespace: dev-ns
- name: staging
  context:
    cluster: c1
    user: u1
    namespace: staging-ns
users:
- name: u1
  user:
    token: t
`

// writeStartupKubeconfig points KUBECONFIG at a temp kubeconfig and returns
// its path and content.
func writeStartupKubeconfig(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	data := []byte(startupKubeconfig)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", path)
	// Run reads ~/.kc/config.yaml before it reads the kubeconfig.
	t.Setenv("HOME", filepath.Dir(path))
	return path, data
}

func assertFileUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("kubeconfig changed:\n%s", got)
	}
}

// TestRunUnknownContextFailsBeforeUI checks that Run returns
// ErrUnknownContext from initData, before it connects or starts the
// Bubble Tea program (which would block on the test's stdin).
func TestRunUnknownContextFailsBeforeUI(t *testing.T) {
	path, data := writeStartupKubeconfig(t)
	err := Run(t.Context(), RunConfig{Context: "nope"})
	if !errors.Is(err, ErrUnknownContext) {
		t.Fatalf("Run error = %v, want ErrUnknownContext", err)
	}
	if want := `unknown context "nope" in ` + path + `; contexts: dev, staging`; err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
	assertFileUnchanged(t, path, data)
}

// kubeFile is a kubeconfig file of a test: its name, its current-context
// and the names of its contexts.
type kubeFile struct {
	name     string
	current  string
	contexts []string
}

// writeKubeFiles writes files into dir and returns their paths.
func writeKubeFiles(t *testing.T, dir string, files ...kubeFile) []string {
	t.Helper()
	paths := make([]string, 0, len(files))
	for _, f := range files {
		cfg := clientcmdapi.NewConfig()
		cfg.CurrentContext = f.current
		cfg.Clusters["c"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1"}
		cfg.AuthInfos["u"] = &clientcmdapi.AuthInfo{Token: "t"}
		for _, name := range f.contexts {
			cfg.Contexts[name] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "u"}
		}
		path := filepath.Join(dir, f.name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := clientcmd.WriteToFile(*cfg, path); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

// A current-context that names a context the kubeconfig does not define
// stops kc before the UI, as kubectl refuses it. Before, kc opened one of
// the other contexts, a different one from run to run.
func TestRunUndefinedCurrentContextFailsBeforeUI(t *testing.T) {
	dir := t.TempDir()
	paths := writeKubeFiles(t, dir, kubeFile{name: "config", current: "typo-context", contexts: []string{"good", "refused", "badtoken", "limited"}})
	t.Setenv("KUBECONFIG", paths[0])
	t.Setenv("HOME", dir)
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}

	err = Run(t.Context(), RunConfig{})
	if !errors.Is(err, ErrUndefinedCurrentContext) {
		t.Fatalf("Run error = %v, want ErrUndefinedCurrentContext", err)
	}
	want := `current-context "typo-context" in ` + paths[0] + ` is not defined; contexts: badtoken, good, limited, refused; pick one with --context`
	if err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
	assertFileUnchanged(t, paths[0], data)
}

func TestSelectCurrentContext(t *testing.T) {
	tests := []struct {
		name  string
		home  bool // files under ~/.kube, else $KUBECONFIG in this order
		files []kubeFile
		// want is the context selected, only whether it is the only one.
		want string
		only bool
		// wantErr is the error kind; wantMsg its message, with %[1]s for
		// the dir of the files.
		wantErr error
		wantMsg string
	}{
		{
			name:  "defined",
			files: []kubeFile{{name: "config", current: "dev", contexts: []string{"dev", "prod"}}},
			want:  "dev",
		},
		{
			name:    "not defined",
			files:   []kubeFile{{name: "config", current: "typo", contexts: []string{"prod", "dev"}}},
			wantErr: ErrUndefinedCurrentContext,
			wantMsg: `current-context "typo" in %[1]s/config is not defined; contexts: dev, prod; pick one with --context`,
		},
		{
			name: "not defined in the first file of the list that names one",
			files: []kubeFile{
				{name: "a", current: "typo", contexts: []string{"a1"}},
				{name: "b", current: "b1", contexts: []string{"b1"}},
			},
			wantErr: ErrUndefinedCurrentContext,
			wantMsg: `current-context "typo" in %[1]s/a is not defined; contexts: a1, b1; pick one with --context`,
		},
		{
			name: "defined only in another file under ~/.kube",
			home: true,
			files: []kubeFile{
				{name: "config", current: "prod", contexts: []string{"dev"}},
				{name: "old.yaml", contexts: []string{"prod"}},
			},
			wantErr: ErrUndefinedCurrentContext,
			wantMsg: `current-context "prod" in %[1]s/config is not defined there, only in %[1]s/old.yaml; contexts: dev, prod; pick one with --context`,
		},
		{
			name:    "not defined and no contexts",
			files:   []kubeFile{{name: "config", current: "gone"}},
			wantErr: ErrUndefinedCurrentContext,
			wantMsg: `current-context "gone" in %[1]s/config is not defined; the kubeconfig defines no contexts`,
		},
		{
			name: "none named, several contexts",
			files: []kubeFile{
				{name: "a", contexts: []string{"two", "one"}},
				{name: "b", contexts: []string{"three"}},
			},
			wantErr: ErrNoCurrentContext,
			wantMsg: `no current-context is set in %[1]s/a` + string(os.PathListSeparator) + `%[1]s/b; contexts: one, three, two; pick one with --context`,
		},
		{
			name: "none named, one name in two files under ~/.kube",
			home: true,
			files: []kubeFile{
				{name: "config", contexts: []string{"kind"}},
				{name: "copy.yaml", contexts: []string{"kind"}},
			},
			wantErr: ErrNoCurrentContext,
			wantMsg: `no current-context is set in %[1]s/config` + string(os.PathListSeparator) + `%[1]s/copy.yaml; contexts: kind; pick one with --context`,
		},
		{
			name:  "none named, one context",
			files: []kubeFile{{name: "config", contexts: []string{"only"}}},
			want:  "only",
			only:  true,
		},
		{
			name:  "none named, no contexts",
			files: []kubeFile{{name: "config"}},
		},
		{
			name:    "names that need quotes",
			files:   []kubeFile{{name: "config", current: "typo", contexts: []string{"ok", "a b", "evil\x1b[2J", "x,y"}}},
			wantErr: ErrUndefinedCurrentContext,
			wantMsg: `current-context "typo" in %[1]s/config is not defined; contexts: "a b", "evil\x1b[2J", ok, "x,y"; pick one with --context`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := home
			if tc.home {
				dir = filepath.Join(home, ".kube")
			}
			paths := writeKubeFiles(t, dir, tc.files...)
			t.Setenv("HOME", home)
			if tc.home {
				t.Setenv("KUBECONFIG", "")
			} else {
				t.Setenv("KUBECONFIG", strings.Join(paths, string(os.PathListSeparator)))
			}
			app := NewApp()
			app.kubeMgr = kubeconfig.NewManager()
			if err := app.kubeMgr.DiscoverKubeconfigs(); err != nil {
				t.Fatalf("discover: %v", err)
			}

			c, only, err := app.selectCurrentContext()
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error %v, want %v", err, tc.wantErr)
				}
				if want := fmt.Sprintf(tc.wantMsg, dir); err.Error() != want {
					t.Fatalf("error %q,\nwant  %q", err, want)
				}
				if c != nil {
					t.Fatalf("context %s with the error", c.Name)
				}
				return
			}
			if err != nil {
				t.Fatalf("error %v", err)
			}
			got := ""
			if c != nil {
				got = c.Name
			}
			if got != tc.want || only != tc.only {
				t.Fatalf("context %q (only %v), want %q (only %v)", got, only, tc.want, tc.only)
			}
		})
	}
}

func TestStartupContext(t *testing.T) {
	path, data := writeStartupKubeconfig(t)
	app := NewApp()
	app.kubeMgr = kubeconfig.NewManager()
	if err := app.kubeMgr.DiscoverKubeconfigs(); err != nil {
		t.Fatalf("discover: %v", err)
	}
	c, err := app.startupContext("staging")
	if err != nil {
		t.Fatalf("startupContext: %v", err)
	}
	if c.Name != "staging" || c.Namespace != "staging-ns" {
		t.Fatalf("context = %s/%s, want staging/staging-ns", c.Name, c.Namespace)
	}
	if _, err := app.startupContext("nope"); !errors.Is(err, ErrUnknownContext) {
		t.Fatalf("unknown context error = %v", err)
	}
	// The current-context in the file stays dev.
	assertFileUnchanged(t, path, data)
}

func TestInitialNamespaceAllNamespaces(t *testing.T) {
	tcs := []struct {
		name     string
		ctxNs    string
		override string
		all      bool
		want     string
	}{
		{name: "all namespaces", ctxNs: "dev", all: true, want: models.AllNamespaces},
		{name: "all namespaces wins over -n", ctxNs: "dev", override: "prod", all: true, want: models.AllNamespaces},
		{name: "-n without -A", ctxNs: "dev", override: "prod", want: "prod"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			app := NewApp()
			useTestCluster(app, &kubeconfig.Context{Name: "ctx", Namespace: tc.ctxNs}, nil)
			app.namespaceOverride = tc.override
			app.allNamespaces = tc.all
			if got := app.initialNamespace(); got != tc.want {
				t.Fatalf("initialNamespace() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStartupIntentNamespace(t *testing.T) {
	tcs := []struct {
		name     string
		ctxNs    string
		intentNs string
		all      bool
		want     string
	}{
		{name: "all namespaces", ctxNs: "dev", all: true, want: models.AllNamespaces},
		{name: "intent namespace", ctxNs: "dev", intentNs: "prod", want: "prod"},
		{name: "context namespace", ctxNs: "dev", want: "dev"},
		{name: "default", want: "default"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			app := NewApp()
			useTestCluster(app, &kubeconfig.Context{Name: "ctx", Namespace: tc.ctxNs}, nil)
			app.allNamespaces = tc.all
			app.startupIntent = StartupIntent{Verb: KubectlVerbGet, Namespace: tc.intentNs}
			if got := app.startupIntentNamespace(); got != tc.want {
				t.Fatalf("startupIntentNamespace() = %q, want %q", got, tc.want)
			}
		})
	}
}
