package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/execctx"
	"github.com/stokaro/kc/internal/models"
)

// Keys that open a session in the container of the selected pod or
// container row. k9s uses s and a, but plain letters go to the shell line
// in kc, so the session keys are Meta keys (Esc x / Esc t as fallback).
const (
	containerShellKey  = "alt+x"
	containerAttachKey = "alt+t"
)

// execResolveTimeout bounds reading the pod that decides the container.
const execResolveTimeout = 10 * time.Second

// execResolvedMsg carries the container a session will open in.
type execResolvedMsg struct {
	target models.ExecTarget
	mode   models.ExecMode
	err    error
}

// execFinishedMsg reports the end of a kubectl session.
type execFinishedMsg struct {
	target models.ExecTarget
	mode   models.ExecMode
	err    error
	stderr string
}

// openContainerSession starts a shell or attach for the selected row of the
// active panel. Rows that are neither pods nor containers ignore the key.
func (a *App) openContainerSession(mode models.ExecMode) tea.Cmd {
	panel := a.activePanelRef()
	if panel == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := panel.SelectedNavItem(ctx)
	cancel()
	if !ok || item == nil || !models.SupportsExec(item) {
		return nil
	}
	return a.resolveExecTarget(panel.Folder(), item, mode)
}

// resolveExecTarget reads what it needs (the pod, for a pod row) off the UI
// goroutine.
func (a *App) resolveExecTarget(folder models.Folder, item models.Item, mode models.ExecMode) tea.Cmd {
	parent := a.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, execResolveTimeout)
		defer cancel()
		target, err := models.ResolveExecTarget(ctx, folder, item)
		return execResolvedMsg{target: target, mode: mode, err: err}
	}
}

// errExecReadOnly refuses a shell or attach session in a read-only context.
var errExecReadOnly = errors.New("disabled in read-only mode: a shell or attach can change anything in the container")

// execGate is the single place that decides whether kc may open a session.
// Read-only mode refuses both shell and attach: kc cannot see what is typed
// in the session. The access checks for create on pods/exec and pods/attach
// (#102) can plug in here as well.
func (a *App) execGate(target models.ExecTarget, _ models.ExecMode) error {
	kubeContext := target.Cluster.ContextName
	if kubeContext == "" {
		kubeContext = a.activeConn().name()
	}
	if a.readOnlyFor(kubeContext) {
		return errExecReadOnly
	}
	return nil
}

func (a *App) handleExecResolved(msg execResolvedMsg) tea.Cmd {
	if msg.err != nil {
		return a.execError(fmt.Sprintf("%s: %v", titleCase(msg.mode.String()), msg.err))
	}
	if err := a.execGate(msg.target, msg.mode); err != nil {
		return a.execError(fmt.Sprintf("%s in %s: %v", titleCase(msg.mode.String()), msg.target.Label(), err))
	}
	log := ctrllog.FromContext(a.ctx).WithName("container_exec")
	command := msg.target.Command(msg.mode)
	log.Info("launching kubectl session", "mode", msg.mode.String(), "namespace", msg.target.Namespace,
		"pod", msg.target.Pod, "container", msg.target.Container, "command", command)

	// Return to the panels first, as kubectl edit does, so the screen kc
	// comes back to is the one the session started from.
	if a.modalManager != nil {
		for a.modalManager.IsModalVisible() {
			a.hideModal()
		}
	}
	if a.showTerminal {
		a.showTerminal = false
		if a.terminal != nil {
			a.terminal.SetShowPanels(true)
		}
		a.invalidateView("container session exit terminal")
		a.invalidateFunctionBar("terminal exit")
		a.invalidateTerminalArea("terminal exit")
	}

	session := newExecSession(msg.target, msg.mode)
	session.interrupts = &a.interrupts
	target, mode := msg.target, msg.mode
	return tea.Exec(session, func(err error) tea.Msg {
		return execFinishedMsg{target: target, mode: mode, err: err, stderr: session.stderrTail()}
	})
}

func (a *App) handleExecFinished(msg execFinishedMsg) tea.Cmd {
	text, show := execFailureMessage(msg.target, msg.mode, msg.err, msg.stderr)
	if !show {
		return nil
	}
	return a.execError(text)
}

func (a *App) execError(text string) tea.Cmd {
	if a.toastLogger != nil {
		return a.toastLogger.Errorf("%s", text)
	}
	return a.ShowToast(text, 5*time.Second)
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// execFailureMessage turns the end of a session into a message, or none.
// A shell that ends with a non-zero status is the user's business; kubectl
// reports it as "command terminated with exit code N" and kc stays quiet.
func execFailureMessage(target models.ExecTarget, mode models.ExecMode, err error, stderr string) (string, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "kubectl not found in PATH; kc runs kubectl for shell and attach", true
	}
	if mode == models.ExecShell && strings.Contains(stderr, "executable file not found") {
		return fmt.Sprintf("%s has no shell (tried bash and sh). Browse its files with the /root row, which can use the podfs debug helper, or run kubectl debug.", target.Label()), true
	}
	last := lastStderrLine(stderr)
	if strings.HasPrefix(last, "command terminated with exit code") {
		return "", false
	}
	if last == "" {
		last = err.Error()
	}
	return fmt.Sprintf("%s in %s failed: %s", titleCase(mode.String()), target.Label(), last), true
}

func lastStderrLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for _, line := range slices.Backward(lines) {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

// execSession runs kubectl on the terminal kc releases. It prints the
// command first, so the user sees what runs, and keeps the end of stderr
// to explain a failure after kc takes the screen back.
type execSession struct {
	banner string
	args   []string
	// kubeconfig is the KUBECONFIG value kubectl runs with; empty keeps
	// kc's own.
	kubeconfig string
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	tail       tailBuffer
	// interrupts, when set, is held while kubectl runs: a C-c in the
	// cooked terminal reaches kc too and must not quit it.
	interrupts *interruptGate
}

func newExecSession(target models.ExecTarget, mode models.ExecMode) *execSession {
	banner := "[kc] " + target.Command(mode)
	if target.Defaulted && len(target.Containers) > 1 {
		banner += fmt.Sprintf("\n[kc] container %q, the default of %s", target.Container, strings.Join(target.Containers, ", "))
	}
	if mode == models.ExecAttach {
		if target.Stdin {
			banner += "\n[kc] keys go to the main process; ending it ends the container"
		} else {
			banner += "\n[kc] the container has no stdin: output only, C-c leaves"
		}
	}
	return &execSession{banner: banner, args: target.KubectlArgs(mode), kubeconfig: target.KubeconfigEnv()}
}

func (s *execSession) SetStdin(r io.Reader)  { s.stdin = r }
func (s *execSession) SetStdout(w io.Writer) { s.stdout = w }
func (s *execSession) SetStderr(w io.Writer) { s.stderr = w }

func (s *execSession) Run() error {
	stdout, stderr := s.stdout, s.stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprintf(stdout, "%s\n", s.banner)
	// #nosec G204 -- kubectl is fixed; the arguments are built by kc from the
	// selected object and passed without a shell.
	cmd := exec.Command("kubectl", s.args...)
	cmd.Stdin = s.stdin
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, &s.tail)
	cmd.Env = os.Environ()
	if s.kubeconfig != "" {
		cmd.Env = execctx.ProcessEnv(s.kubeconfig)
	}
	if s.interrupts != nil {
		release := s.interrupts.hold()
		defer release()
	}
	return cmd.Run()
}

func (s *execSession) stderrTail() string { return s.tail.String() }

// tailBuffer keeps the last bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

const tailBufferSize = 4096

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - tailBufferSize; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
