package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/dialog"
)

const (
	questionYes = "yes"
	questionNo  = "no"
	// questionMinWidth and questionMaxWidth bound the content width of a
	// question; longer notes wrap.
	questionMinWidth = 40
	questionMaxWidth = 72
)

// questionModel is a red Yes/No dialog that asks before kc ends or starts
// something that cannot be taken back: quitting while port forwards run,
// attaching to a container's main process (#375). It keeps the red-dialog
// rule of ADR 0373: a letter alone does nothing, M-y and M-n answer, Enter
// presses the focused button, No has the focus when the dialog opens, and
// Esc, C-c, C-g and F10 answer No. onAnswer gets the answer once; it hides
// the dialog and runs the step.
type questionModel struct {
	title string
	// details are label and value lines under the title, such as the
	// context; items are a list after them, such as the forwards that
	// stop; notes are paragraphs at the end, wrapped at the dialog width.
	details  [][2]string
	items    []string
	notes    []string
	onAnswer func(yes bool) tea.Cmd

	form          dialog.Form
	width, height int
	rules         []int
	answered      bool
}

func newQuestionModel(title string, onAnswer func(yes bool) tea.Cmd) *questionModel {
	m := &questionModel{title: title, onAnswer: onAnswer, form: dialog.Form{Skin: dialog.Danger(), AltOnly: true}}
	m.syncItems(questionMaxWidth)
	m.form.FocusButton(questionNo)
	return m
}

func (m *questionModel) Init() tea.Cmd          { return nil }
func (m *questionModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// RuleRows implements ModalRules.
func (m *questionModel) RuleRows() []int { return m.rules }

// HandleModalEscape answers No on a single Esc.
func (m *questionModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) { return true, m.answer(false) }

func (m *questionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+c", "ctrl+g", "f10":
			return m, m.answer(false)
		}
	}
	ev, cmd := m.form.Update(msg)
	switch ev.Pressed {
	case questionYes:
		return m, m.answer(true)
	case questionNo:
		return m, m.answer(false)
	}
	return m, cmd
}

// answer reports the answer once: a key that arrives before the dialog
// is gone must not run the step twice.
func (m *questionModel) answer(yes bool) tea.Cmd {
	if m.answered || m.onAnswer == nil {
		return nil
	}
	m.answered = true
	return m.onAnswer(yes)
}

// syncItems lays the dialog out for a content width of width columns.
func (m *questionModel) syncItems(width int) {
	inner := max(1, width-2)
	items := []dialog.Item{dialog.Text{Text: m.title, Center: true, Bold: true}}
	if len(m.details) > 0 {
		items = append(items, dialog.Text{})
		for _, line := range detailLines(m.details) {
			items = append(items, dialog.Text{Text: line})
		}
	}
	if len(m.items) > 0 {
		items = append(items, dialog.Text{})
		for _, it := range m.items {
			items = append(items, dialog.Text{Text: "  " + it})
		}
	}
	for _, note := range m.notes {
		items = append(items, dialog.Text{})
		for line := range strings.SplitSeq(ansi.Wrap(note, inner, ""), "\n") {
			items = append(items, dialog.Text{Text: line})
		}
	}
	items = append(items, dialog.Rule{}, dialog.Buttons{
		{ID: questionYes, Label: "&Yes"},
		{ID: questionNo, Label: "&No", Default: true},
	})
	m.form.Items = items
}

// PreferredSize implements ModalSizer: wide enough for the title, the
// details and the list, the notes wrapped at questionMaxWidth.
func (m *questionModel) PreferredSize(maxContentWidth, _ int) (width, height int) {
	width = lipgloss.Width(m.title) + 2
	for _, line := range detailLines(m.details) {
		width = max(width, lipgloss.Width(line)+2)
	}
	for _, it := range m.items {
		width = max(width, lipgloss.Width(it)+4)
	}
	for _, note := range m.notes {
		width = max(width, min(lipgloss.Width(note)+2, questionMaxWidth))
	}
	width = max(width, questionMinWidth)
	if maxContentWidth > 0 {
		width = min(width, maxContentWidth)
	}
	m.syncItems(width)
	return width, m.form.Height()
}

func (m *questionModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width, _ = m.PreferredSize(0, 0)
	}
	m.syncItems(width)
	content, cursor, rules := m.form.View(width)
	m.rules = rules
	return viewWithCursor(content, cursor)
}

// newQuestionModal frames m as a red dialog. The caller registers it under
// a constant name, which help_context_test.go maps to a help page.
func newQuestionModal(frameTitle string, m *questionModel) *Modal {
	modal := NewModal(frameTitle, m)
	modal.SetDanger(true)
	return modal
}

// showQuestion opens the registered question name over the panel
// panelIdx, or over the middle of the screen when panelIdx is negative.
func (a *App) showQuestion(name string, modal *Modal, m *questionModel, panelIdx int) {
	a.configureModalWindow(modal, m, panelIdx, "", 0, 0)
	a.showModal(name)
}
