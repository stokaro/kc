package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	utilexec "k8s.io/client-go/util/exec"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/access"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/nodeshell"
	"github.com/stokaro/kc/internal/podexec"
)

// nodeShellDeleteTimeout bounds the delete of the shell pod.
const nodeShellDeleteTimeout = 10 * time.Second

// nodeShellSession is an open node shell: its pod and its terminal.
type nodeShellSession struct {
	view   *execView
	client kubernetes.Interface
	pod    *corev1.Pod
}

// nodeShellClient talks to the API server for one node shell.
type nodeShellClient struct {
	cfg    *rest.Config
	client kubernetes.Interface
}

// nodeShellStartedMsg reports the outcome of starting the pod.
type nodeShellStartedMsg struct {
	req    models.NodeShellRequest
	conn   nodeShellClient
	pod    *corev1.Pod
	err    error
	stream execStreamFunc // nil: podexec.Stream; tests replace it
}

// nodeShellDeletedMsg reports that the pod of an ended shell was deleted.
type nodeShellDeletedMsg struct {
	pod string
	err error
}

// newNodeShellClient builds the client from the current cluster's config.
// Tests replace it.
var newNodeShellClient = func(cfg *rest.Config) (nodeShellClient, error) {
	cfg = rest.AddUserAgent(rest.CopyConfig(cfg), "kc-node-shell")
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nodeShellClient{}, err
	}
	return nodeShellClient{cfg: cfg, client: client}, nil
}

// startNodeShell creates the pod in the background in pc, the connection
// of the node row that asked; nodeShellStartedMsg follows.
func (a *App) startNodeShell(pc *panelConn, req models.NodeShellRequest) tea.Cmd {
	// The confirmation already refuses in read-only mode; this catches a
	// request that was on its way when the context became read-only.
	createPod := access.Check{Resource: corev1.SchemeGroupVersion.WithResource("pods"), Verb: "create", Namespace: req.Namespace}
	if cmd := a.refuseReadOnly(req.Context, "Node shell", createPod); cmd != nil {
		return cmd
	}
	switch {
	case a.nodeShell != nil || a.nodeShellStarting:
		return a.ShowToast("A node shell is already open", 3*time.Second)
	case pc.cluster() == nil || pc.name() != req.Context:
		return a.ShowToast("Node shell: no panel shows the context "+req.Context, 5*time.Second)
	case !a.cfg.NodeShell.EnabledFor(req.Context):
		return a.ShowToast("Node shell is disabled for "+req.Context+" (nodeShell in config)", 5*time.Second)
	}
	conn, err := newNodeShellClient(pc.cluster().GetConfig())
	if err != nil {
		return a.ShowToast("Node shell: "+err.Error(), 5*time.Second)
	}
	opts := nodeshell.OptionsFrom(a.cfg.NodeShell)
	ctx := a.ctx
	a.nodeShellStarting = true
	start := func() tea.Msg {
		pod, err := nodeshell.Start(ctx, conn.client, req.Node, req.Pod, opts)
		return nodeShellStartedMsg{req: req, conn: conn, pod: pod, err: err}
	}
	return tea.Batch(a.ShowInfo(fmt.Sprintf("Starting node shell on %s (pod %s/%s)...", req.Node, req.Namespace, req.Pod), opts.ReadyTimeout), start)
}

// handleNodeShellStarted opens the terminal on the running pod.
func (a *App) handleNodeShellStarted(msg nodeShellStartedMsg) tea.Cmd {
	a.nodeShellStarting = false
	if msg.err != nil {
		return a.ShowToast("Node shell failed: "+msg.err.Error(), 8*time.Second)
	}
	status := fmt.Sprintf("Node shell on %s, pod %s/%s (privileged). exit or C-] ends it and deletes the pod.",
		msg.req.Node, msg.pod.Namespace, msg.pod.Name)
	view, err := newExecView(a.width, a.height, status)
	if err != nil {
		sess := &nodeShellSession{client: msg.conn.client, pod: msg.pod}
		return tea.Batch(a.ShowToast("Node shell: "+err.Error(), 5*time.Second), a.deleteNodeShellPod(sess))
	}
	stream := msg.stream
	if stream == nil {
		target := podexec.Target{
			Namespace: msg.pod.Namespace,
			Pod:       msg.pod.Name,
			Container: nodeshell.ContainerName,
			Command:   nodeshell.ShellCommand(),
			TTY:       true,
		}
		stream = func(ctx context.Context, stdin io.Reader, stdout io.Writer, sizes *podexec.SizeQueue) error {
			// The keepalive ends with the shell's context; without it the
			// pod stops within nodeshell.IdleTimeout.
			go func() {
				if err := nodeshell.Keepalive(ctx, msg.conn.cfg, msg.conn.client, msg.pod); err != nil && ctx.Err() == nil {
					ctrllog.FromContext(ctx).Error(err, "node shell keepalive ended", "namespace", msg.pod.Namespace, "name", msg.pod.Name)
				}
			}()
			return podexec.Stream(ctx, msg.conn.cfg, msg.conn.client, target, stdin, stdout, sizes)
		}
	}
	a.nodeShell = &nodeShellSession{view: view, client: msg.conn.client, pod: msg.pod}
	a.invalidateView("node shell open")
	return view.start(a.ctx, stream)
}

