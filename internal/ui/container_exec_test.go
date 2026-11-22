package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
)

// collectMsgs runs cmd and every command a batch carries, and returns the
// messages of the given type.
func collectMsgs[T any](cmd tea.Cmd) []T {
	var out []T
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch m := c().(type) {
		case tea.BatchMsg:
			for _, sub := range m {
				walk(sub)
			}
		case T:
			out = append(out, m)
		}
	}
	walk(cmd)
	return out
}

func execTestApp(t *testing.T, rows ...table.Row) *App {
	t.Helper()
	app := NewApp()
	app.width, app.height = 100, 30
	ctx := t.Context()
	app.leftPanel.Update(tea.WindowSizeMsg{Width: 50, Height: 20})
	app.leftPanel.UseFolder(ctx, true)
	app.leftPanel.SetFolder(ctx, &namesFolder{rows: rows}, false)
	// The app installs the real handler once a cluster is up; Enter needs one.
	app.leftPanel.SetFolderNavHandler(func(bool, string, models.Folder) {})
	app.activePanel = 0
	return app
}

func containerRow() *models.ContainerItem {
	item := models.NewContainerItem("app", []string{"/app"}, nil, models.WhiteStyle(), nil, nil)
	item.SetExecTarget(models.ExecTarget{
		Cluster:   kccluster.Key{ContextName: "dev"},
		Namespace: "shop", Pod: "web-0", Container: "app", Stdin: true, TTY: true,
	})
	return item
}

func TestContainerSessionKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []tea.KeyPressMsg
		mode models.ExecMode
	}{
		{"M-x opens a shell", []tea.KeyPressMsg{{Code: 'x', Mod: tea.ModAlt}}, models.ExecShell},
		{"M-t attaches", []tea.KeyPressMsg{{Code: 't', Mod: tea.ModAlt}}, models.ExecAttach},
		{"Esc x opens a shell", []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 'x', Text: "x"}}, models.ExecShell},
		{"Esc t attaches", []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 't', Text: "t"}}, models.ExecAttach},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := execTestApp(t, containerRow())
			var cmd tea.Cmd
			for _, k := range tc.keys {
				_, cmd = app.Update(k)
			}
			got := collectMsgs[execResolvedMsg](cmd)
			if len(got) != 1 {
				t.Fatalf("got %d resolved messages, want 1", len(got))
			}
			if got[0].err != nil || got[0].mode != tc.mode || got[0].target.Container != "app" || got[0].target.Pod != "web-0" {
				t.Fatalf("resolved = %+v, want mode %v for web-0/app", got[0], tc.mode)
			}
		})
	}
}

func TestContainerSessionKeysIgnoreOtherRows(t *testing.T) {
	app := execTestApp(t, models.NewSimpleItem("cm", []string{"/configmaps"}, nil, models.WhiteStyle()))
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	if got := collectMsgs[execResolvedMsg](cmd); len(got) != 0 {
		t.Fatalf("M-x on a non-container row resolved %+v", got)
	}
}

func TestEnterOnShellRowRequestsAShell(t *testing.T) {
	target := models.ExecTarget{Cluster: kccluster.Key{ContextName: "dev"}, Namespace: "shop", Pod: "web-0", Container: "app"}
	app := execTestApp(t, models.NewContainerShellItem("shell", []string{"shell"}, nil, target))
	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	reqs := collectMsgs[models.ExecRequest](cmd)
	if len(reqs) != 1 || reqs[0].Mode != models.ExecShell {
		t.Fatalf("Enter on the shell row: requests %+v, want one shell request", reqs)
	}
	_, cmd = app.Update(reqs[0])
	got := collectMsgs[execResolvedMsg](cmd)
	if len(got) != 1 || !reflect.DeepEqual(got[0].target, target) {
		t.Fatalf("resolved %+v, want %+v", got, target)
	}
}

