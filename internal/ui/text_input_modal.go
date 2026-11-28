package ui

import (
	"strings"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/ui/dialog"
)

// TextInputModalResult describes the outcome of a text input modal interaction.
type TextInputModalResult struct {
	Value   string
	Confirm bool
	Close   bool
}

// TextInputModalResultMsg is emitted when no custom result builder is provided.
type TextInputModalResultMsg TextInputModalResult

// TextInputModalButtonStyles used to color the filled button blocks.
//
// Deprecated: the buttons are drawn in mc's dialog skin; the styles are
// ignored. Drop the field from new code.
type TextInputModalButtonStyles struct {
	Confirm        *lipgloss.Style
	ConfirmFocused *lipgloss.Style
	Cancel         *lipgloss.Style
	CancelFocused  *lipgloss.Style
}

// TextInputModalConfig controls the appearance and behaviour of TextInputModal.
type TextInputModalConfig struct {
	Title        string
	Label        string
	Description  string
	ConfirmLabel string
	CancelLabel  string
	Placeholder  string
	InitialValue string
	MinWidth     int
	MaxWidth     int
	MinHeight    int
	MaxHeight    int
	Validate     func(string) error
	ResultMsg    func(TextInputModalResult) tea.Msg
	HideTitle    bool
	// WrapDescription wraps each description line at the dialog width
	// instead of cutting it; the dialog grows by the lines it needs.
	WrapDescription bool
	// Danger draws the dialog in the red skin of dialogs that change the
	// cluster; a hotkey then needs Meta even on a button.
	Danger bool
	// Deprecated: ignored; see TextInputModalButtonStyles.
	ButtonStyles TextInputModalButtonStyles
}

const (
	textInputConfirmID = "confirm"
	textInputCancelID  = "cancel"
	// textInputMargin is the blank column dialog.Form keeps on each side.
	textInputMargin = 1
)

// TextInputModal is mc's input dialog: a prompt, a cyan input line, a hint
// or error line, a rule and the buttons "[< OK >] [ Cancel ]".
type TextInputModal struct {
	cfg           TextInputModalConfig
	input         textinput.Model
	form          dialog.Form
	errMsg        string
	width, height int
	rules         []int
	// wrapWidth is the content width the description wraps at; zero
	// until the dialog is sized.
	wrapWidth int
	// details are label and value lines above the prompt, such as the
	// context the dialog changes.
	details [][2]string
}

// NewTextInputModal constructs a TextInputModal with the given configuration.
func NewTextInputModal(cfg TextInputModalConfig) *TextInputModal {
	skin := dialog.Normal()
	if cfg.Danger {
		skin = dialog.Danger()
	}
	m := &TextInputModal{
		cfg:   cfg,
		input: dialog.NewInput(skin, cfg.InitialValue),
		form:  dialog.Form{Skin: skin, AltOnly: cfg.Danger},
	}
	m.input.Placeholder = cfg.Placeholder
	m.syncItems()
	m.form.FocusFirst()
	return m
}

// syncItems rebuilds the form rows from the configuration and the error.
func (m *TextInputModal) syncItems() {
	var items []dialog.Item
	if !m.cfg.HideTitle && strings.TrimSpace(m.cfg.Title) != "" {
		items = append(items, dialog.Text{Text: m.cfg.Title, Center: true, Bold: true})
	}
	if len(m.details) > 0 {
		labelW := 0
		for _, d := range m.details {
			labelW = max(labelW, lipgloss.Width(d[0]))
		}
		for _, d := range m.details {
			items = append(items, dialog.Text{Text: d[0] + ":" + strings.Repeat(" ", labelW-lipgloss.Width(d[0])+2) + d[1]})
		}
		items = append(items, dialog.Text{})
	}
	if strings.TrimSpace(m.cfg.Label) != "" {
		items = append(items, dialog.Text{Text: m.cfg.Label})
	}
	items = append(items, dialog.Input{Model: &m.input})
	// The hint keeps its lines; an error takes the first one, so the
	// dialog does not change its height.
	for i, line := range m.descriptionLines() {
		if i == 0 && m.errMsg != "" {
			items = append(items, dialog.Text{Text: m.errMsg, Error: true})
			continue
		}
		items = append(items, dialog.Text{Text: line})
	}
	confirm := hotkeyLabel(m.confirmLabel())
	cancel := hotkeyLabel(m.cancelLabel(), confirm.Hotkey())
	items = append(items, dialog.Rule{}, dialog.Buttons{
		{ID: textInputConfirmID, Label: confirm, Default: true},
		{ID: textInputCancelID, Label: cancel},
	})
	m.form.Items = items
}

