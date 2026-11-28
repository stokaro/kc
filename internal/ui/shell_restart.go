package ui

import (
	"fmt"
	"os/exec"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	bubbleterm "github.com/taigrr/bubbleterm"
	"github.com/taigrr/bubbleterm/emulator"
)

// The embedded shell does not end kc (#375, docs/adr/0375-keep-kc-when-its-
// shell-exits.md). When it exits, with exit or C-d on an empty line, kc
// starts a new one and says so. A shell that exits at once (a SHELL that
// does not run, an rc file that exits) starts again after a delay that
// doubles, and after shellMaxQuickExits such exits in a row kc stops
// starting shells until C-o.

const (
	// shellQuickExit: a shell that ran less than this exited at once.
	shellQuickExit = 2 * time.Second
	// shellBackoff is the delay after the second quick exit in a row; it
	// doubles with each further one. The first restarts at once.
	shellBackoff = time.Second
	// shellMaxQuickExits is how many shells in a row may exit at once
	// before kc stops starting them.
	shellMaxQuickExits = 5
)

// shellExitedMsg reports that the shell started as number gen ended, or
// did not start.
type shellExitedMsg struct {
	gen int
	// err says why the shell or its terminal did not start.
	err error
	// status is how a failed shell ended, as "exit status 127"; empty
	// when it exited with status 0.
	status string
}

// shellRestartMsg starts the next shell when the delay after shell gen is
// over.
type shellRestartMsg struct{ gen int }

// shellLife is the lifecycle of the embedded shell. The update loop owns
// it.
type shellLife struct {
	// gen counts shell starts; a message about an earlier shell is stale.
	gen     int
	name    string // the command of the last shell, for the notices
	started time.Time
	running bool // the shell gen runs: its exit is still to come
	waiting bool // a delay runs; shellRestartMsg{gen} starts the next shell
	stopped bool // too many quick exits: no shell until C-o
	// quickExits counts the shells in a row that exited at once.
	quickExits int
	// status says why no shell runs, in place of the shell line.
	status string

	now       func() time.Time
	quickExit time.Duration
	backoff   time.Duration
}

func (s *shellLife) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// statusText is what the shell line shows while no shell runs.
func (s *shellLife) statusText() string {
	if s.status != "" {
		return s.status
	}
	return "Starting the shell..."
}

// shellRestartDelay is how long kc waits before it starts the next shell
// after quickExits shells in a row exited at once, and false when it stops
// starting them: 0, 0, base, 2*base, 4*base, stop.
func shellRestartDelay(quickExits int, base time.Duration) (time.Duration, bool) {
	switch {
	case quickExits >= shellMaxQuickExits:
		return 0, false
	case quickExits <= 1:
		return 0, true
	default:
		return base << (quickExits - 2), true
	}
}

// startShell starts a new shell in a new emulator. macOS revokes a pty
// when its session leader exits, so a shell never reuses the emulator of
// the one before it.
func (t *Terminal) startShell() tea.Cmd {
	s := &t.shell
	cmd, name := t.shellCommand()
	s.gen++
	gen := s.gen
	s.name = name
	s.started = s.clock()
	s.running, s.waiting, s.stopped = true, false, false
	s.status = ""
	t.line.reset()
	t.hasTyped = false
	t.cursor, t.haveCursor = nil, false
	if t.log.GetSink() != nil {
		t.log.Info("starting terminal shell", "command", name, "start", gen)
	}
	model, err := bubbleterm.New(t.width, t.height)
	if err != nil {
		// No terminal (no pty): it counts as a shell that exited at once.
		t.terminal = nil
		return func() tea.Msg { return shellExitedMsg{gen: gen, err: fmt.Errorf("open a terminal: %w", err)} }
	}
	// bubbleterm polls by blocking until the emulator reports a change, so
	// output (including the echo of typed keys) shows up without a timer.
	model.SetAutoPoll(true)
	if t.blurred {
		model.Blur()
	}
	t.terminal = model
	emu := model.GetEmulator()
	exited := make(chan struct{})
	closeExited := sync.OnceFunc(func() { close(exited) })
	emu.SetOnExit(func(string) { closeExited() })
	return tea.Batch(model.Init(), runShell(gen, emu, cmd, exited))
}

// runShell starts cmd in emu and reports its end. The goroutine of the
// command ends with the shell, or when kc closes the emulator.
func runShell(gen int, emu *emulator.Emulator, cmd *exec.Cmd, exited <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		if err := emu.StartCommand(cmd); err != nil {
			return shellExitedMsg{gen: gen, err: err}
		}
		select {
		case <-exited:
		case <-emu.Done():
		}
		select {
		case <-emu.Done():
			// kc closed the terminal, quitting: nothing to replace.
			return nil
		default:
		}
		msg := shellExitedMsg{gen: gen}
		// The exit callback runs after cmd.Wait returned, so ProcessState
		// is set.
		if ps := cmd.ProcessState; ps != nil && !ps.Success() {
			msg.status = ps.String()
		}
		return msg
	}
}

