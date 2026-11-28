package ui

import (
	"fmt"
	"unicode"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/dialog"
)

// DeleteConfirmMsg signals the result of the delete confirmation dialog.
type DeleteConfirmMsg struct {
	Confirm bool
	Close   bool
}

// DeleteConfirmModel is the red Yes/No dialog of F8. It asks about one
// object, or about the marked rows: their count, kind and first names.
// For an object whose delete takes others with it (a namespace), it also
// shows what goes with it and keeps Yes disabled until the user typed the
// object's name (#374).
type DeleteConfirmModel struct {
	width, height int
	title         string // the question, "Delete pod shop/web-1?"
	context       string
	// names are the objects of a marked set the dialog lists, more the
	// number of objects the set holds beyond them.
	names      []string
	more       int
	focus      int // 0=yes, 1=no
	buttonRect [2]buttonRect

	// typed is the name the user must type before Yes works; empty when
	// the dialog asks for no name.
	typed string
	input textinput.Model
	// focusChosen reports that the user moved the focus (Tab, an arrow
	// key, a click) since the name last matched. Until then the first
	// Enter on No moves the focus to Yes instead of pressing No.
	focusChosen bool
	// impactSeq identifies the count of what goes with the object that
	// the dialog waits for; zero when it waits for none. impact is the
	// result, nil while kc counts; counting is what the dialog says
	// meanwhile.
	impactSeq uint64
	impact    *deleteImpact
	counting  string
}

type buttonRect struct{ x, y, w, h int }

func (r buttonRect) contains(px, py int) bool {
	return px >= r.x && px < r.x+r.w && py >= r.y && py < r.y+r.h
}

func NewDeleteConfirmModel() *DeleteConfirmModel {
	return &DeleteConfirmModel{focus: 1, input: dialog.NewInput(dialog.Danger(), "")}
}

func (m *DeleteConfirmModel) Init() tea.Cmd          { return nil }
func (m *DeleteConfirmModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// Configure asks about one object, target ("pod shop/web-1"). context is
// the kubeconfig context the object was read from; empty hides the line.
func (m *DeleteConfirmModel) Configure(target, context string) {
	m.ConfigureSet(fmt.Sprintf("Delete %s?", target), context, nil, 0)
}

// ConfigureSet asks title about a set of objects, listing names and
// saying how many more the set holds.
func (m *DeleteConfirmModel) ConfigureSet(title, context string, names []string, more int) {
	m.title = title
	m.context = context
	m.names = append([]string(nil), names...)
	m.more = more
	m.focus = 1 // default to "No"
	m.focusChosen = false
	m.typed = ""
	m.input.SetValue("")
	m.input.Blur()
	m.forgetImpact()
}

// RequireName keeps Yes disabled until name is typed exactly, and shows
// counting until SetImpact delivers the result of count seq. Call it
// after Configure or ConfigureSet.
func (m *DeleteConfirmModel) RequireName(name string, seq uint64, counting string) {
	m.typed = name
	m.input.SetValue("")
	m.input.Focus()
	m.impactSeq, m.impact, m.counting = seq, nil, counting
	m.focus, m.focusChosen = 1, false
}

// SetImpact shows impact, the result of count seq. It reports false and
// changes nothing when the dialog no longer waits for that count: it was
// closed, or opened again for another object.
func (m *DeleteConfirmModel) SetImpact(seq uint64, impact deleteImpact) bool {
	if seq == 0 || seq != m.impactSeq || m.impact != nil {
		return false
	}
	m.impact = &impact
	return true
}

// forgetImpact stops waiting for a count; a late result is then stale.
func (m *DeleteConfirmModel) forgetImpact() {
	m.impactSeq, m.impact, m.counting = 0, nil, ""
}

// nameTyped reports whether Yes works: no name is asked for, or the input
// holds exactly the name.
func (m *DeleteConfirmModel) nameTyped() bool {
	return m.typed == "" || m.input.Value() == m.typed
}

// answerDelete is the dialog's answer.
func answerDelete(confirm bool) tea.Cmd {
	return func() tea.Msg { return DeleteConfirmMsg{Confirm: confirm, Close: true} }
}

// Update handles the keys of a red dialog: a plain letter does nothing, so
// a "y" typed for the shell line or a fast double press cannot delete.
// Enter runs the focused button, No when the dialog opens; M-y and M-n
// pick the button whose letter is highlighted.
//
// With a name to type, the input line takes every other key, the focus
// moves between the buttons only, and Yes is disabled (M-y does nothing,
// the focus skips it) until the input holds the name exactly. Once it
// does, the first Enter on No moves the focus to Yes and a second Enter
// deletes, unless the user moved the focus since the name matched: then
// Enter presses the focused button.
func (m *DeleteConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.PasteMsg:
		return m, m.edit(msg)
	case tea.MouseMsg:
		return m, m.mouse(msg)
	}
	return m, nil
}

// key handles a key press; see Update.
func (m *DeleteConfirmModel) key(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+g":
		return answerDelete(false)
	case "enter":
		if m.typed != "" && m.nameTyped() && m.focus == 1 && !m.focusChosen {
			m.focus = 0
			return nil
		}
		return answerDelete(m.focus == 0 && m.nameTyped())
	case "left", "right", "tab", "shift+tab":
		if m.nameTyped() {
			m.focus, m.focusChosen = 1-m.focus, true
		}
		return nil
	}
	if key := msg.Key(); key.Mod == tea.ModAlt {
		switch unicode.ToLower(key.Code) {
		case 'y':
			if !m.nameTyped() {
				return nil
			}
			return answerDelete(true)
		case 'n':
			return answerDelete(false)
		}
	}
	return m.edit(msg)
}