// descriptionLines are the description's lines, wrapped at the content
// width when the dialog asks for it and is sized.
func (m *TextInputModal) descriptionLines() []string {
	lines := strings.Split(m.cfg.Description, "\n")
	if !m.cfg.WrapDescription || m.wrapWidth <= 0 {
		return lines
	}
	inner := max(1, m.wrapWidth-2*textInputMargin)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, strings.Split(ansi.Wrap(line, inner, ""), "\n")...)
	}
	return out
}

// hotkeyLabel marks the first letter of label as its hotkey, or the first
// letter that is not taken, unless label already has a marker.
func hotkeyLabel(label string, taken ...rune) dialog.Label {
	if strings.Contains(label, "&") {
		return dialog.Label(label)
	}
	r := []rune(label)
	for i, c := range r {
		lc := []rune(strings.ToLower(string(c)))[0]
		if lc < 'a' || lc > 'z' {
			continue
		}
		free := true
		for _, t := range taken {
			if t == lc {
				free = false
			}
		}
		if free {
			return dialog.Label(string(r[:i]) + "&" + string(r[i:]))
		}
	}
	return dialog.Label(label)
}

// Init implements tea.Model.
func (m *TextInputModal) Init() tea.Cmd { return nil }

// SetDimensions records the available content dimensions.
func (m *TextInputModal) SetDimensions(w, h int) {
	m.width = w
	m.height = h
	if w > 0 {
		m.wrapWidth = m.contentWidth()
	}
}

// PreferredSize implements ModalSizer.
func (m *TextInputModal) PreferredSize(maxContentWidth, maxContentHeight int) (width, height int) {
	m.wrapWidth = 0 // the widest line decides the width
	width = m.clampWidth(m.desiredWidth())
	if maxContentWidth > 0 && width > maxContentWidth {
		width = maxContentWidth
	}
	m.wrapWidth = width
	m.syncItems()
	height = m.clampHeight(m.form.Height())
	if maxContentHeight > 0 && height > maxContentHeight {
		height = maxContentHeight
	}
	return width, height
}

// View renders the modal content and cursor.
func (m *TextInputModal) View() tea.View {
	width := m.contentWidth()
	if width <= 0 {
		width = m.clampWidth(m.desiredWidth())
	}
	content, cursor := m.render(width)
	return viewWithCursor(content, cursor)
}

// RuleRows implements ModalRules.
func (m *TextInputModal) RuleRows() []int { return m.rules }

// Update handles Tea messages.
func (m *TextInputModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c", "ctrl+g":
			return m, m.emitResult(false, true)
		}
	}
	m.syncItems()
	ev, cmd := m.form.Update(msg)
	if ev.Edited && m.errMsg != "" {
		m.errMsg = ""
		m.syncItems()
	}
	switch ev.Pressed {
	case textInputConfirmID:
		return m, m.validateAndSubmit()
	case textInputCancelID:
		return m, m.emitResult(false, true)
	}
	return m, cmd
}

// FooterHints implements ModalFooterHints.
func (m *TextInputModal) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Enter", Label: m.confirmLabel(), Enabled: true},
		{Key: "Esc", Label: m.cancelLabel(), Enabled: true},
	}
}

// FocusInput focuses the text field.
func (m *TextInputModal) FocusInput() tea.Cmd {
	m.syncItems()
	return m.form.FocusFirst()
}

// BlurInput moves the focus from the text field to the confirm button.
func (m *TextInputModal) BlurInput() {
	m.syncItems()
	if m.form.FocusedInput() {
		m.form.FocusButton(textInputConfirmID)
	}
}

// SetTitle updates the modal title.
func (m *TextInputModal) SetTitle(title string) { m.cfg.Title = title }

// SetLabel updates the field label.
func (m *TextInputModal) SetLabel(label string) { m.cfg.Label = label }

