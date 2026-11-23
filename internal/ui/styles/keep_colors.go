package styles

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// KeepPanelColors restores the panel's colors after every full reset
// inside line. A styled segment such as a red state word ends with a
// reset, which also clears the background; text after it would otherwise
// show the terminal's own background inside the blue panel.
func KeepPanelColors(line string) string {
	if !strings.Contains(line, "\x1b[") {
		return line
	}
	base := panelColors()
	if base == "" {
		return line
	}
	var b strings.Builder
	for line != "" {
		i, n := nextReset(line)
		if i < 0 {
			b.WriteString(line)
			break
		}
		b.WriteString(line[:i+n])
		line = line[i+n:]
		if line != "" {
			b.WriteString(base)
		}
	}
	return b.String()
}

// nextReset finds the next full reset, ESC[m or ESC[0m, and its length.
func nextReset(s string) (at, size int) {
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], "\x1b[")
		if j < 0 {
			return -1, 0
		}
		j += i
		switch {
		case strings.HasPrefix(s[j:], "\x1b[m"):
			return j, 3
		case strings.HasPrefix(s[j:], "\x1b[0m"):
			return j, 4
		}
		i = j + 2
	}
	return -1, 0
}

// panelColors is the sequence that sets the panel's foreground and
// background.
func panelColors() string {
	const mark = "\x00"
	out := lipgloss.NewStyle().Background(current.Background).Foreground(current.Foreground).Render(mark)
	i := strings.Index(out, mark)
	if i <= 0 {
		return ""
	}
	return out[:i]
}