// mouse focuses the button a left click presses and answers with the one
// it is released on. A click on the disabled Yes does nothing.
func (m *DeleteConfirmModel) mouse(msg tea.MouseMsg) tea.Cmd {
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	for idx, r := range m.buttonRect {
		if !r.contains(mouse.X, mouse.Y) || (idx == 0 && !m.nameTyped()) {
			continue
		}
		if _, ok := msg.(tea.MouseClickMsg); ok {
			m.focus, m.focusChosen = idx, true
			return nil
		}
		if _, ok := msg.(tea.MouseReleaseMsg); ok {
			return answerDelete(idx == 0)
		}
	}
	return nil
}

// edit hands msg to the input line when the dialog asks for a name. When
// the input stops matching, the focus leaves the disabled Yes, and the
// next match arms Enter again.
func (m *DeleteConfirmModel) edit(msg tea.Msg) tea.Cmd {
	if m.typed == "" {
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if !m.nameTyped() {
		m.focus, m.focusChosen = 1, false
	}
	return cmd
}

func (m *DeleteConfirmModel) View() tea.View {
	innerWidth := max(30, m.width-4)
	skin := dialog.Danger()
	row := skin.Normal.Width(innerWidth)
	// The title and the names wrap rather than cut: the names are what
	// the user checks before confirming.
	lines := []string{row.Bold(true).Align(lipgloss.Center).Render(m.title)}
	if m.context != "" {
		lines = append(lines, row.Align(lipgloss.Center).Render(fmt.Sprintf("Context: %s", m.context)))
	}
	if len(m.names) > 0 {
		lines = append(lines, row.Render(""))
		item := row.PaddingLeft(2)
		for _, name := range m.names {
			lines = append(lines, item.Render(name))
		}
		if m.more > 0 {
			lines = append(lines, item.Render(fmt.Sprintf("and %d more", m.more)))
		}
	}
	var cursor *tea.Cursor
	if m.typed != "" {
		var nameLines []string
		nameLines, cursor = m.nameLines(skin, innerWidth, lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, lines...)))
		lines = append(lines, nameLines...)
	}
	lines = append(lines, row.Render(""))
	top := lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, lines...))
	// No is the default answer, so Enter right after opening keeps the
	// objects, as mc's confirm dialogs do.
	buttons, rects := dialog.RenderButtons(skin, innerWidth, []dialog.Button{
		{Label: "&Yes", Disabled: !m.nameTyped()}, {Label: "&No", Default: true},
	}, m.focus)
	for i := range m.buttonRect {
		m.buttonRect[i] = buttonRect{}
	}
	for i, r := range rects {
		if i < len(m.buttonRect) {
			m.buttonRect[i] = buttonRect{x: r.X, y: top, w: r.W, h: 1}
		}
	}
	lines = append(lines, buttons)
	return viewWithCursor(lipgloss.JoinVertical(lipgloss.Left, lines...), cursor)
}

// nameLines are what a dialog that asks for the name shows under the
// question: what goes with the object (or that kc is counting it), the
// prompt and the input line. The lines start at line y of the dialog;
// the cursor is the input line's.
func (m *DeleteConfirmModel) nameLines(skin dialog.Skin, width, y int) ([]string, *tea.Cursor) {
	row := skin.Normal.Width(width)
	text := row.PaddingLeft(1).PaddingRight(1)
	lines := []string{row.Render("")}
	if m.impact == nil {
		lines = append(lines, text.Render(m.counting))
	} else {
		for _, l := range m.impact.lines {
			lines = append(lines, text.Render(l))
		}
	}
	lines = append(lines, row.Render(""), text.Render(fmt.Sprintf("Type %q to enable Yes:", m.typed)))
	// The input keeps one column free on each side, and one more for the
	// cursor after the text.
	fieldW := max(2, width-2)
	if m.input.Width() != fieldW-1 {
		m.input.SetWidth(fieldW - 1)
	}
	field := skin.Input.Width(fieldW).Render(ansi.Truncate(m.input.View(), fieldW, ""))
	pad := skin.Normal.Render(" ")
	inputY := y + lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, lines...))
	lines = append(lines, pad+field+pad)
	cursor := m.input.Cursor()
	if cursor != nil {
		cursor.Position.X++
		cursor.Position.Y = inputY
	}
	return lines, cursor
}

// Height is the number of lines the dialog shows in a window whose inner
// width is innerW: the title (wrapped), the context, the names of a set
// after a blank line, a blank line and the buttons.
func (m *DeleteConfirmModel) Height(innerW int) int {
	w, h := m.width, m.height
	m.SetDimensions(innerW, h)
	n := lipgloss.Height(m.View().Content)
	m.SetDimensions(w, h)
	return n
}

// FooterHints leaves Enter out: it runs the focused button, which is No
// when the dialog opens (or, once a typed name matches, moves the focus
// to Yes), so "Enter Confirm" would promise a delete.
func (m *DeleteConfirmModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Esc", Label: "Cancel", Enabled: true},
	}
}
