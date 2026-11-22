package ui

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"

	"github.com/stokaro/kc/internal/execctx"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/panelcontent"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

const execTestConfigA = `apiVersion: v1
kind: Config
current-context: ctx-a
clusters:
- name: cluster-a
  cluster:
    server: https://a.example
users:
- name: user-a
  user:
    token: token-a
contexts:
- name: ctx-a
  context:
    cluster: cluster-a
    user: user-a
    namespace: ns-a
`

const execTestConfigB = `apiVersion: v1
kind: Config
current-context: ctx-b
clusters:
- name: cluster-b
  cluster:
    server: https://b.example
users:
- name: user-b
  user:
    token: token-b
contexts:
- name: ctx-b
  context:
    cluster: cluster-b
    user: user-b
    namespace: ns-b
`

var execTestImp = execctx.Impersonation{User: "alice", UID: "uid-1", Groups: []string{"dev", "ops"}}

type execTestEnv struct {
	app          *App
	ctxA, ctxB   *kubeconfig.Context
	pathA, pathB string
	before       map[string][]byte
}

// newExecTestApp builds an App on two kubeconfig files with kubeconfig
// context A selected and kc started with impersonation. KUBECONFIG names
// both files, so plain kubectl's current context is A.
func newExecTestApp(t *testing.T, mode appconfig.TerminalMode) execTestEnv {
	t.Helper()
	dir := t.TempDir()
	env := execTestEnv{pathA: filepath.Join(dir, "a.yaml"), pathB: filepath.Join(dir, "b.yaml"), before: map[string][]byte{}}
	for p, content := range map[string]string{env.pathA: execTestConfigA, env.pathB: execTestConfigB} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		env.before[p] = []byte(content)
	}
	t.Setenv("KUBECONFIG", env.pathA+string(os.PathListSeparator)+env.pathB)
	mgr := kubeconfig.NewManager()
	if err := mgr.DiscoverKubeconfigs(); err != nil {
		t.Fatal(err)
	}
	for _, c := range mgr.GetContexts() {
		switch c.Name {
		case "ctx-a":
			env.ctxA = c
		case "ctx-b":
			env.ctxB = c
		}
	}
	if env.ctxA == nil || env.ctxB == nil {
		t.Fatalf("contexts not discovered: %v", mgr.GetContexts())
	}
	app := NewApp()
	app.cfg.Terminal = appconfig.TerminalConfig{Follow: true, Mode: mode}
	app.kubeMgr = mgr
	app.currentCtx = env.ctxA
	app.impersonateUser = execTestImp.User
	app.impersonateUID = execTestImp.UID
	app.impersonateGroups = execTestImp.Groups
	t.Cleanup(app.closeKubeconfigFiles)
	env.app = app
	return env
}

func (e execTestEnv) assertKubeconfigsUnchanged(t *testing.T) {
	t.Helper()
	for p, want := range e.before {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("user kubeconfig %s was modified", p)
		}
	}
}

func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

type resolved struct {
	context, host, namespace, token string
	imp                             rest.ImpersonationConfig
}

// resolveKubeconfig reads a KUBECONFIG value the way kubectl does.
func resolveKubeconfig(t *testing.T, value string) resolved {
	t.Helper()
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: filepath.SplitList(value)}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	raw, err := cc.RawConfig()
	if err != nil {
		t.Fatalf("load %q: %v", value, err)
	}
	rc, err := cc.ClientConfig()
	if err != nil {
		t.Fatalf("client config %q: %v", value, err)
	}
	ns, _, err := cc.Namespace()
	if err != nil {
		t.Fatal(err)
	}
	return resolved{context: raw.CurrentContext, host: rc.Host, namespace: ns, token: rc.BearerToken, imp: rc.Impersonate}
}

func assertTargetsB(t *testing.T, who string, got resolved, wantNS string) {
	t.Helper()
	want := resolved{
		context: "ctx-b", host: "https://b.example", namespace: wantNS, token: "token-b",
		imp: rest.ImpersonationConfig{UserName: execTestImp.User, UID: execTestImp.UID, Groups: execTestImp.Groups},
	}
	if got.context != want.context || got.host != want.host || got.namespace != want.namespace || got.token != want.token ||
		got.imp.UserName != want.imp.UserName || got.imp.UID != want.imp.UID || !slices.Equal(got.imp.Groups, want.imp.Groups) {
		t.Fatalf("%s resolves to %+v, want %+v", who, got, want)
	}
}

