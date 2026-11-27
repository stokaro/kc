package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/pkg/appconfig"
)

// configErrorMaxRows bounds the configuration error bar; a longer text ends
// in "…" and the log keeps all of it.
const configErrorMaxRows = 2

// configErrorRows is the height of the configuration error bar between the
// panels and the command line.
func (a *App) configErrorRows() int {
	return len(a.configErrorLines())
}

// configErrorStyle draws the bar in the colors of the RO marker of the
// panel frames, darker than the red of an error message, so a message on
// the command line right under it reads as a separate line.
var configErrorStyle = scopeReadOnlyStyle

// renderConfigError draws the rows of the configuration error bar; none
// while there is no error.
func (a *App) renderConfigError() []string {
	lines := a.configErrorLines()
	for i, line := range lines {
		lines[i] = configErrorStyle.Width(a.width).Render(line)
	}
	return lines
}

// configErrorLines is the text of the configuration error bar, wrapped to
// the screen. The bar shows while the configuration file has not loaded in
// full, which is the whole session: kc reads the file once, at start. It
// shows in the two-panel view, as the command line does.
func (a *App) configErrorLines() []string {
	if a.cfgLoadErr == nil || a.showTerminal || a.height < 8 || a.width <= 0 {
		return nil
	}
	maxRows := configErrorMaxRows
	if a.height < 16 {
		maxRows = 1
	}
	home, _ := os.UserHomeDir()
	text := configErrorText(a.cfgLoadErr, a.readOnlyOverride, home)
	lines := strings.Split(ansi.Wrap(text, a.width, ""), "\n")
	if len(lines) > maxRows {
		rest := strings.Join(lines[maxRows-1:], " ")
		lines = append(lines[:maxRows-1], ansi.Truncate(rest, a.width, "…"))
	}
	return lines
}

// configErrorText says what kc does about a configuration file that did not
// load in full, which file it is and what is wrong, in that order, so the
// part that matters most survives a narrow screen:
//
//	Config not loaded, every context read-only: ~/.kc/config.yaml: yaml: line 2: ...
//	Config layout not loaded, using its defaults: ~/.kc/config.yaml: layout.rightPanelMode: ...
func configErrorText(le *appconfig.LoadError, override ReadOnlyOverride, home string) string {
	var b strings.Builder
	b.WriteString("Config ")
	if len(le.Sections) > 0 {
		b.WriteString(strings.Join(le.Sections, ", "))
		b.WriteString(" ")
	}
	b.WriteString("not loaded, ")
	switch {
	case le.ReadOnlyLost() && override != ReadOnlyWriteForced:
		b.WriteString("every context read-only")
	case len(le.Sections) == 1:
		b.WriteString("using its defaults")
	case len(le.Sections) > 1:
		b.WriteString("using their defaults")
	default:
		b.WriteString("using the defaults")
	}
	b.WriteString(": ")
	b.WriteString(homeRelative(le.Path, home))
	b.WriteString(": ")
	b.WriteString(le.Reason())
	return b.String()
}

// homeRelative writes a path under home as ~/...
func homeRelative(path, home string) string {
	if home == "" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}
