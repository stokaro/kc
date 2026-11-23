package ui

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/execctx"
	"github.com/stokaro/kc/internal/models"
)

// localEditFinishedMsg reports that the editor F4 opened on a local file
// exited.
type localEditFinishedMsg struct {
	err        error
	panelIndex int
	path       string
}

// localEditor is the editor F4 opens on a local file. It follows kubectl
// edit, so F4 opens the same editor on a file and on a resource:
// KUBE_EDITOR, then EDITOR, then vi.
func localEditor(getenv func(string) string) string {
	for _, name := range []string{"KUBE_EDITOR", "EDITOR"} {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
	}
	return "vi"
}

// localEditCommand runs editor on path. An editor with arguments, such as
// "code --wait", runs through sh the way kubectl runs it; path is passed as
// a separate argument, so no character in it reaches the shell.
func localEditCommand(editor, path string) (*exec.Cmd, string) {
	if !strings.ContainsAny(editor, " \t") {
		// #nosec G204 -- the user's own editor setting; path is one argument.
		return exec.Command(editor, path), execctx.ShellJoin([]string{editor, path})
	}
	if runtime.GOOS == "windows" {
		argv := append(strings.Fields(editor), path)
		// #nosec G204 -- the user's own editor setting; path is one argument.
		return exec.Command(argv[0], argv[1:]...), execctx.ShellJoin(argv)
	}
	// #nosec G204 -- the user's own editor setting; path is passed as $1.
	return exec.Command("sh", "-c", editor+` "$1"`, "kc-edit", path), editor + " " + execctx.ShellJoin([]string{path})
}

// editLocalFile opens the editor on a local file. The editor writes the
// file in place; kc reads nothing back but the refreshed listing. It does
// not touch a cluster, so read-only mode allows it.
func (a *App) editLocalFile(panelIdx int, path string) tea.Cmd {
	if err := models.CheckLocalEditable(path); err != nil {
		return a.localEditError(err)
	}
	cmd, commandStr := localEditCommand(localEditor(os.Getenv), path)
	ctrllog.FromContext(a.ctx).WithName("local_edit").Info("launching editor", "command", commandStr)
	a.logCommandToTerminal(commandStr)
	a.leaveForEditor("local edit exit terminal")
	return a.execProcess(cmd, func(err error) tea.Msg {
		return localEditFinishedMsg{err: err, panelIndex: panelIdx, path: path}
	})
}

func (a *App) handleLocalEditFinished(msg localEditFinishedMsg) tea.Cmd {
	if msg.err != nil {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("Edit %s failed: %v", msg.path, msg.err)
		}
		return a.ShowToast("Edit "+msg.path+" failed: "+msg.err.Error(), 5*time.Second)
	}
	// The size and the modification time changed.
	a.refreshPanelAfterEdit(msg.panelIndex)
	return nil
}

func (a *App) localEditError(err error) tea.Cmd {
	if a.toastLogger != nil {
		return a.toastLogger.Errorf("Edit failed: %v", err)
	}
	return a.ShowToast("Edit failed: "+err.Error(), 5*time.Second)
}