// shellEnd is what kc does about a shell that ended.
type shellEnd struct {
	notice string
	// failed makes the notice red: the shell exited at once.
	failed bool
	// cmd starts the next shell now, or after the delay.
	cmd tea.Cmd
}

// shellEnded replaces the shell msg reports: at once, after a delay, or
// not until C-o. ok is false for a message about an earlier shell.
func (t *Terminal) shellEnded(msg shellExitedMsg) (end shellEnd, ok bool) {
	s := &t.shell
	if msg.gen != s.gen || !s.running {
		return shellEnd{}, false
	}
	s.running = false
	ran := s.clock().Sub(s.started)
	quick := msg.err != nil || ran < s.quickExit
	if quick {
		s.quickExits++
	} else {
		s.quickExits = 0
	}
	if t.terminal != nil {
		// Its pty is dead; closing it stops the emulator's read loop.
		_ = t.terminal.Close()
		t.terminal = nil
	}
	what := shellEndText(s.name, msg, quick)
	if t.log.GetSink() != nil {
		t.log.Info("terminal shell ended", "command", s.name, "start", s.gen, "ran", ran.Round(time.Millisecond).String(),
			"status", msg.status, "error", fmt.Sprint(msg.err), "quickExits", s.quickExits)
	}
	delay, again := shellRestartDelay(s.quickExits, s.backoff)
	switch {
	case !again:
		s.stopped = true
		s.status = fmt.Sprintf("%s, %d times in a row; kc stopped starting it. C-o starts it again.", what, s.quickExits)
		return shellEnd{notice: s.status, failed: true}, true
	case delay > 0:
		s.waiting = true
		s.status = fmt.Sprintf("%s; kc starts a new one in %s.", what, delay)
		gen := s.gen
		tick := tea.Tick(delay, func(time.Time) tea.Msg { return shellRestartMsg{gen: gen} })
		return shellEnd{notice: s.status, failed: true, cmd: tick}, true
	default:
		return shellEnd{notice: what + "; kc started a new one.", failed: quick, cmd: t.startShell()}, true
	}
}

// shellEndText says how the shell ended, without the end of the sentence.
func shellEndText(name string, msg shellExitedMsg, quick bool) string {
	switch {
	case msg.err != nil:
		return fmt.Sprintf("The shell did not start (%v)", msg.err)
	case quick && msg.status != "":
		return fmt.Sprintf("The shell %s exited at once (%s)", name, msg.status)
	case quick:
		return fmt.Sprintf("The shell %s exited at once", name)
	case msg.status != "":
		return fmt.Sprintf("The shell exited (%s)", msg.status)
	default:
		return "The shell exited"
	}
}

// shellRestart starts the next shell when its delay is over.
func (t *Terminal) shellRestart(msg shellRestartMsg) tea.Cmd {
	if msg.gen != t.shell.gen || !t.shell.waiting {
		return nil
	}
	return t.startShell()
}

// ShellDown reports whether no shell runs: kc waits before it starts the
// next one, or stopped starting them.
func (t *Terminal) ShellDown() bool { return t.shell.waiting || t.shell.stopped }

// StartShellNow starts a shell while none runs (C-o). After kc stopped
// starting shells, the new one gets one try: if it exits at once too, kc
// stops again.
func (t *Terminal) StartShellNow() tea.Cmd {
	if !t.ShellDown() {
		return nil
	}
	if t.shell.stopped {
		t.shell.quickExits = shellMaxQuickExits - 1
	}
	return t.startShell()
}

// updateShell handles the messages of the shell's lifecycle. They pass
// every dialog: a shell that ended is replaced whatever is open.
func (a *App) updateShell(msg tea.Msg) (tea.Cmd, bool) {
	if a.terminal == nil {
		return nil, false
	}
	switch msg := msg.(type) {
	case shellExitedMsg:
		end, ok := a.terminal.shellEnded(msg)
		if !ok {
			return nil, true
		}
		cmds := []tea.Cmd{end.cmd}
		if a.showTerminal {
			// exit in the full-screen shell returns to the panels, where
			// the notice shows on the key bar line.
			cmds = append(cmds, a.toggleTerminal())
		}
		if end.failed {
			cmds = append(cmds, a.ShowToast(end.notice, 8*time.Second))
		} else {
			cmds = append(cmds, a.ShowInfo(end.notice, 5*time.Second))
		}
		a.invalidateView("shell ended")
		a.invalidateTerminalArea("shell ended")
		return tea.Batch(cmds...), true
	case shellRestartMsg:
		cmd := a.terminal.shellRestart(msg)
		a.invalidateView("shell restart")
		a.invalidateTerminalArea("shell restart")
		return cmd, true
	}
	return nil, false
}