// stubOnPath puts a script named name first on PATH. It prints its
// KUBECONFIG and then one argument per line.
func stubOnPath(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'KUBECONFIG=%s\\n' \"$KUBECONFIG\"\nfor a in \"$@\"; do printf 'arg=%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runOutput(t *testing.T, cmd *exec.Cmd) (kubeconfigValue string, args []string) {
	t.Helper()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run %v: %v", cmd.Args, err)
	}
	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
		if v, ok := strings.CutPrefix(line, "KUBECONFIG="); ok {
			kubeconfigValue = v
		} else if v, ok := strings.CutPrefix(line, "arg="); ok {
			args = append(args, v)
		}
	}
	return kubeconfigValue, args
}

// TestSwitchedContextReachesEverySubprocess is the acceptance scenario:
// kubeconfig's current context is A, kc switches to B, and the embedded
// shell, F4 and F9 all target B with kc's impersonation and B's own token.
func TestSwitchedContextReachesEverySubprocess(t *testing.T) {
	for _, mode := range []appconfig.TerminalMode{appconfig.TerminalModeOverlay, appconfig.TerminalModeCopy} {
		t.Run(string(mode), func(t *testing.T) {
			e := newExecTestApp(t, mode)
			stubOnPath(t, "kubectl")
			app := e.app

			app.ensureTermContext()
			shellBefore, ok := envValue(app.terminal.env, "KUBECONFIG")
			if !ok {
				t.Fatal("shell environment has no KUBECONFIG")
			}
			if got := resolveKubeconfig(t, shellBefore); got.context != "ctx-a" {
				t.Fatalf("shell starts on %q, want ctx-a", got.context)
			}

			// Switch kc to B, as applyContextSwitch does, then scope the
			// active panel to a namespace.
			app.currentCtx = e.ctxB
			app.ensureTermContext()
			app.updateTermContextNamespace("team")

			t.Run("embedded shell", func(t *testing.T) {
				shellAfter, _ := envValue(app.terminal.env, "KUBECONFIG")
				if shellAfter != shellBefore {
					t.Fatalf("shell KUBECONFIG changed from %q to %q; a running shell would keep the old one", shellBefore, shellAfter)
				}
				assertTargetsB(t, "running shell", resolveKubeconfig(t, shellBefore), "team")
			})

			t.Run("all namespaces keeps impersonation", func(t *testing.T) {
				app.updateTermContextNamespace(models.AllNamespaces)
				v, _ := envValue(app.terminal.env, "KUBECONFIG")
				assertTargetsB(t, "shell in all namespaces", resolveKubeconfig(t, v), "ns-b")
				app.updateTermContextNamespace("team")
			})

			t.Run("F4 kubectl edit", func(t *testing.T) {
				cmd, line := kubectlEditCommand(app.execContext("team"), "pods.v1./web")
				gotKubeconfig, args := runOutput(t, cmd)
				want := []string{"edit", "pods.v1./web", "--kubeconfig", e.pathB, "--context", "ctx-b", "--namespace", "team",
					"--as", "alice", "--as-uid", "uid-1", "--as-group", "dev", "--as-group", "ops"}
				if !slices.Equal(args, want) {
					t.Fatalf("kubectl argv = %q, want %q", args, want)
				}
				if gotKubeconfig != e.pathB {
					t.Fatalf("kubectl KUBECONFIG = %q, want %q", gotKubeconfig, e.pathB)
				}
				if !strings.HasPrefix(line, "kubectl edit pods.v1./web --kubeconfig ") || !strings.Contains(line, "--as-group ops") {
					t.Fatalf("shown command = %q", line)
				}
			})

			t.Run("F9 custom command", func(t *testing.T) {
				supplier := panelCommandExec{app: app, panel: app.leftPanel}
				run, err := supplier.CommandExec("cmd-test", "team")
				if err != nil {
					t.Fatal(err)
				}
				gotKubeconfig, args := runOutput(t, commandProcess("sh", `kubectl get pods`, run))
				if !slices.Equal(args, []string{"get", "pods"}) {
					t.Fatalf("argv = %q", args)
				}
				assertTargetsB(t, "custom command", resolveKubeconfig(t, gotKubeconfig), "team")

				item := models.NewObjectRow("id", nil, nil, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "team", "web", nil)
				rendered, err := RenderCommandIn("kubectl --context {{.Context}} --as {{.As}} --as-uid {{.AsUID}}{{range .AsGroups}} --as-group {{.}}{{end}} -n {{.Namespace}} get pod {{.Name}}",
					[]models.Item{item}, schema.GroupVersionResource{}, run.Context)
				if err != nil {
					t.Fatal(err)
				}
				if want := "kubectl --context ctx-b --as alice --as-uid uid-1 --as-group dev --as-group ops -n team get pod web"; rendered != want {
					t.Fatalf("rendered = %q, want %q", rendered, want)
				}

				supplier.ReleaseCommandExec("cmd-test")
				if _, err := os.Stat(app.execFiles.Path("cmd-test")); !os.IsNotExist(err) {
					t.Fatalf("released slot still exists: %v", err)
				}
			})

			dir := app.execFiles.Dir()
			assertFileMode(t, dir, 0o700)
			assertFileMode(t, app.execFiles.Path(shellSlot), 0o600)
			e.assertKubeconfigsUnchanged(t)
			app.closeKubeconfigFiles()
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("generated kubeconfig directory left behind: %v", err)
			}
		})
	}
}