func TestExecFailureMessage(t *testing.T) {
	t.Parallel()
	target := models.ExecTarget{Pod: "web-0", Container: "app"}
	exitErr := errors.New("exit status 1")
	for _, tc := range []struct {
		name   string
		mode   models.ExecMode
		err    error
		stderr string
		want   string // substring; "" means no message
	}{
		{name: "clean exit", mode: models.ExecShell},
		{name: "shell exits non-zero", mode: models.ExecShell, err: exitErr, stderr: "command terminated with exit code 130\n"},
		{
			name: "image without a shell", mode: models.ExecShell, err: exitErr,
			stderr: `error: Internal error occurred: error executing command in container: failed to exec in container: failed to start exec: OCI runtime exec failed: exec failed: unable to start container process: exec: "sh": executable file not found in $PATH: unknown` + "\n",
			want:   "web-0/app has no shell (tried bash and sh). Browse its files with the /root row, which can use the podfs debug helper",
		},
		{name: "kubectl missing", mode: models.ExecShell, err: &exec.Error{Name: "kubectl", Err: exec.ErrNotFound}, want: "kubectl not found in PATH"},
		{
			name: "forbidden", mode: models.ExecAttach, err: exitErr,
			stderr: "Error from server (Forbidden): pods \"web-0\" is forbidden: User \"alice\" cannot create resource \"pods/attach\"\n",
			want:   "Attach in web-0/app failed: Error from server (Forbidden)",
		},
		{name: "no stderr", mode: models.ExecShell, err: exitErr, want: "Shell in web-0/app failed: exit status 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, show := execFailureMessage(target, tc.mode, tc.err, tc.stderr)
			if tc.want == "" {
				if show {
					t.Fatalf("got message %q, want none", got)
				}
				return
			}
			if !show || !strings.Contains(got, tc.want) {
				t.Fatalf("message = %q (show=%v), want it to contain %q", got, show, tc.want)
			}
		})
	}
}

// The session runs kubectl with the target's arguments, prints the
// equivalent command first and keeps stderr for the failure message.
func TestExecSessionRunsKubectl(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\necho 'Error from server (NotFound): pods \"web-0\" not found' >&2\nexit 1\n"
	// #nosec G306 -- the fake kubectl must be executable.
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	target := models.ExecTarget{
		Cluster:   kccluster.Key{KubeconfigPath: "/k/config", ContextName: "prod", ImpersonateUser: "alice"},
		Namespace: "shop", Pod: "web-0", Container: "app",
		Defaulted: true, Containers: []string{"app", "proxy"},
	}
	session := newExecSession(target, models.ExecShell)
	var stdout, stderr strings.Builder
	session.SetStdout(&stdout)
	session.SetStderr(&stderr)
	session.SetStdin(strings.NewReader(""))
	err := session.Run()
	if err == nil {
		t.Fatalf("Run: want the fake kubectl's exit error")
	}

	args, readErr := os.ReadFile(filepath.Join(dir, "args"))
	if readErr != nil {
		t.Fatalf("fake kubectl did not run: %v", readErr)
	}
	if got, want := strings.Fields(string(args))[:8], []string{"--kubeconfig", "/k/config", "--context", "prod", "--as", "alice", "exec", "-it"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("kubectl args start %q, want %q", got, want)
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "[kc] kubectl --context prod --as alice exec -it web-0 -n shop -c app -- sh -c ") {
		t.Fatalf("banner = %q", out)
	}
	if !strings.Contains(out, `container "app", the default of app, proxy`) {
		t.Fatalf("banner does not name the default container: %q", out)
	}
	if !strings.Contains(stderr.String(), "NotFound") || !strings.Contains(session.stderrTail(), "NotFound") {
		t.Fatalf("stderr %q tail %q, want the kubectl error in both", stderr.String(), session.stderrTail())
	}
	msg, show := execFailureMessage(target, models.ExecShell, err, session.stderrTail())
	if !show || !strings.Contains(msg, `pods "web-0" not found`) {
		t.Fatalf("failure message = %q show=%v", msg, show)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	t.Parallel()
	var b tailBuffer
	_, _ = b.Write([]byte(strings.Repeat("x", tailBufferSize)))
	_, _ = b.Write([]byte("end"))
	if got := b.String(); len(got) != tailBufferSize || !strings.HasSuffix(got, "end") {
		t.Fatalf("tail has %d bytes, suffix %q", len(got), got[len(got)-3:])
	}
}
