package ui

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/models"
)

// copyCommandKey copies the kubectl command of the active panel. Alt+C is
// free in panels and in the shell line, and reads as "copy".
const copyCommandKey = "alt+c"

var (
	commandLineBg      = lipgloss.Color("18")
	commandLineStyle   = lipgloss.NewStyle().Background(commandLineBg).Foreground(lipgloss.Color("252"))
	commandLineLabel   = commandLineStyle.Foreground(lipgloss.Color("248"))
	commandLineCommand = commandLineStyle.Foreground(lipgloss.BrightWhite)
	commandLineKey     = commandLineStyle.Foreground(lipgloss.BrightYellow)
)

// commandLineHeight is the height of the "Command:" line between the panels
// and the shell line. It is hidden in the fullscreen terminal and on tiny screens.
func (a *App) commandLineHeight() int {
	if a == nil || a.showTerminal || a.height < 8 {
		return 0
	}
	return 1
}

// panelCommand returns the kubectl command matching the active panel's folder.
func (a *App) panelCommand() string {
	panel := a.activePanelRef()
	if panel == nil {
		return ""
	}
	if c, ok := panel.Folder().(models.KubectlCommander); ok {
		return c.KubectlCommand()
	}
	return ""
}

func (a *App) renderCommandLine() string {
	return renderCommandLine(a.panelCommand(), a.width)
}

func renderCommandLine(command string, width int) string {
	if width <= 0 {
		return ""
	}
	label := " Command: "
	hint := ""
	if command != "" {
		hint = renderCopyHint()
	}
	room := width - lipgloss.Width(label) - lipgloss.Width(hint) - 1
	if room < 10 {
		hint = ""
		room = width - lipgloss.Width(label)
	}
	if lipgloss.Width(command) > room {
		command = truncateStringToWidth(command, max(0, room-1)) + "…"
	}
	body := commandLineLabel.Render(label) + commandLineCommand.Render(command)
	gap := max(0, width-lipgloss.Width(body)-lipgloss.Width(hint))
	return body + commandLineStyle.Render(strings.Repeat(" ", gap)) + hint
}

// copyHintIcon is the clickable copy button at the right of the line.
const copyHintIcon = "📋"

func renderCopyHint() string {
	return commandLineStyle.Render(copyHintIcon+" Copy ") + commandLineKey.Render("Alt+C") + commandLineStyle.Render(" ")
}

// commandLineY is the screen row of the command line, or -1 when hidden.
func (a *App) commandLineY() int {
	if a.commandLineHeight() == 0 {
		return -1
	}
	return a.height - 1 - a.inlineTerminalHeight() - a.commandLineHeight()
}

// commandLineCopyHit reports whether x falls on the copy button.
func (a *App) commandLineCopyHit(x int) bool {
	return a.panelCommand() != "" && x >= a.width-lipgloss.Width(renderCopyHint()) && x < a.width
}

// copyPanelCommand puts the active panel's command on the clipboard.
//
// It writes OSC 52 to the terminal, so the terminal on the user's machine
// sets its clipboard; this also works over SSH. Inside tmux or screen the
// sequence is wrapped for passthrough as well. When kc runs locally it also
// calls a clipboard tool (pbcopy and friends), because Terminal.app ignores
// OSC 52. Over SSH that tool would fill the remote clipboard, so it is skipped.
func (a *App) copyPanelCommand() tea.Cmd {
	command := a.panelCommand()
	if command == "" {
		return nil
	}
	cmds := []tea.Cmd{tea.Raw(osc52Sequence(command, os.Getenv)), a.ShowToast("Copied: "+command, 2*time.Second)}
	if !isSSHSession(os.Getenv) {
		cmds = append(cmds, func() tea.Msg {
			_ = copyWithLocalTool(command)
			return nil
		})
	}
	return tea.Batch(cmds...)
}

// osc52Sequence builds the clipboard escape sequence. The plain sequence is
// always sent (tmux with set-clipboard on forwards it itself); inside tmux
// or screen a passthrough-wrapped copy follows for setups that rely on
// allow-passthrough.
func osc52Sequence(text string, getenv func(string) string) string {
	seq := ansi.SetSystemClipboard(text)
	switch {
	case getenv("TMUX") != "":
		return seq + ansi.TmuxPassthrough(seq)
	case strings.HasPrefix(getenv("TERM"), "screen") && getenv("STY") != "":
		return seq + ansi.ScreenPassthrough(seq, 0)
	}
	return seq
}

func isSSHSession(getenv func(string) string) bool {
	return getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" || getenv("SSH_CLIENT") != ""
}

// clipboardTools lists local clipboard writers in order of preference.
var clipboardTools = [][]string{
	{"pbcopy"},
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

func copyWithLocalTool(text string) error {
	for _, tool := range clipboardTools {
		path, err := exec.LookPath(tool[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, tool[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return errors.New("no clipboard tool found")
}
