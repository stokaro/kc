package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/ui/dialog"
)

// CopyToLocalResultMsg captures the outcome of the copy dialog.
type CopyToLocalResultMsg struct {
	Path    string
	Confirm bool
	Close   bool
}

const (
	copyOKID     = "ok"
	copyCancelID = "cancel"
)

// CopyToLocalModel is the dialog that writes an object, a log or a viewer
// body to a local file, laid out like mc's Copy dialog: the prompt, the
// target input line, an option and the buttons.
type CopyToLocalModel struct {
	subject       string
	input         textinput.Model
	overwrite     bool
	form          dialog.Form
	errMsg        string
	lastValidPath string
	width, height int
	rules         []int
}

// NewCopyToLocalModel constructs an empty copy dialog model.
func NewCopyToLocalModel() *CopyToLocalModel {
	skin := dialog.Normal()
	m := &CopyToLocalModel{input: dialog.NewInput(skin, ""), form: dialog.Form{Skin: skin}}
	m.syncItems()
	return m
}

func (m *CopyToLocalModel) syncItems() {
	prompt := "Copy to:"
	if m.subject != "" {
		prompt = fmt.Sprintf("Copy %q to:", m.subject)
	}
	m.form.Items = []dialog.Item{
		dialog.Text{Text: prompt},
		dialog.Input{Model: &m.input},
		dialog.Text{Text: m.errMsg, Error: true},
		dialog.Rule{},
		dialog.Check{ID: "overwrite", Label: "Over&write an existing file", On: &m.overwrite},
		dialog.Rule{},
		dialog.Buttons{
			{ID: copyOKID, Label: "&OK", Default: true},
			{ID: copyCancelID, Label: "&Cancel"},
		},
	}
}

func (m *CopyToLocalModel) Init() tea.Cmd          { return nil }
func (m *CopyToLocalModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// PreferredSize implements ModalSizer.
func (m *CopyToLocalModel) PreferredSize(maxW, _ int) (width, height int) {
	m.syncItems()
	width = min(max(m.form.Width(lipgloss.Width(m.input.Value())+4), 50), 80)
	if maxW > 0 {
		width = min(width, maxW)
	}
	return width, m.form.Height()
}

// FooterHints implements ModalFooterHints.
func (m *CopyToLocalModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Enter", Label: "Copy", Enabled: true},
		{Key: "Esc", Label: "Cancel", Enabled: true},
	}
}

// RuleRows implements ModalRules.
func (m *CopyToLocalModel) RuleRows() []int { return m.rules }

// Error is the validation message on display, or "".
func (m *CopyToLocalModel) Error() string { return m.errMsg }

func (m *CopyToLocalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+c", "ctrl+g":
			return m, m.result(false)
		}
	}
	m.syncItems()
	ev, cmd := m.form.Update(msg)
	if ev.Edited || ev.Toggled != "" {
		m.errMsg = ""
	}
	switch ev.Pressed {
	case copyOKID:
		if err := m.validatePath(m.input.Value()); err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		return m, m.result(true)
	case copyCancelID:
		return m, m.result(false)
	}
	return m, cmd
}

func (m *CopyToLocalModel) View() tea.View {
	m.syncItems()
	content, cursor, rules := m.form.View(max(m.width, 20))
	m.rules = rules
	return viewWithCursor(content, cursor)
}

// Configure sets the dialog subject and default path. The overwrite
// option starts off, so Enter alone never replaces an existing file.
func (m *CopyToLocalModel) Configure(subject, path string) {
	m.subject = strings.TrimSpace(subject)
	m.input.SetValue(m.defaultPath(path))
	m.input.CursorEnd()
	m.overwrite = false
	m.errMsg = ""
	m.lastValidPath = ""
	m.syncItems()
}

// FocusPath focuses the file path input.
func (m *CopyToLocalModel) FocusPath() tea.Cmd {
	m.syncItems()
	return m.form.FocusFirst()
}

// BlurInputs blurs the input control.
func (m *CopyToLocalModel) BlurInputs() {
	m.syncItems()
	m.form.FocusButton(copyCancelID)
}

func (m *CopyToLocalModel) defaultPath(path string) string {
	value := expandedPath(strings.TrimSpace(path))
	if value == "" {
		value = "resource.txt"
	}
	if abs, err := filepath.Abs(value); err == nil {
		value = abs
	}
	return value
}

// validatePath checks the target and refuses an existing file unless the
// overwrite option is on.
func (m *CopyToLocalModel) validatePath(value string) error {
	normalized, err := m.normalizePath(value)
	if err != nil {
		m.lastValidPath = ""
		return err
	}
	if _, err := os.Lstat(normalized); err == nil && !m.overwrite {
		m.lastValidPath = ""
		return fmt.Errorf("The file exists; check Overwrite to replace it")
	}
	m.lastValidPath = normalized
	return nil
}

func (m *CopyToLocalModel) normalizePath(value string) (string, error) {
	target := expandedPath(strings.TrimSpace(value))
	if target == "" {
		return "", fmt.Errorf("File path is required")
	}
	absPath, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("File path: %w", err)
	}
	if !filepath.IsAbs(absPath) {
		return "", fmt.Errorf("File path must be absolute")
	}
	dir := filepath.Dir(absPath)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("Directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Directory is not valid")
	}
	if info, err := os.Stat(absPath); err == nil && info.IsDir() {
		return "", fmt.Errorf("File path points to a directory")
	}
	return absPath, nil
}

func (m *CopyToLocalModel) result(confirm bool) tea.Cmd {
	path := m.lastValidPath
	if path == "" {
		if normalized, err := m.normalizePath(m.input.Value()); err == nil {
			path = normalized
		} else {
			path = strings.TrimSpace(m.input.Value())
		}
	}
	msg := CopyToLocalResultMsg{Path: path, Confirm: confirm, Close: true}
	return func() tea.Msg { return msg }
}

func expandedPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if value[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			if value == "~" {
				return home
			}
			if len(value) > 1 && (value[1] == '/' || value[1] == '\\') {
				return filepath.Join(home, value[2:])
			}
		}
	}
	return value
}
