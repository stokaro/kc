package viewer

import (
	"strings"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// Focus order of the search dialog fields, as in Midnight Commander: the
// input, the mode radio group, the option check boxes, then the buttons.
const (
	searchFocusInput = iota
	searchFocusNormal
	searchFocusRegex
	searchFocusWildcard
	searchFocusCase
	searchFocusBackwards
	searchFocusWholeWords
	searchFocusOK
	searchFocusCancel
	searchFocusCount
)

var (
	searchDialogBg     = lipgloss.Color(uistyles.ColorGrey)
	searchDialogStyle  = lipgloss.NewStyle().Background(searchDialogBg).Foreground(lipgloss.Black)
	searchDialogTitle  = lipgloss.NewStyle().Background(searchDialogBg).Foreground(lipgloss.Blue)
	searchDialogFocus  = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black)
	searchDialogInput  = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black)
	searchDialogShadow = lipgloss.NewStyle().Background(lipgloss.Black)
)

// searchDialog is the modal search form shown over the viewer.
type searchDialog struct {
	input textinput.Model
	opts  SearchOptions
	focus int
	err   string
}

type searchDialogResult int

const (
	searchDialogOpen searchDialogResult = iota
	searchDialogAccepted
	searchDialogCanceled
)

func newSearchDialog(query string, opts SearchOptions) *searchDialog {
	ti := textinput.New()
	ti.Prompt = ""
	st := textinput.DefaultLightStyles()
	st.Focused.Text = searchDialogInput
	st.Focused.Prompt = searchDialogInput
	st.Focused.Placeholder = searchDialogInput
	st.Blurred = st.Focused
	st.Cursor.Color = lipgloss.Black
	ti.SetStyles(st)
	ti.SetVirtualCursor(true)
	ti.SetValue(query)
	ti.CursorEnd()
	return &searchDialog{input: ti, opts: opts}
}

func (d *searchDialog) focusCmd() tea.Cmd {
	return d.input.Focus()
}

func (d *searchDialog) setFocus(f int) tea.Cmd {
	d.focus = (f + searchFocusCount) % searchFocusCount
	if d.focus == searchFocusInput {
		return d.input.Focus()
	}
	d.input.Blur()
	return nil
}

