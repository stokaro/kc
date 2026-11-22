package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/viewer"
)

// copyAllKey copies the whole viewer content.
const copyAllKey = "alt+a"

// viewerFlashTTL is how long a copy confirmation replaces the copy hint.
const viewerFlashTTL = 2 * time.Second

type viewerFlashExpiredMsg struct{ seq int }

// viewerCommandBar is the "Command:" line at the bottom of a viewer: the
// kubectl command that prints the same content, a copy button, and room for
// copy confirmations (the app's toast row is hidden behind the viewer).
type viewerCommandBar struct {
	command  string
	flash    string
	flashErr bool
	flashSeq int
}

func (b *viewerCommandBar) rows(height int) int {
	if height < 3 {
		return 0
	}
	return 1
}

func (b *viewerCommandBar) render(width int) string {
	if b.flash == "" {
		return renderCommandLine(b.command, width)
	}
	style := commandLineStyle.Foreground(lipgloss.BrightCyan)
	if b.flashErr {
		style = commandLineStyle.Foreground(lipgloss.BrightRed)
	}
	text := " " + b.flash
	if lipgloss.Width(text) > width {
		text = truncateStringToWidth(text, width)
	}
	return style.Width(width).Render(text)
}

// handle processes copy keys, clicks on the bar and copy results. inner is
// the viewer widget; width and height are the full content area.
func (b *viewerCommandBar) handle(msg tea.Msg, inner *viewer.Widget, width, height int) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case copyResultMsg:
		b.flash, b.flashErr = m.text, m.error
		b.flashSeq++
		seq := b.flashSeq
		return tea.Tick(viewerFlashTTL, func(time.Time) tea.Msg { return viewerFlashExpiredMsg{seq: seq} }), true
	case viewerFlashExpiredMsg:
		if m.seq == b.flashSeq {
			b.flash = ""
		}
		return nil, true
	case tea.KeyMsg:
		if inner.SearchMode() {
			return nil, false
		}
		switch m.String() {
		case copyCommandKey:
			return viewerCopyRequest(b.command, "command"), b.command != ""
		case copyAllKey:
			return viewerCopyRequest(inner.PlainText(), "output"), true
		}
	case tea.MouseClickMsg, tea.MouseReleaseMsg:
		mouse := m.(tea.MouseMsg).Mouse()
		if b.rows(height) == 0 || mouse.Y != height-1 || inner.Selecting() {
			return nil, false
		}
		if _, release := m.(tea.MouseReleaseMsg); release && mouse.Button == tea.MouseLeft &&
			b.command != "" && mouse.X >= width-lipgloss.Width(renderCopyHint()) {
			return viewerCopyRequest(b.command, "command"), true
		}
		return nil, true // clicks on the bar do not reach the text
	}
	return nil, false
}

func viewerCopyRequest(text, what string) tea.Cmd {
	if text == "" {
		return nil
	}
	return func() tea.Msg { return viewer.CopyRequestMsg{Text: text, What: what} }
}

// viewerHints are the copy hints shared by the viewers' footers.
func viewerCopyHints(hasCommand bool) []FooterHint {
	return []FooterHint{
		{Key: "M-a", Label: "Copy all", Enabled: true},
		{Key: "M-c", Label: "Copy cmd", Enabled: hasCommand},
	}
}
