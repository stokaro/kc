package ui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	clientcmd "k8s.io/client-go/tools/clientcmd"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/execctx"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// This file builds the execution contexts (execctx.Context) that the
// subprocesses kc starts run against, one per panel connection, and hands
// them to each consumer:
//
//   - the embedded shell gets KUBECONFIG pointing at a generated file whose
//     path never changes, so the running shell follows the active panel's
//     context and namespace;
//   - F4 (kubectl edit) gets kubectl flags for the panel it was pressed in;
//   - custom commands (F2) get KUBECONFIG pointing at a generated file of
//     their own, for the connection they started with, and the context in
//     their template data.

// shellSlot names the embedded shell's generated kubeconfig.
const shellSlot = "shell"

// CommandExec is what a custom command runs against.
type CommandExec struct {
	Context execctx.Context
	// Env is the full process environment. Nil inherits kc's environment.
	Env []string
}

// CommandExecSupplier gives a custom command its execution context.
type CommandExecSupplier interface {
	// CommandExec prepares slot, the command's own generated kubeconfig,
	// for namespace ("" when the command has none) and returns what the
	// command runs with.
	CommandExec(slot, namespace string) (CommandExec, error)
	// ReleaseCommandExec removes slot when the command is gone.
	ReleaseCommandExec(slot string)
}

var commandSlots atomic.Uint64

// nextCommandSlot names the kubeconfig of a new command widget. A widget
// keeps its slot across restarts, so a watch command rewrites one file.
func nextCommandSlot() string {
	return fmt.Sprintf("cmd-%d", commandSlots.Add(1))
}

// commandProcess builds the process of a custom command.
func commandProcess(shell, cmdStr string, run CommandExec) *exec.Cmd {
	cmd := exec.Command(shell, "-c", cmdStr)
	cmd.Env = run.Env
	return cmd
}

// kubectlEditCommand builds the F4 process and the command line shown for
// it. Flags select the context, namespace and impersonation, so kubectl
// keeps the user's own credentials.
func kubectlEditCommand(ec execctx.Context, resourceRef string) (*exec.Cmd, string) {
	argv := append([]string{"kubectl", "edit", resourceRef}, ec.KubectlArgs()...)
	// #nosec G204 -- kubectl is fixed; the arguments are built by kc and
	// passed without a shell.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = execctx.ProcessEnv(ec.KubeconfigEnv())
	return cmd, execctx.ShellJoin(argv)
}

// impersonation is the identity kc was started with.
func (a *App) impersonation() execctx.Impersonation {
	return execctx.Impersonation{
		User:   a.impersonateUser,
		UID:    a.impersonateUID,
		Groups: append([]string(nil), a.impersonateGroups...),
	}.Normalized()
}

// execContextFor is connection c's context with namespace. It is zero
// when c has no context.
func (a *App) execContextFor(c *panelConn, namespace string) execctx.Context {
	if c == nil || c.kctx == nil || c.kctx.Kubeconfig == nil {
		return execctx.Context{}
	}
	k := c.kctx
	return execctx.Context{
		Kubeconfig:    k.Kubeconfig.Path,
		Load:          k.LoadPath(),
		Config:        k.LoadedConfig(),
		Context:       k.Name,
		Namespace:     strings.TrimSpace(namespace),
		Impersonation: a.impersonation(),
	}
}

// panelExecNamespace is the namespace panel shows: the namespace it browses
// into, else its scope. "" means all namespaces.
func (a *App) panelExecNamespace(panel *Panel) string {
	if panel == nil {
		return ""
	}
	if ns := a.navigatorNamespace(a.navigatorForPanel(panel)); ns != "" {
		return ns
	}
	if idx := a.panelIndex(panel); idx >= 0 && idx < len(a.panelScope) {
		return a.panelScope[idx]
	}
	return ""
}

// kubeconfigFiles returns the generated kubeconfig store, creating it on
// first use.
func (a *App) kubeconfigFiles() (*execctx.Files, error) {
	if a.execFiles != nil {
		return a.execFiles, nil
	}
	mode := execctx.ModeOverlay
	if a.cfg != nil && a.cfg.Terminal.Mode == appconfig.TerminalModeCopy {
		mode = execctx.ModeCopy
	}
	files, err := execctx.NewFiles(mode, a.overlaySources())
	if err != nil {
		return nil, fmt.Errorf("creating kubeconfig directory: %w", err)
	}
	a.execFiles = files
	return files, nil
}

