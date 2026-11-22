package menubar_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/menubar"
)

// SGR parameters of mc's default [menu] skin, as lipgloss writes them.
const (
	sgrText     = "97;46" // white on cyan: titles, entries, shortcuts, frame
	sgrHot      = "93;46" // yellow on cyan: hotkey letters
	sgrSel      = "97;40" // white on black: the selected title and entry
	sgrSelHot   = "93;40" // yellow on black: the hotkey of the selection
	sgrDisabled = "90;46" // gray on cyan: entries that cannot be chosen
	sgrIdle     = "30;46" // black on cyan: the closed bar, no hotkeys
)

// cell is one printed character of a rendered line and the SGR parameters
// it was drawn with.
type cell struct {
	r   rune
	sgr string
}

// cells splits a rendered line into its characters with their SGR state.
// It understands the plain "ESC [ params m" sequences lipgloss writes.
func cells(t *testing.T, line string) []cell {
	t.Helper()
	var out []cell
	sgr := ""
	for len(line) > 0 {
		if strings.HasPrefix(line, "\x1b[") {
			end := strings.IndexByte(line, 'm')
			if end < 0 {
				t.Fatalf("unterminated escape in %q", line)
			}
			sgr = line[2:end]
			line = line[end+1:]
			continue
		}
		r := []rune(line)[0]
		out = append(out, cell{r: r, sgr: sgr})
		line = line[len(string(r)):]
	}
	return out
}

// styleOf returns the SGR parameters of the first cell of text in line.
func styleOf(t *testing.T, line, text string) string {
	t.Helper()
	cs := cells(t, line)
	want := []rune(text)
	for i := 0; i+len(want) <= len(cs); i++ {
		match := true
		for j, r := range want {
			if cs[i+j].r != r {
				match = false
				break
			}
		}
		if match {
			return cs[i].sgr
		}
	}
	t.Fatalf("%q not found in %q", text, ansi.Strip(line))
	return ""
}

func colorMenus() []menubar.Menu {
	run := func() tea.Cmd { return nil }
	return []menubar.Menu{
		{Title: "&Left", Items: []menubar.Item{
			{Label: "&List", Shortcut: "C-x q", Run: run},
			{Label: "E&xplain", Shortcut: "M-e", Run: run},
			menubar.Separator(),
			{Label: "&Reread", Shortcut: "C-r", Disabled: true, Run: run},
		}},
		{Title: "&File", Items: []menubar.Item{{Label: "&View", Run: run}}},
	}
}

func TestRenderTitlesColors(t *testing.T) {
	line := menubar.RenderTitles(colorMenus(), 40)
	if got := ansi.StringWidth(line); got != 40 {
		t.Fatalf("width %d, want 40", got)
	}
	tests := []struct {
		name, text, want string
	}{
		{name: "title text", text: "eft", want: sgrIdle},
		{name: "title hotkey", text: "L", want: sgrIdle},
		{name: "second title hotkey", text: "F", want: sgrIdle},
		{name: "bar padding", text: " ", want: sgrIdle},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := styleOf(t, line, tc.text); got != tc.want {
				t.Fatalf("%q drawn with %q, want %q", tc.text, got, tc.want)
			}
		})
	}
	if got := menubar.TitleIndex(colorMenus(), strings.Index(ansi.Strip(line), "File")); got != 1 {
		t.Fatalf("TitleIndex on File = %d, want 1", got)
	}
}

func TestOpenMenuColors(t *testing.T) {
	var b menubar.Bar
	b.OpenAt(colorMenus(), 40, 0)
	bar := b.RenderBar(40)
	drop := strings.Split(b.RenderDropdown(), "\n")
	// drop: top frame, List, Explain, separator, Reread, bottom frame.
	if len(drop) != 6 {
		t.Fatalf("drop-down has %d lines, want 6", len(drop))
	}
	tests := []struct {
		name, line, text, want string
	}{
		{name: "open title", line: bar, text: "eft", want: sgrSel},
		{name: "open title hotkey", line: bar, text: "L", want: sgrSelHot},
		{name: "other title", line: bar, text: "ile", want: sgrText},
		{name: "other title hotkey", line: bar, text: "F", want: sgrHot},
		{name: "top frame", line: drop[0], text: "┌", want: sgrText},
		{name: "frame side", line: drop[2], text: "│", want: sgrText},
		{name: "selected entry", line: drop[1], text: "ist", want: sgrSel},
		{name: "selected hotkey", line: drop[1], text: "L", want: sgrSelHot},
		{name: "selected shortcut", line: drop[1], text: "C-x q", want: sgrSel},
		{name: "entry text", line: drop[2], text: "E", want: sgrText},
		{name: "entry hotkey", line: drop[2], text: "x", want: sgrHot},
		{name: "entry shortcut", line: drop[2], text: "M-e", want: sgrText},
		{name: "separator tee", line: drop[3], text: "├", want: sgrText},
		{name: "separator line", line: drop[3], text: "─", want: sgrText},
		{name: "separator right tee", line: drop[3], text: "┤", want: sgrText},
		{name: "disabled entry", line: drop[4], text: "Reread", want: sgrDisabled},
		{name: "disabled shortcut", line: drop[4], text: "C-r", want: sgrDisabled},
		{name: "bottom frame", line: drop[5], text: "└", want: sgrText},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := styleOf(t, tc.line, tc.text); got != tc.want {
				t.Fatalf("%q drawn with %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
