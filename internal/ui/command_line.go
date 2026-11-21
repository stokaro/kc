package ui

import (
	"errors"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
		hint = commandLineStyle.Render("Copy ") + commandLineKey.Render("Alt+C") + commandLineStyle.Render(" ")
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

// copyPanelCommand puts the active panel's command on the clipboard. It sends
// OSC 52 (works over SSH in terminals that allow it) and also runs a local
// clipboard tool when one exists, because Terminal.app ignores OSC 52.
func (a *App) copyPanelCommand() tea.Cmd {
	command := a.panelCommand()
	if command == "" {
		return nil
	}
	local := func() tea.Msg {
		_ = copyWithLocalTool(command)
		return nil
	}
	return tea.Batch(tea.SetClipboard(command), local, a.ShowToast("Copied: "+command, 2*time.Second))
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
