package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/models"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// RowFilterResultMsg is the outcome of the Filter dialog.
type RowFilterResultMsg struct {
	Panel   int
	Text    string
	Confirm bool
	Close   bool
}

// RowFilterModel is the Filter dialog (M-f), like mc's panel filter. An
// empty text clears the filter.
type RowFilterModel struct {
	panel int
	modal *TextInputModal
}

const rowFilterHint = "terms, all must match: regex, !regex, -l app=web, -n a,b, cpu>500m, status=Running; empty clears"

// NewRowFilterModel returns the Filter dialog for a panel, prefilled with
// the current filter text.
func NewRowFilterModel(panel int, text string) *RowFilterModel {
	return newRowFilterModelFor(panel, text, nil)
}

// newRowFilterModelFor is NewRowFilterModel with a check of the parsed
// filter against the list, such as the columns it has.
func newRowFilterModelFor(panel int, text string, check func(models.RowFilter) error) *RowFilterModel {
	m := &RowFilterModel{panel: panel}
	validate := func(text string) error {
		f, err := models.ParseRowFilter(text)
		if err != nil || check == nil {
			return err
		}
		return check(f)
	}
	confirmStyle := lipgloss.NewStyle().
		Padding(0, 3).
		Background(lipgloss.Color(uistyles.ColorModalButtonBg)).
		Foreground(lipgloss.Color(uistyles.ColorWhite))
	confirmFocused := confirmStyle.
		Background(lipgloss.Color(uistyles.ColorModalButtonSelBg)).
		Foreground(lipgloss.Color(uistyles.ColorBlack)).
		Bold(true)
	m.modal = NewTextInputModal(TextInputModalConfig{
		Title:        "Filter",
		Label:        "Filter",
		Description:  rowFilterHint,
		ConfirmLabel: "OK",
		CancelLabel:  "Cancel",
		InitialValue: text,
		MinWidth:     30,
		MaxWidth:     70,
		MinHeight:    5,
		Validate:     validate,
		ResultMsg:    m.resultMsg,
		HideTitle:    true,
		ButtonStyles: TextInputModalButtonStyles{
			Confirm:        &confirmStyle,
			ConfirmFocused: &confirmFocused,
		},
	})
	return m
}

func (m *RowFilterModel) Init() tea.Cmd          { return m.modal.Init() }
func (m *RowFilterModel) SetDimensions(w, h int) { m.modal.SetDimensions(w, h) }
func (m *RowFilterModel) PreferredSize(maxW, maxH int) (width, height int) {
	return m.modal.PreferredSize(maxW, maxH)
}
func (m *RowFilterModel) FooterHints() []FooterHint { return m.modal.FooterHints() }

func (m *RowFilterModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.modal.Update(msg)
	return m, cmd
}

func (m *RowFilterModel) View() tea.View { return m.modal.View() }

// RuleRows implements ModalRules.
func (m *RowFilterModel) RuleRows() []int { return m.modal.RuleRows() }

func (m *RowFilterModel) resultMsg(res TextInputModalResult) tea.Msg {
	return RowFilterResultMsg{
		Panel:   m.panel,
		Text:    strings.TrimSpace(res.Value),
		Confirm: res.Confirm,
		Close:   res.Close,
	}
}
