package ui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q should name the context and the kubeconfig", err)
	}
	assertFileUnchanged(t, path, data)
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
