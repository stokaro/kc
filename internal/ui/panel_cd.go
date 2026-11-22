package ui

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// cdErrorToast is how long a cd error stays on the command line.
const cdErrorToast = 4 * time.Second

// interceptShellCd takes over a cd typed on the shell line, as mc does: the
// active panel changes location and the shell never sees the line. It
// reports false when the line is not a plain cd (or kc does not know the
// whole line); then Enter goes to the shell as usual.
func (a *App) interceptShellCd() (tea.Cmd, bool) {
	if a.terminal == nil {
		return nil, false
	}
	line, ok := a.terminal.ShellLine()
	if !ok {
		return nil, false
	}
	req, err := parseCdLine(line)
	if errors.Is(err, errNotCd) {
		return nil, false
	}
	ctrllog.FromContext(a.ctx).WithName("shellCd").Info("cd from the shell line", "line", line, "panel", a.activePanel)
	// The typed text is in the shell's line editor; C-u discards it there.
	discard := a.terminal.DiscardLine()
	a.invalidateView("shell cd")
	a.invalidateTerminalArea("shell cd")
	if err != nil {
		return tea.Batch(discard, a.ShowToast("cd: "+err.Error(), cdErrorToast)), true
	}
	return tea.Batch(discard, a.runCd(req)), true
}

// runCd moves the active panel as req says.
func (a *App) runCd(req cdRequest) tea.Cmd {
	idx := a.activePanel
	p := req.path
	if req.previous {
		prev := a.prevLocation[idx]
		if prev == nil {
			return a.ShowToast("cd: no previous location", cdErrorToast)
		}
		p = *prev
	}
	return a.NavigatePanel(idx, p, func(err error) tea.Cmd {
		if err == nil {
			return nil
		}
		return a.ShowToast("cd: "+err.Error(), cdErrorToast)
	})
}