// update handles a key and reports whether the dialog was accepted or canceled.
func (d *searchDialog) update(key tea.KeyMsg) (searchDialogResult, tea.Cmd) {
	switch key.String() {
	case "esc", "ctrl+c":
		return searchDialogCanceled, nil
	case "enter":
		if d.focus == searchFocusCancel {
			return searchDialogCanceled, nil
		}
		return searchDialogAccepted, nil
	case "tab":
		return searchDialogOpen, d.setFocus(d.focus + 1)
	case "shift+tab":
		return searchDialogOpen, d.setFocus(d.focus - 1)
	}

	if d.focus == searchFocusInput {
		if key.String() == "down" {
			return searchDialogOpen, d.setFocus(searchFocusNormal)
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(key)
		d.err = ""
		return searchDialogOpen, cmd
	}

	switch key.String() {
	case "up":
		return searchDialogOpen, d.setFocus(d.focus - 1)
	case "down":
		return searchDialogOpen, d.setFocus(d.focus + 1)
	case "left":
		if d.focus >= searchFocusCase && d.focus <= searchFocusWholeWords {
			return searchDialogOpen, d.setFocus(d.focus - 3)
		}
		if d.focus == searchFocusCancel {
			return searchDialogOpen, d.setFocus(searchFocusOK)
		}
	case "right":
		if d.focus >= searchFocusNormal && d.focus <= searchFocusWildcard {
			return searchDialogOpen, d.setFocus(d.focus + 3)
		}
		if d.focus == searchFocusOK {
			return searchDialogOpen, d.setFocus(searchFocusCancel)
		}
	case "space", " ":
		d.toggle()
	}
	return searchDialogOpen, nil
}

func (d *searchDialog) toggle() {
	switch d.focus {
	case searchFocusNormal:
		d.opts.Mode = SearchNormal
	case searchFocusRegex:
		d.opts.Mode = SearchRegex
	case searchFocusWildcard:
		d.opts.Mode = SearchWildcard
	case searchFocusCase:
		d.opts.CaseSensitive = !d.opts.CaseSensitive
	case searchFocusBackwards:
		d.opts.Backwards = !d.opts.Backwards
	case searchFocusWholeWords:
		d.opts.WholeWords = !d.opts.WholeWords
	}
}

func (d *searchDialog) query() string { return d.input.Value() }

// view renders the dialog box (with a shadow) for a viewer of the given width.
func (d *searchDialog) view(maxWidth int) string {
	width := min(64, max(30, maxWidth-4))
	inner := width - 2

	line := func(segments ...string) string {
		body := strings.Join(segments, "")
		pad := max(0, inner-lipgloss.Width(body))
		return searchDialogStyle.Render("│") + body + searchDialogStyle.Render(strings.Repeat(" ", pad)+"│")
	}
	rule := func(left, right string) string {
		return searchDialogStyle.Render(left + strings.Repeat("─", inner) + right)
	}
	text := func(s string) string { return searchDialogStyle.Render(s) }

	title := " Search "
	leftRule := (inner - lipgloss.Width(title)) / 2
	top := searchDialogStyle.Render("┌"+strings.Repeat("─", leftRule)) +
		searchDialogTitle.Render(title) +
		searchDialogStyle.Render(strings.Repeat("─", inner-leftRule-lipgloss.Width(title))+"┐")

	d.input.SetWidth(max(1, inner-3))
	input := searchDialogInput.Width(inner - 2).Render(d.input.View())

	item := func(focus int, mark, label string) string {
		s := mark + " " + label
		if d.focus == focus {
			return searchDialogFocus.Render(s)
		}
		return text(s)
	}
	radio := func(on bool) string {
		if on {
			return "(*)"
		}
		return "( )"
	}
	check := func(on bool) string {
		if on {
			return "[x]"
		}
		return "[ ]"
	}
	colWidth := 25
	row := func(left, right string) string {
		gap := max(1, colWidth-lipgloss.Width(left))
		return line(text(" "), left, text(strings.Repeat(" ", gap)), right)
	}

	okLabel := "[< OK >]"
	cancelLabel := "[ Cancel ]"
	ok := text(okLabel)
	if d.focus == searchFocusOK {
		ok = searchDialogFocus.Render(okLabel)
	}
	cancel := text(cancelLabel)
	if d.focus == searchFocusCancel {
		cancel = searchDialogFocus.Render(cancelLabel)
	}
	buttons := ok + text(" ") + cancel
	buttonPad := max(0, (inner-lipgloss.Width(buttons))/2)

	prompt := " Enter search string:"
	if d.err != "" {
		prompt = " " + d.err
	}

	lines := []string{
		top,
		line(text(truncateRunes(prompt, inner))),
		line(text(" "), input),
		rule("├", "┤"),
		row(item(searchFocusNormal, radio(d.opts.Mode == SearchNormal), "Normal"), item(searchFocusCase, check(d.opts.CaseSensitive), "Case sensitive")),
		row(item(searchFocusRegex, radio(d.opts.Mode == SearchRegex), "Regular expression"), item(searchFocusBackwards, check(d.opts.Backwards), "Backwards")),
		row(item(searchFocusWildcard, radio(d.opts.Mode == SearchWildcard), "Wildcard search"), item(searchFocusWholeWords, check(d.opts.WholeWords), "Whole words")),
		rule("├", "┤"),
		line(text(strings.Repeat(" ", buttonPad)), buttons),
		rule("└", "┘"),
	}
	// One-column shadow on the right and a shadow row below, like mc.
	for i := 1; i < len(lines); i++ {
		lines[i] += searchDialogShadow.Render(" ")
	}
	lines[0] += " "
	lines = append(lines, " "+searchDialogShadow.Render(strings.Repeat(" ", width)))
	return strings.Join(lines, "\n")
}

func truncateRunes(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > width {
		r = r[:len(r)-1]
	}
	return string(r)
}
