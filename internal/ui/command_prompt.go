package ui

import (
	"strings"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/dialog"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
	"github.com/stokaro/kc/pkg/appconfig"
)

// commandPromptModal is the modal name of the prompt dialog.
const commandPromptModal = "command_prompt"

// commandPromptField is one prompt: a text field, or a list of choices
// that Left/Right/Space cycle through.
type commandPromptField struct {
	prompt appconfig.CommandPrompt
	input  textinput.Model
	choice int
}

func (f *commandPromptField) hasChoices() bool { return len(f.prompt.Choices) > 0 }

func (f *commandPromptField) value() string {
	if f.hasChoices() {
		return f.prompt.Choices[f.choice]
	}
	return f.input.Value()
}

func (f *commandPromptField) label() string {
	if l := strings.TrimSpace(f.prompt.Label); l != "" {
		return l
	}
	return f.prompt.Name
}

// CommandPromptModel asks the prompts of a custom command in one dialog,
// as mc does for its user menu. Enter on a field or on OK answers, Esc or
// Cancel drops the command. Values are not trimmed: the user may want a
// leading or trailing space.
type CommandPromptModel struct {
	fields        []commandPromptField
	focus         int // fields, then OK, then Cancel
	width, height int
	buttons       [2]buttonRect
	onDone        func(map[string]string) tea.Cmd
	onCancel      func() tea.Cmd
}

// NewCommandPromptModel builds the dialog for prompts. defaults holds the
// rendered default of each prompt by name; a choice prompt without one
// starts on its first choice.
func NewCommandPromptModel(prompts []appconfig.CommandPrompt, defaults map[string]string, onDone func(map[string]string) tea.Cmd, onCancel func() tea.Cmd) *CommandPromptModel {
	m := &CommandPromptModel{onDone: onDone, onCancel: onCancel}
	for _, p := range prompts {
		f := commandPromptField{prompt: p}
		def := defaults[p.Name]
		if f.hasChoices() {
			for i, c := range p.Choices {
				if c == def {
					f.choice = i
				}
			}
		} else {
			f.input = textinput.New()
			f.input.Prompt = ""
			f.input.CharLimit = 0
			f.input.SetVirtualCursor(false)
			f.input.SetStyles(dialog.InputStyles(dialog.Normal()))
			f.input.SetValue(def)
			f.input.CursorEnd()
		}
		m.fields = append(m.fields, f)
	}
	m.setFocus(0)
	return m
}

func (m *CommandPromptModel) Init() tea.Cmd          { return nil }
func (m *CommandPromptModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// Values returns the current answers by prompt name.
func (m *CommandPromptModel) Values() map[string]string {
	out := make(map[string]string, len(m.fields))
	for i := range m.fields {
		out[m.fields[i].prompt.Name] = m.fields[i].value()
	}
	return out
}

// HandleModalEscape cancels on a single Esc.
func (m *CommandPromptModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) {
	return true, m.cancel()
}

func (m *CommandPromptModel) okIndex() int     { return len(m.fields) }
func (m *CommandPromptModel) cancelIndex() int { return len(m.fields) + 1 }

func (m *CommandPromptModel) setFocus(i int) tea.Cmd {
	n := len(m.fields) + 2
	m.focus = ((i % n) + n) % n
	var cmd tea.Cmd
	for j := range m.fields {
		f := &m.fields[j]
		if f.hasChoices() {
			continue
		}
		if j == m.focus {
			cmd = f.input.Focus()
		} else {
			f.input.Blur()
		}
	}
	return cmd
}

func (m *CommandPromptModel) submit() tea.Cmd {
	if m.onDone == nil {
		return nil
	}
	return m.onDone(m.Values())
}

func (m *CommandPromptModel) cancel() tea.Cmd {
	if m.onCancel == nil {
		return nil
	}
	return m.onCancel()
}

func (m *CommandPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab", "down":
			return m, m.setFocus(m.focus + 1)
		case "shift+tab", "up":
			return m, m.setFocus(m.focus - 1)
		case "ctrl+c", "ctrl+g":
			return m, m.cancel()
		case "enter":
			if m.focus == m.cancelIndex() {
				return m, m.cancel()
			}
			return m, m.submit()
		}
		if m.focus >= len(m.fields) {
			switch msg.String() {
			case "left", "right":
				if m.focus == m.okIndex() {
					return m, m.setFocus(m.cancelIndex())
				}
				return m, m.setFocus(m.okIndex())
			}
			return m, nil
		}
		f := &m.fields[m.focus]
		if f.hasChoices() {
			switch msg.String() {
			case "right", "space", " ":
				f.choice = (f.choice + 1) % len(f.prompt.Choices)
			case "left":
				f.choice = (f.choice - 1 + len(f.prompt.Choices)) % len(f.prompt.Choices)
			}
			return m, nil
		}
		var cmd tea.Cmd
		f.input, cmd = f.input.Update(msg)
		return m, cmd
	case tea.MouseReleaseMsg:
		mouse := msg.Mouse()
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		if m.buttons[0].contains(mouse.X, mouse.Y) {
			return m, m.submit()
		}
		if m.buttons[1].contains(mouse.X, mouse.Y) {
			return m, m.cancel()
		}
	}
	return m, nil
}