// updateNodeShell routes messages while a node shell is open. Keys,
// pastes and mouse events belong to the shell; C-] ends it. Other
// messages also reach the rest of kc (handled is false).
func (a *App) updateNodeShell(msg tea.Msg) (tea.Cmd, bool) {
	view := a.nodeShell.view
	a.invalidateView("node shell")
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if k := msg.Key(); k.Code == ']' && k.Mod.Contains(tea.ModCtrl) {
			view.stop()
			return nil, true
		}
		return view.update(msg), true
	case tea.KeyMsg, tea.PasteMsg, tea.MouseMsg:
		return view.update(msg), true
	case tea.WindowSizeMsg:
		return view.resize(max(40, msg.Width), max(5, msg.Height)), false
	case execEndedMsg, nodeShellStartedMsg, nodeShellDeletedMsg:
		return nil, false
	}
	return view.update(msg), false
}

// handleExecEnded closes the shell and deletes its pod.
func (a *App) handleExecEnded(msg execEndedMsg) tea.Cmd {
	sess := a.nodeShell
	if sess == nil || sess.view.id != msg.id {
		return nil
	}
	a.nodeShell = nil
	sess.view.close()
	a.invalidateView("node shell closed")
	a.invalidateFunctionBar("node shell closed")
	a.invalidateTerminalArea("node shell closed")
	cmds := []tea.Cmd{a.deleteNodeShellPod(sess)}
	if err := msg.err; err != nil && !shellExitError(err) && !errors.Is(err, context.Canceled) {
		cmds = append(cmds, a.ShowToast("Node shell ended: "+err.Error(), 8*time.Second))
	}
	cmds = append(cmds, a.resizePanelsForCurrentLayout()...)
	return tea.Batch(cmds...)
}

// shellExitError reports a shell that exited with a status: normal end.
func shellExitError(err error) bool {
	var exit utilexec.ExitError
	return errors.As(err, &exit)
}

func (a *App) deleteNodeShellPod(sess *nodeShellSession) tea.Cmd {
	ctx := a.ctx
	return func() tea.Msg {
		// kc may be quitting; the pod must go anyway.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeShellDeleteTimeout)
		defer cancel()
		return nodeShellDeletedMsg{pod: sess.pod.Namespace + "/" + sess.pod.Name, err: nodeshell.Delete(ctx, sess.client, sess.pod)}
	}
}

func (a *App) handleNodeShellDeleted(msg nodeShellDeletedMsg) tea.Cmd {
	if msg.err != nil {
		return a.ShowToast("Could not delete node shell pod "+msg.pod+": "+msg.err.Error(), 8*time.Second)
	}
	return a.ShowInfo("Node shell pod "+msg.pod+" deleted", 3*time.Second)
}

// closeNodeShell ends an open node shell and deletes its pod before kc
// exits.
func (a *App) closeNodeShell() {
	sess := a.nodeShell
	if sess == nil {
		return
	}
	a.nodeShell = nil
	sess.view.close()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(a.ctx), nodeShellDeleteTimeout)
	defer cancel()
	if err := nodeshell.Delete(ctx, sess.client, sess.pod); err != nil {
		ctrllog.FromContext(a.ctx).Error(err, "delete node shell pod on exit", "namespace", sess.pod.Namespace, "name", sess.pod.Name)
	}
}

// renderNodeShell draws the open shell over the whole screen.
func (a *App) renderNodeShell() tea.View {
	content, cursor := a.nodeShell.view.render()
	v := viewWithCursor(content, cursor)
	v.AltScreen = true
	return v
}
