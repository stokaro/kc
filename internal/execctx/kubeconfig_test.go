package execctx_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/sttts/kc/internal/execctx"
)

const fileA = `apiVersion: v1
kind: Config
current-context: ctx-a
clusters:
- name: cluster-a
  cluster:
    server: https://a.example
- name: shared
  cluster:
    server: https://shared-in-a.example
users:
- name: user-a
  user:
    token: token-a
- name: shared-user
  user:
    token: token-shared-a
contexts:
- name: ctx-a
  context:
    cluster: cluster-a
    user: user-a
    namespace: ns-a
`

const fileB = `apiVersion: v1
kind: Config
current-context: ctx-b
clusters:
- name: cluster-b
  cluster:
    server: https://b.example
- name: shared
  cluster:
    server: https://shared-in-b.example
users:
- name: user-b
  user:
    client-certificate: certs/b.crt
    client-key: certs/b.key
- name: user-exec
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: ./bin/auth
      args: ["token"]
      interactiveMode: Never
- name: shared-user
  user:
    token: token-shared-b
contexts:
- name: ctx-b
  context:
    cluster: cluster-b
    user: user-b
    namespace: ns-b
- name: ctx-exec
  context:
    cluster: cluster-b
    user: user-exec
- name: ctx-shadow
  context:
    cluster: shared
    user: shared-user
`

type fixture struct {
	dir     string
	a, b    execctx.Source
	sources []execctx.Source
	before  map[string][]byte
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pa := write("a/config", fileA)
	pb := write("b/config", fileB)
	write("b/certs/b.crt", "cert")
	write("b/certs/b.key", "key")
	write("b/bin/auth", "#!/bin/sh\n")
	load := func(p string) execctx.Source {
		cfg, err := clientcmd.LoadFromFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return execctx.Source{Path: p, Config: cfg}
	}
	f := fixture{dir: dir, a: load(pa), b: load(pb), before: map[string][]byte{}}
	f.sources = []execctx.Source{f.a, f.b}
	for _, p := range []string{pa, pb} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f.before[p] = data
	}
	return f
}

// assertSourcesUnchanged proves kc never wrote to the user's files.
func (f fixture) assertSourcesUnchanged(t *testing.T) {
	t.Helper()
	for p, want := range f.before {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s changed", p)
		}
	}
}

func (f fixture) ctx(src execctx.Source, name, ns string, imp execctx.Impersonation) execctx.Context {
	return execctx.Context{Kubeconfig: src.Path, Config: src.Config, Context: name, Namespace: ns, Impersonation: imp}
}

// resolve loads a KUBECONFIG value the way kubectl does and returns the
// client config and namespace kubectl would use.
func resolve(t *testing.T, kubeconfig string) (*rest.Config, string, string) {
	t.Helper()
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: filepath.SplitList(kubeconfig)}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	raw, err := cc.RawConfig()
	if err != nil {
		t.Fatalf("raw config: %v", err)
	}
	rc, err := cc.ClientConfig()
	if err != nil {
		t.Fatalf("client config: %v", err)
	}
	ns, _, err := cc.Namespace()
	if err != nil {
		t.Fatalf("namespace: %v", err)
	}
	return rc, ns, raw.CurrentContext
}

var allImp = execctx.Impersonation{User: "alice", UID: "uid-1", Groups: []string{"dev", "ops"}}

