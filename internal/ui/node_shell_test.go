package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/podexec"
	"github.com/stokaro/kc/pkg/appconfig"
)

type followUpAction struct {
	fakeAction
}

func (f *followUpAction) FollowUp() any { return models.NodeShellRequest{Node: "n1"} }

func TestConfirmFollowUpOnlyWhenApproved(t *testing.T) {
	action := &followUpAction{fakeAction{SimpleItem: models.NewSimpleItem("a", []string{"a"}, nil, models.WhiteStyle())}}
	app := &App{ctx: t.Context(), width: 100, height: 30, modalManager: NewModalManager(nil),
		leftPanel: NewPanel("l"), rightPanel: NewPanel("r"), leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}

	app.showConfirm(action)
	if cmd := app.handleConfirmResult(confirmResultMsg{ok: false}); cmd != nil {
		t.Fatalf("cancel returned a command")
	}
	app.showConfirm(action)
	cmd := app.handleConfirmResult(confirmResultMsg{ok: true})
	if cmd == nil {
		t.Fatalf("approval returned no follow-up")
	}
	if req, ok := cmd().(models.NodeShellRequest); !ok || req.Node != "n1" {
		t.Fatalf("follow-up = %#v, want the node shell request", cmd())
	}
}

func TestStartNodeShellRefuses(t *testing.T) {
	enabled := appconfig.Default()
	enabled.NodeShell.Enabled = true
	tests := []struct {
		name  string
		cfg   *appconfig.Config
		setup func(a *App)
		want  string
	}{
		{name: "disabled", cfg: appconfig.Default(), want: "not the current one"}, // no cluster in the test either
		{name: "already open", cfg: enabled, setup: func(a *App) { a.nodeShellStarting = true }, want: "already open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{ctx: t.Context(), cfg: tt.cfg}
			if tt.setup != nil {
				tt.setup(app)
			}
			cmd := app.startNodeShell(models.NodeShellRequest{Context: "mba", Node: "n1", Pod: "p"})
			msg, ok := cmd().(showToastMsg)
			if !ok || msg.info || !strings.Contains(msg.text, tt.want) {
				t.Fatalf("result = %#v, want an error toast with %q", cmd(), tt.want)
			}
		})
	}
}

// cmdRunner runs commands and their batches in the background and collects
// the messages.
type cmdRunner struct {
	t    *testing.T
	msgs chan tea.Msg
}

func newCmdRunner(t *testing.T) *cmdRunner {
	return &cmdRunner{t: t, msgs: make(chan tea.Msg, 64)}
}

func (r *cmdRunner) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				r.run(c)
			}
			return
		}
		if msg != nil {
			r.msgs <- msg
		}
	}()
}

// await returns the first message of type T, dropping others.
func await[T tea.Msg](r *cmdRunner, what string) T {
	r.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case msg := <-r.msgs:
			if m, ok := msg.(T); ok {
				return m
			}
		case <-timeout:
			var zero T
			r.t.Fatalf("timed out waiting for %s (%T)", what, zero)
			return zero
		}
	}
}

// fakeShell is a stream that echoes what it receives until the context ends.
type fakeShell struct {
	mu    sync.Mutex
	input strings.Builder
	sizes []string
}

func (f *fakeShell) stream(ctx context.Context, stdin io.Reader, stdout io.Writer, sizes *podexec.SizeQueue) error {
	_, _ = io.WriteString(stdout, "node-1 # ")
	go func() {
		for s := sizes.Next(); s != nil; s = sizes.Next() {
			f.mu.Lock()
			f.sizes = append(f.sizes, fmt.Sprintf("%dx%d", s.Width, s.Height))
			f.mu.Unlock()
		}
	}()
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := stdin.Read(buf)
			f.mu.Lock()
			f.input.Write(buf[:n])
			f.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeShell) got() (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.input.String(), append([]string(nil), f.sizes...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func nodeShellTestApp(t *testing.T) (*App, *fake.Clientset, *corev1.Pod) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "kc-node-shell-n1-abcde", Namespace: "default", UID: "u1"}}
	client := fake.NewClientset(pod)
	cfg := appconfig.Default()
	cfg.NodeShell.Enabled = true
	app := &App{ctx: t.Context(), cfg: cfg, width: 80, height: 24, modalManager: NewModalManager(nil),
		leftPanel: NewPanel("l"), rightPanel: NewPanel("r"), leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}
	return app, client, pod
}