// SetDetails shows label and value lines above the prompt, as the red
// confirmation dialogs do: {"Context", "kind-dev"}.
func (m *TextInputModal) SetDetails(details [][2]string) {
	m.details = append([][2]string(nil), details...)
	m.syncItems()
}

// SetDescription updates the supporting description/hint text.
func (m *TextInputModal) SetDescription(desc string) { m.cfg.Description = desc }

// SetConfirmLabel updates the confirm button label.
func (m *TextInputModal) SetConfirmLabel(label string) { m.cfg.ConfirmLabel = label }

// SetCancelLabel updates the cancel button label.
func (m *TextInputModal) SetCancelLabel(label string) { m.cfg.CancelLabel = label }

// SetPlaceholder changes the placeholder text.
func (m *TextInputModal) SetPlaceholder(placeholder string) { m.input.Placeholder = placeholder }

// SetValue replaces the current text input value.
func (m *TextInputModal) SetValue(val string) {
	m.input.SetValue(val)
	m.input.CursorEnd()
}

// Value returns the trimmed text input value.
func (m *TextInputModal) Value() string { return strings.TrimSpace(m.input.Value()) }

// SetValidate overrides the validation function.
func (m *TextInputModal) SetValidate(fn func(string) error) { m.cfg.Validate = fn }

// SetResultBuilder overrides the result message factory.
func (m *TextInputModal) SetResultBuilder(fn func(TextInputModalResult) tea.Msg) {
	m.cfg.ResultMsg = fn
}

// ClearError resets any displayed validation error.
func (m *TextInputModal) ClearError() { m.errMsg = "" }

// Error returns the last validation error, if any.
func (m *TextInputModal) Error() string { return m.errMsg }

func (m *TextInputModal) render(width int) (string, *tea.Cursor) {
	m.wrapWidth = width
	m.syncItems()
	content, cursor, rules := m.form.View(width)
	m.rules = rules
	return lipgloss.NewStyle().Width(width).Background(m.form.Skin.Normal.GetBackground()).Render(content), cursor
}

// contentWidth is the width the content renders at: the configured
// bounds, but never wider than the window gave it, so a line that wraps
// stays inside the frame. Zero before the window is sized.
func (m *TextInputModal) contentWidth() int {
	if m.width <= 0 {
		return 0
	}
	return min(m.clampWidth(m.width), m.width)
}

func (m *TextInputModal) desiredWidth() int {
	m.syncItems()
	return m.clampWidth(m.form.Width(lipgloss.Width(m.input.Value()) + 6))
}

func (m *TextInputModal) clampWidth(w int) int {
	minWidth := m.cfg.MinWidth
	if minWidth <= 0 {
		minWidth = 24
	}
	if w < minWidth {
		w = minWidth
	}
	maxWidth := m.cfg.MaxWidth
	if maxWidth > 0 && w > maxWidth {
		w = maxWidth
	}
	return w
}

func (m *TextInputModal) clampHeight(h int) int {
	minHeight := m.cfg.MinHeight
	if minHeight <= 0 {
		minHeight = 5
	}
	if h < minHeight {
		h = minHeight
	}
	maxHeight := m.cfg.MaxHeight
	if maxHeight > 0 && h > maxHeight {
		h = maxHeight
	}
	return h
}

func (m *TextInputModal) validateAndSubmit() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	if fn := m.cfg.Validate; fn != nil {
		if err := fn(value); err != nil {
			m.errMsg = err.Error()
			return nil
		}
	}
	m.errMsg = ""
	return m.emitResult(true, true)
}

func (m *TextInputModal) emitResult(confirm, closeModal bool) tea.Cmd {
	result := TextInputModalResult{
		Value:   strings.TrimSpace(m.input.Value()),
		Confirm: confirm,
		Close:   closeModal,
	}
	builder := m.cfg.ResultMsg
	if builder == nil {
		builder = func(res TextInputModalResult) tea.Msg { return TextInputModalResultMsg(res) }
	}
	return func() tea.Msg { return builder(result) }
}

func (m *TextInputModal) confirmLabel() string {
	if label := strings.TrimSpace(m.cfg.ConfirmLabel); label != "" {
		return label
	}
	return "OK"
}

func (m *TextInputModal) cancelLabel() string {
	if label := strings.TrimSpace(m.cfg.CancelLabel); label != "" {
		return label
	}
	return "Cancel"
}