func TestFilesResolveLikeKubectl(t *testing.T) {
	t.Parallel()
	type want struct {
		context, host, ns, token, certFile string
		execCommand                        string
		imp                                execctx.Impersonation
	}
	tests := []struct {
		name string
		src  func(fixture) execctx.Source
		ctx  string
		ns   string
		imp  execctx.Impersonation
		want func(fixture) want
	}{
		{
			name: "context of the first file, namespace from kc",
			src:  func(f fixture) execctx.Source { return f.a },
			ctx:  "ctx-a", ns: "team",
			want: func(fixture) want {
				return want{context: "ctx-a", host: "https://a.example", ns: "team", token: "token-a"}
			},
		},
		{
			name: "context of a later file is current, not the file's current context",
			src:  func(f fixture) execctx.Source { return f.b },
			ctx:  "ctx-b", ns: "team",
			want: func(f fixture) want {
				return want{context: "ctx-b", host: "https://b.example", ns: "team",
					certFile: filepath.Join(f.dir, "b", "certs", "b.crt")}
			},
		},
		{
			name: "all namespaces keeps the context namespace and the impersonation",
			src:  func(f fixture) execctx.Source { return f.a },
			ctx:  "ctx-a", ns: "", imp: allImp,
			want: func(fixture) want {
				return want{context: "ctx-a", host: "https://a.example", ns: "ns-a", token: "token-a", imp: allImp}
			},
		},
		{
			name: "token kept under impersonation",
			src:  func(f fixture) execctx.Source { return f.a },
			ctx:  "ctx-a", ns: "team", imp: allImp,
			want: func(fixture) want {
				return want{context: "ctx-a", host: "https://a.example", ns: "team", token: "token-a", imp: allImp}
			},
		},
		{
			name: "certificate kept under impersonation, relative paths resolved",
			src:  func(f fixture) execctx.Source { return f.b },
			ctx:  "ctx-b", ns: "team", imp: allImp,
			want: func(f fixture) want {
				return want{context: "ctx-b", host: "https://b.example", ns: "team",
					certFile: filepath.Join(f.dir, "b", "certs", "b.crt"), imp: allImp}
			},
		},
		{
			name: "exec plugin kept under impersonation",
			src:  func(f fixture) execctx.Source { return f.b },
			ctx:  "ctx-exec", ns: "team", imp: allImp,
			want: func(f fixture) want {
				return want{context: "ctx-exec", host: "https://b.example", ns: "team",
					execCommand: filepath.Join(f.dir, "b", "bin", "auth"),
					imp:         allImp}
			},
		},
		{
			name: "entries shadowed by an earlier file come from the context's file",
			src:  func(f fixture) execctx.Source { return f.b },
			ctx:  "ctx-shadow", ns: "team",
			want: func(fixture) want {
				return want{context: "ctx-shadow", host: "https://shared-in-b.example", ns: "team", token: "token-shared-b"}
			},
		},
	}
	for _, mode := range []execctx.Mode{execctx.ModeOverlay, execctx.ModeCopy} {
		for _, tt := range tests {
			t.Run(string(mode)+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t)
				files, err := execctx.NewFiles(mode, f.sources)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = files.Close() })
				env, err := files.Write("shell", f.ctx(tt.src(f), tt.ctx, tt.ns, tt.imp))
				if err != nil {
					t.Fatalf("write: %v", err)
				}
				rc, ns, current := resolve(t, env)
				w := tt.want(f)
				if current != w.context {
					t.Errorf("current context = %q, want %q", current, w.context)
				}
				if rc.Host != w.host {
					t.Errorf("host = %q, want %q", rc.Host, w.host)
				}
				if ns != w.ns {
					t.Errorf("namespace = %q, want %q", ns, w.ns)
				}
				if rc.BearerToken != w.token {
					t.Errorf("token = %q, want %q", rc.BearerToken, w.token)
				}
				if rc.CertFile != w.certFile {
					t.Errorf("cert file = %q, want %q", rc.CertFile, w.certFile)
				}
				gotExec := ""
				if rc.ExecProvider != nil {
					gotExec = rc.ExecProvider.Command
				}
				if gotExec != w.execCommand {
					t.Errorf("exec command = %q, want %q", gotExec, w.execCommand)
				}
				if rc.Impersonate.UserName != w.imp.User || rc.Impersonate.UID != w.imp.UID || !slices.Equal(rc.Impersonate.Groups, w.imp.Groups) {
					t.Errorf("impersonation = %+v, want %+v", rc.Impersonate, w.imp)
				}
				f.assertSourcesUnchanged(t)
			})
		}
	}
}