func podGone(t *testing.T, client *fake.Clientset, pod *corev1.Pod) bool {
	t.Helper()
	_, err := client.Tracker().Get(podsResource, pod.Namespace, pod.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("tracker: %v", err)
	}
	return apierrors.IsNotFound(err)
}

func TestNodeShellRunsFullScreenAndDeletesThePodOnExit(t *testing.T) {
	app, client, pod := nodeShellTestApp(t)
	shell := &fakeShell{}
	runner := newCmdRunner(t)

	runner.run(app.handleNodeShellStarted(nodeShellStartedMsg{
		req:    models.NodeShellRequest{Context: "mba", Node: "n1"},
		conn:   nodeShellClient{client: client},
		pod:    pod,
		stream: shell.stream,
	}))
	if app.nodeShell == nil {
		t.Fatalf("no node shell after the pod started")
	}

	// The shell owns the keys; the panels do not see them.
	if _, handled := app.updateNodeShell(tea.KeyPressMsg{Code: 'l', Text: "l"}); !handled {
		t.Fatalf("a key while the shell is open was not handled by it")
	}
	app.updateNodeShell(tea.PasteMsg{Content: "s"})
	waitFor(t, "typed input", func() bool { in, _ := shell.got(); return in == "ls" })

	// A resize reaches the remote terminal (one line is the status bar).
	if _, handled := app.updateNodeShell(tea.WindowSizeMsg{Width: 100, Height: 30}); handled {
		t.Fatalf("a resize must reach the rest of kc too")
	}
	waitFor(t, "resize", func() bool { _, s := shell.got(); return len(s) > 0 && s[len(s)-1] == "100x29" })

	view := ansi.Strip(viewString(app.View()))
	for _, want := range []string{"Node shell on n1", "default/kc-node-shell-n1-abcde", "C-]"} {
		if !strings.Contains(view, want) {
			t.Errorf("screen lacks %q:\n%s", want, view)
		}
	}
	waitFor(t, "shell output", func() bool {
		runner.drainInto(app)
		return strings.Contains(ansi.Strip(viewString(app.View())), "node-1 #")
	})

	// C-] ends the shell; the pod is deleted.
	app.updateNodeShell(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	ended := await[execEndedMsg](runner, "shell end")
	runner.run(app.handleExecEnded(ended))
	if app.nodeShell != nil {
		t.Fatalf("node shell still open after it ended")
	}
	deleted := await[nodeShellDeletedMsg](runner, "pod delete")
	if deleted.err != nil {
		t.Fatalf("delete: %v", deleted.err)
	}
	if !podGone(t, client, pod) {
		t.Fatalf("pod %s still exists", pod.Name)
	}
}

// drainInto hands pending emulator messages to the app without blocking.
func (r *cmdRunner) drainInto(a *App) {
	for {
		select {
		case msg := <-r.msgs:
			if a.nodeShell != nil {
				cmd, _ := a.updateNodeShell(msg)
				r.run(cmd)
			}
		default:
			return
		}
	}
}

func TestNodeShellPodIsDeletedWhenKcQuits(t *testing.T) {
	app, client, pod := nodeShellTestApp(t)
	shell := &fakeShell{}
	runner := newCmdRunner(t)
	runner.run(app.handleNodeShellStarted(nodeShellStartedMsg{
		req:    models.NodeShellRequest{Context: "mba", Node: "n1"},
		conn:   nodeShellClient{client: client},
		pod:    pod,
		stream: shell.stream,
	}))

	app.closeNodeShell()

	if !podGone(t, client, pod) {
		t.Fatalf("pod %s still exists after kc quit", pod.Name)
	}
	if app.nodeShell != nil {
		t.Fatalf("node shell still set after quit")
	}
	// The stream was told to stop.
	await[execEndedMsg](runner, "shell end")
}

func TestNodeShellStartFailureShowsTheError(t *testing.T) {
	app, _, _ := nodeShellTestApp(t)
	app.nodeShellStarting = true
	cmd := app.handleNodeShellStarted(nodeShellStartedMsg{err: apierrors.NewForbidden(podsResource.GroupResource(), "p", io.EOF)})
	if app.nodeShellStarting || app.nodeShell != nil {
		t.Fatalf("state after failed start: starting=%v shell=%v", app.nodeShellStarting, app.nodeShell)
	}
	msg, ok := cmd().(showToastMsg)
	if !ok || !strings.Contains(msg.text, "forbidden") {
		t.Fatalf("result = %#v, want a toast with the error", cmd())
	}
}
