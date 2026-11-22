package ui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	clientcmd "k8s.io/client-go/tools/clientcmd"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/sttts/kc/internal/execctx"
	"github.com/sttts/kc/pkg/appconfig"
	"github.com/sttts/kc/pkg/kubeconfig"
)

// This file builds the one execution context (execctx.Context) that every
// subprocess kc starts runs against, and hands it to each consumer:
//
//   - the embedded shell gets KUBECONFIG pointing at a generated file whose
//     path never changes, so the running shell follows context switches;
//   - F4 (kubectl edit) gets kubectl flags;
//   - custom commands (F2) get KUBECONFIG pointing at a generated file of
//     their own, and the context in their template data.

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
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = execctx.ProcessEnv(ec.Kubeconfig)
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

// execContext is kc's selected context with namespace. It is zero when no
// context is selected.
func (a *App) execContext(namespace string) execctx.Context {
	if a.currentCtx == nil || a.currentCtx.Kubeconfig == nil {
		return execctx.Context{}
	}
	return execctx.Context{
		Kubeconfig:    a.currentCtx.Kubeconfig.Path,
		Config:        a.currentCtx.Kubeconfig.Config,
		Context:       a.currentCtx.Name,
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
	loaded := map[string]*kubeconfig.Kubeconfig{}
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

// ensureTermContext points the embedded shell at the selected context and
// the active panel's namespace.
func (a *App) ensureTermContext() {
	a.updateTermContextNamespace(a.panelExecNamespace(a.panelByIndex(a.activePanel)))
}

// updateTermContextNamespace rewrites the shell's kubeconfig for the
// selected context and ns. The KUBECONFIG value stays the same, so the
// running shell picks the change up with its next command.
func (a *App) updateTermContextNamespace(ns string) {
	if a.terminal == nil || a.cfg == nil || !a.cfg.Terminal.Follow {
		if a.terminal != nil {
			a.terminal.SetEnv(nil)
		}
		return
	}
	ec := a.execContext(ns)
	if ec.IsZero() {
		return
	}
	files, err := a.kubeconfigFiles()
	if err == nil {
		var kubeconfig string
		if kubeconfig, err = files.Write(shellSlot, ec); err == nil {
			a.terminal.SetEnv(execctx.ProcessEnv(kubeconfig))
			ctrllog.FromContext(a.ctx).WithName("termctx").Info("updated terminal kubeconfig",
				"path", files.Path(shellSlot), "context", ec.Context, "namespace", ec.Namespace)
			return
		}
	}
	if a.toastLogger != nil {
		a.enqueueCmd(a.toastLogger.Errorf("Terminal kubeconfig: %v", err))
	}
}

// panelCommandExec supplies custom commands started in one panel.
type panelCommandExec struct {
	app   *App
	panel *Panel
}

func (s panelCommandExec) CommandExec(slot, namespace string) (CommandExec, error) {
	if namespace == "" {
		namespace = s.app.panelExecNamespace(s.panel)
	}
	ec := s.app.execContext(namespace)
	if ec.IsZero() {
		return CommandExec{}, nil
	}
	files, err := s.app.kubeconfigFiles()
	if err != nil {
		return CommandExec{}, err
	}
	kubeconfig, err := files.Write(slot, ec)
	if err != nil {
		return CommandExec{}, err
	}
	return CommandExec{Context: ec, Env: execctx.ProcessEnv(kubeconfig)}, nil
}

func (s panelCommandExec) ReleaseCommandExec(slot string) {
	if s.app.execFiles != nil {
		_ = s.app.execFiles.Remove(slot)
	}
}