func TestOverlayCopiesOnlyWhatItMust(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tests := []struct {
		name         string
		ctx          execctx.Context
		wantClusters []string
		wantUsers    []string
	}{
		{name: "plain", ctx: f.ctx(f.a, "ctx-a", "", execctx.Impersonation{})},
		{name: "impersonation copies the user", ctx: f.ctx(f.a, "ctx-a", "", allImp), wantUsers: []string{"user-a"}},
		{name: "shadowed names are copied", ctx: f.ctx(f.b, "ctx-shadow", "x", execctx.Impersonation{}), wantClusters: []string{"shared"}, wantUsers: []string{"shared-user"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := execctx.RenderOverlay(tt.ctx, f.sources)
			if err != nil {
				t.Fatal(err)
			}
			if got := sortedKeys(cfg.Clusters); !slices.Equal(got, tt.wantClusters) {
				t.Errorf("clusters = %q, want %q", got, tt.wantClusters)
			}
			if got := sortedKeys(cfg.AuthInfos); !slices.Equal(got, tt.wantUsers) {
				t.Errorf("users = %q, want %q", got, tt.wantUsers)
			}
			if got := sortedKeys(cfg.Contexts); !slices.Equal(got, []string{tt.ctx.Context}) {
				t.Errorf("contexts = %q, want only %q", got, tt.ctx.Context)
			}
		})
	}
}

func sortedKeys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	noUser := clientcmdapi.NewConfig()
	noUser.Clusters["c"] = &clientcmdapi.Cluster{Server: "https://c"}
	noUser.Contexts["ctx"] = &clientcmdapi.Context{Cluster: "c"}
	tests := []struct {
		name string
		ctx  execctx.Context
		want string
	}{
		{name: "unknown context", ctx: f.ctx(f.a, "nope", "", execctx.Impersonation{}), want: `context "nope" not found`},
		{name: "not loaded", ctx: execctx.Context{Context: "ctx-a"}, want: "not loaded"},
		{name: "impersonation without a user", ctx: execctx.Context{Config: noUser, Context: "ctx", Impersonation: allImp}, want: "no user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for name, render := range map[string]func() error{
				"copy":    func() error { _, err := execctx.RenderCopy(tt.ctx); return err },
				"overlay": func() error { _, err := execctx.RenderOverlay(tt.ctx, f.sources); return err },
			} {
				err := render()
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Errorf("%s: err = %v, want %q", name, err, tt.want)
				}
			}
		})
	}
}

func TestFilesSlotFollowsContextSwitch(t *testing.T) {
	t.Parallel()
	for _, mode := range []execctx.Mode{execctx.ModeOverlay, execctx.ModeCopy} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			files, err := execctx.NewFiles(mode, f.sources)
			if err != nil {
				t.Fatal(err)
			}
			envA, err := files.Write("shell", f.ctx(f.a, "ctx-a", "", execctx.Impersonation{}))
			if err != nil {
				t.Fatal(err)
			}
			// A shell started now keeps envA. Switching kc to a context of
			// another file must not change the value it holds.
			envB, err := files.Write("shell", f.ctx(f.b, "ctx-b", "", execctx.Impersonation{}))
			if err != nil {
				t.Fatal(err)
			}
			if envA != envB {
				t.Fatalf("KUBECONFIG changed across a context switch: %q -> %q", envA, envB)
			}
			if _, _, current := resolve(t, envA); current != "ctx-b" {
				t.Fatalf("shell resolves context %q, want ctx-b", current)
			}

			assertMode(t, files.Dir(), 0o700)
			assertMode(t, files.Path("shell"), 0o600)

			if err := files.Remove("shell"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(files.Path("shell")); !os.IsNotExist(err) {
				t.Fatalf("slot not removed: %v", err)
			}
			if err := files.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(files.Dir()); !os.IsNotExist(err) {
				t.Fatalf("dir not removed: %v", err)
			}
			f.assertSourcesUnchanged(t)
		})
	}
}

func TestFilesEnvValue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	overlay, err := execctx.NewFiles(execctx.ModeOverlay, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = overlay.Close() })
	want := strings.Join([]string{overlay.Path("x"), f.a.Path, f.b.Path}, string(os.PathListSeparator))
	if got := overlay.EnvValue("x"); got != want {
		t.Fatalf("overlay EnvValue = %q, want %q", got, want)
	}
	cp, err := execctx.NewFiles(execctx.ModeCopy, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cp.Close() })
	if got := cp.EnvValue("x"); got != cp.Path("x") {
		t.Fatalf("copy EnvValue = %q, want %q", got, cp.Path("x"))
	}
	if _, err := cp.Write("../escape", f.ctx(f.a, "ctx-a", "", execctx.Impersonation{})); err == nil {
		t.Fatal("slot with a path separator was accepted")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