// overlaySources are the files plain kubectl reads (KUBECONFIG, else
// ~/.kube/config) that kc loaded, in that order. An overlay goes in front
// of them, so a shell sees the user's usual contexts. A context from a
// file outside this list is written into the overlay in full.
func (a *App) overlaySources() []execctx.Source {
	if a.kubeMgr == nil {
		return nil
	}
	loaded := make(map[string]*kubeconfig.Kubeconfig)
	for _, kc := range a.kubeMgr.GetKubeconfigs() {
		loaded[filepath.Clean(kc.Path)] = kc
	}
	var out []execctx.Source
	for _, p := range clientcmd.NewDefaultClientConfigLoadingRules().GetLoadingPrecedence() {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if kc := loaded[abs]; kc != nil {
			out = append(out, execctx.Source{Path: kc.Path, Config: kc.Config})
			delete(loaded, abs)
		}
	}
	return out
}

// closeKubeconfigFiles removes every generated kubeconfig.
func (a *App) closeKubeconfigFiles() {
	if a.execFiles == nil {
		return
	}
	_ = a.execFiles.Close()
	a.execFiles = nil
}

// termTarget is the connection and namespace of what the active panel
// shows: its own list, or in Describe, Manifest, Logs and Info the pin's,
// else where the object it shows was read, else the other panel's.
func (a *App) termTarget() (*panelConn, string) {
	idx := a.activePanel
	panel := a.panelByIndex(idx)
	if panel == nil || !followerMode(panel.Mode()) {
		return a.ownConn(idx), a.panelExecNamespace(panel)
	}
	if pin := panel.pinned(); pin != nil && pin.conn != nil {
		return pin.conn, pin.scope
	}
	if o := panel.origin; o.conn != nil {
		// The object the follower shows, which Logs and Info keep after
		// the other panel has moved on (followOrigin).
		return o.conn, o.exec
	}
	return a.ownConn(1 - idx), a.panelExecNamespace(a.panelByIndex(1 - idx))
}

// ensureTermContext points the embedded shell at the active panel's
// effective context and namespace. kc calls it whenever the active panel,
// its context, its namespace or its mode may have changed; the file is
// rewritten only when the target differs from the last one written. The
// KUBECONFIG value stays the same, so the running shell picks the change
// up with its next command, and a program that already runs keeps what
// it started with.
func (a *App) ensureTermContext() {
	if a.terminal == nil || a.cfg == nil || !a.cfg.Terminal.Follow {
		if a.terminal != nil {
			a.terminal.SetEnv(nil)
		}
		a.termWritten = ""
		return
	}
	conn, ns := a.termTarget()
	ec := a.execContextFor(conn, ns)
	if ec.IsZero() {
		return
	}
	written := ec.Load + "\x00" + ec.Context + "\x00" + ec.Namespace
	if written == a.termWritten && a.terminal.env != nil {
		return
	}
	files, err := a.kubeconfigFiles()
	if err == nil {
		var kubeconfigPath string
		if kubeconfigPath, err = files.Write(shellSlot, ec); err == nil {
			a.terminal.SetEnv(execctx.ProcessEnv(kubeconfigPath))
			a.termWritten = written
			ctrllog.FromContext(a.ctx).WithName("termctx").Info("updated terminal kubeconfig",
				"path", files.Path(shellSlot), "context", ec.Context, "namespace", ec.Namespace)
			return
		}
	}
	if a.toastLogger != nil {
		a.enqueueCmd(a.toastLogger.Errorf("Terminal kubeconfig: %v", err))
	}
}

// panelCommandExec supplies custom commands started in one panel. conn,
// when set, is the connection the command started with: the one of the
// rows it runs for. It stays the command's for its watch restarts, even
// after a panel switched its context. source is the panel whose location
// gives the namespace when the command names none.
type panelCommandExec struct {
	app    *App
	panel  *Panel
	conn   *panelConn
	source *Panel
}

// commandExecFor is the supplier of a command shown in panel and started
// for the rows of source, which browses conn.
func (a *App) commandExecFor(panel, source *Panel, conn *panelConn) panelCommandExec {
	return panelCommandExec{app: a, panel: panel, conn: conn, source: source}
}

func (s panelCommandExec) CommandExec(slot, namespace string) (CommandExec, error) {
	source := s.source
	if source == nil {
		source = s.panel
	}
	if namespace == "" {
		namespace = s.app.panelExecNamespace(source)
	}
	conn := s.conn
	if conn == nil {
		conn = s.app.connOfPanel(source)
	}
	ec := s.app.execContextFor(conn, namespace)
	if ec.IsZero() {
		return CommandExec{}, nil
	}
	files, err := s.app.kubeconfigFiles()
	if err != nil {
		return CommandExec{}, err
	}
	kubeconfigPath, err := files.Write(slot, ec)
	if err != nil {
		return CommandExec{}, err
	}
	return CommandExec{Context: ec, Env: execctx.ProcessEnv(kubeconfigPath)}, nil
}

func (s panelCommandExec) ReleaseCommandExec(slot string) {
	if s.app.execFiles != nil {
		_ = s.app.execFiles.Remove(slot)
	}
}
