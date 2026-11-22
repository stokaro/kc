package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// copyCommandKey copies the kubectl command of the active panel. M-c is
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
	if a.toastOnCommandLine() {
		return renderToast(a.toastText, a.toastInfo, a.width)
	}
	return renderCommandLine(a.panelCommand(), a.width)
}

// toastOnCommandLine reports whether the toast covers the command line. It
// does whenever that line is shown, so the function keys stay visible; with
// the line hidden the toast takes the function bar instead.
func (a *App) toastOnCommandLine() bool {
	return a.toastActive && a.commandLineHeight() > 0
}

// renderToast draws a message across a whole line: red for errors, cyan
// for information.
func renderToast(msg string, info bool, width int) string {
	if lipgloss.Width(msg) > width {
		if width > 1 {
			msg = sliceANSIColsRaw(msg, 0, width-1) + "…"
		} else {
			msg = sliceANSIColsRaw(msg, 0, max(0, width))
		}
	}
	style := lipgloss.NewStyle().Background(lipgloss.Color("196")).Foreground(lipgloss.White).Bold(true)
	if info {
		style = style.Background(lipgloss.Cyan).Foreground(lipgloss.Black)
	}
	return style.Width(width).Render(msg)
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
	return commandLineStyle.Render(copyHintIcon+" Copy ") + commandLineKey.Render("M-c") + commandLineStyle.Render(" ")
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
	return !a.toastOnCommandLine() && a.panelCommand() != "" && x >= a.width-lipgloss.Width(renderCopyHint()) && x < a.width
}

// copyResultMsg reports a finished copy. Without a modal it becomes a toast;
// with a modal open it goes to the modal content (the viewer shows it in its
// command line, since the toast row is covered).
type copyResultMsg struct {
	text  string
	error bool
}

// copyPanelCommand puts the active panel's command on the clipboard.
func (a *App) copyPanelCommand() tea.Cmd {
	command := a.panelCommand()
	if command == "" {
		return nil
	}
	return a.copyText(command, command)
}

// copyText puts text on the clipboard: a local clipboard tool (pbcopy,
// wl-copy, xclip, xsel) when kc runs locally, OSC 52 over SSH or when no
// tool exists, and, inside tmux, the tmux paste buffer as well. The confirmation names the path used and what was copied; OSC 52
// cannot confirm that the terminal accepted the text.
func (a *App) copyText(text, what string) tea.Cmd {
	if text == "" {
		return nil
	}
	plan := planCopy(os.Getenv, exec.LookPath)
	var cmds []tea.Cmd
	if plan.osc52 {
		cmds = append(cmds, tea.Raw(osc52Sequence(text, os.Getenv)))
	}
	cmds = append(cmds, func() tea.Msg {
		if plan.tmux {
			_ = runWithInput(text, "tmux", "load-buffer", "-")
		}
		via := "OSC 52 (the terminal must allow clipboard access)"
		if plan.tool != nil {
			if err := runWithInput(text, plan.tool...); err != nil {
				return copyResultMsg{text: "Copy failed: " + err.Error(), error: true}
			}
			via = plan.tool[0]
		}
		if plan.tmux {
			via += " and the tmux buffer"
		}
		return copyResultMsg{text: "Copied " + what + " via " + via}
	})
	return tea.Batch(cmds...)
}

// copyWhat describes copied text for the confirmation message.
func copyWhat(req viewer.CopyRequestMsg) string {
	lines := strings.Count(req.Text, "\n") + 1
	if lines > 1 {
		return fmt.Sprintf("%s (%d lines)", req.What, lines)
	}
	return fmt.Sprintf("%s (%d chars)", req.What, len([]rune(req.Text)))
}

type copyPlan struct {
	tool  []string // local clipboard tool, nil when not used
	osc52 bool
	tmux  bool
}

func planCopy(getenv func(string) string, lookPath func(string) (string, error)) copyPlan {
	plan := copyPlan{tmux: getenv("TMUX") != ""}
	if !isSSHSession(getenv) {
		for _, tool := range clipboardTools {
			if _, err := lookPath(tool[0]); err == nil {
				plan.tool = tool
				break
			}
		}
	}
	plan.osc52 = plan.tool == nil
	return plan
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

func runWithInput(text string, argv ...string) error {
	// #nosec G204 -- argv is one of the fixed clipboardTools entries.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