func TestShellEnvWithoutFollow(t *testing.T) {
	e := newExecTestApp(t, appconfig.TerminalModeOverlay)
	e.app.cfg.Terminal.Follow = false
	e.app.terminal.SetEnv([]string{"KUBECONFIG=/stale"})
	e.app.ensureTermContext()
	if e.app.terminal.env != nil {
		t.Fatalf("terminal.follow=false should leave the shell environment alone, got %q", e.app.terminal.env)
	}
	if e.app.execFiles != nil {
		t.Fatal("no kubeconfig should be generated for the shell")
	}
}

func TestCommandWithoutContextInheritsEnv(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.closeKubeconfigFiles)
	run, err := panelCommandExec{app: app, panel: app.leftPanel}.CommandExec("cmd-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Env != nil || !run.Context.IsZero() {
		t.Fatalf("without a context the command should inherit kc's environment, got %+v", run)
	}
}

type recordingExec struct {
	calls    [][2]string
	released []string
	env      []string
	err      error
}

func (r *recordingExec) CommandExec(slot, namespace string) (CommandExec, error) {
	r.calls = append(r.calls, [2]string{slot, namespace})
	if r.err != nil {
		return CommandExec{}, r.err
	}
	return CommandExec{Context: execctx.Context{Context: "ctx-b"}, Env: r.env}, nil
}

func (r *recordingExec) ReleaseCommandExec(slot string) { r.released = append(r.released, slot) }

func TestCommandWidgetUsesExecSupplier(t *testing.T) {
	item := models.NewObjectRow("id", nil, nil, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "team", "web", nil)
	cfg := appconfig.CommandConfig{Name: "echo", Command: "echo {{.Context}}", Type: appconfig.CommandTypeSticky}

	t.Run("env, namespace and template", func(t *testing.T) {
		rec := &recordingExec{env: []string{"KUBECONFIG=/kc/cmd.kubeconfig"}}
		w := NewCommandWidget(panelcontent.WidgetDeps{}, cfg)
		w.SetExecSupplier(rec)
		_ = w.StartCommand(t.Context(), []models.Item{item}, schema.GroupVersionResource{})
		if len(rec.calls) != 1 || rec.calls[0][1] != "team" || rec.calls[0][0] != w.execSlot {
			t.Fatalf("supplier calls = %v, want one for slot %q in namespace team", rec.calls, w.execSlot)
		}
		if w.cmd == nil {
			t.Fatal("no process built")
		}
		if !slices.Equal(w.cmd.Env, rec.env) {
			t.Fatalf("process env = %q, want %q", w.cmd.Env, rec.env)
		}
		if got := w.cmd.Args[len(w.cmd.Args)-1]; got != "echo ctx-b" {
			t.Fatalf("command = %q, want the context rendered", got)
		}
		// A restart (watch tick) asks again, on the same slot.
		_ = w.Restart(t.Context())
		if len(rec.calls) != 2 || rec.calls[1][0] != w.execSlot {
			t.Fatalf("restart calls = %v", rec.calls)
		}
		w.Teardown(t.Context())
		if !slices.Equal(rec.released, []string{w.execSlot}) {
			t.Fatalf("released = %q, want %q", rec.released, w.execSlot)
		}
	})

	t.Run("supplier failure starts nothing", func(t *testing.T) {
		rec := &recordingExec{err: errors.New("disk full")}
		w := NewCommandWidget(panelcontent.WidgetDeps{}, cfg)
		w.SetExecSupplier(rec)
		_ = w.StartCommand(t.Context(), []models.Item{item}, schema.GroupVersionResource{})
		if w.cmd != nil {
			t.Fatalf("a process was built: %v", w.cmd.Args)
		}
		if w.err == nil || !strings.Contains(w.err.Error(), "disk full") {
			t.Fatalf("err = %v", w.err)
		}
	})

	t.Run("slots are distinct per widget", func(t *testing.T) {
		a := NewCommandWidget(panelcontent.WidgetDeps{}, cfg)
		b := NewCommandWidget(panelcontent.WidgetDeps{}, cfg)
		if a.execSlot == b.execSlot {
			t.Fatalf("two widgets share slot %q", a.execSlot)
		}
	})
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