// FooterHints implements ModalFooterHints.
func (m *CommandPromptModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Tab", Label: "Next", Enabled: true},
		{Key: "Enter", Label: "Run", Enabled: true},
		{Key: "Esc", Label: "Cancel", Enabled: true},
	}
}

// size is the content size the dialog wants: a label and a field line per
// prompt, a blank line and the buttons.
func (m *CommandPromptModel) size() (width, height int) {
	w := 40
	for i := range m.fields {
		f := &m.fields[i]
		w = max(w, max(lipgloss.Width(f.label())+4, lipgloss.Width(f.value())+8))
		for _, c := range f.prompt.Choices {
			w = max(w, lipgloss.Width(c)+12)
		}
	}
	return w, 2*len(m.fields) + 2
}

func (m *CommandPromptModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width, _ = m.size()
	}
	bg := lipgloss.Color(uistyles.ColorModalBg)
	fg := lipgloss.Color(uistyles.ColorModalFg)
	row := lipgloss.NewStyle().Background(bg).Foreground(fg).Width(width)
	const pad = 2
	fieldWidth := max(10, width-2*pad)
	skin := dialog.Normal()
	fieldBg := skin.Input
	focusedChoice := fieldBg.Bold(true)
	blurredChoice := fieldBg
	padding := lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", pad))

	var lines []string
	var cursor *tea.Cursor
	for i := range m.fields {
		f := &m.fields[i]
		lines = append(lines, row.Bold(true).Render(truncateStringToWidth(" "+f.label()+":", width)))
		var field string
		if f.hasChoices() {
			style := blurredChoice
			if i == m.focus {
				style = focusedChoice
			}
			text := truncateStringToWidth("< "+f.value()+" >", fieldWidth)
			field = style.Width(fieldWidth).Render(text)
		} else {
			f.input.SetWidth(fieldWidth - 1)
			field = fieldBg.Width(fieldWidth).Render(f.input.View())
			if i == m.focus {
				if c := f.input.Cursor(); c != nil {
					c.Position.X += pad
					c.Position.Y += len(lines)
					cursor = c
				}
			}
		}
		lines = append(lines, row.Render(padding+field))
	}
	lines = append(lines, row.Render(""))

	focusedButton := -1
	if m.focus >= len(m.fields) {
		focusedButton = m.focus - len(m.fields)
	}
	buttonRow, rects := dialog.RenderButtons(skin, width, []dialog.Button{
		{Label: "OK", Default: true}, {Label: "Cancel"},
	}, focusedButton)
	for i, r := range rects {
		m.buttons[i] = buttonRect{x: r.X, y: len(lines), w: r.W, h: 1}
	}
	lines = append(lines, buttonRow)
	return viewWithCursor(strings.Join(lines, "\n"), cursor)
}
