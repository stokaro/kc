package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type hintedModel struct {
	mockModel
	hints  []FooterHint
	status string
}

func (h *hintedModel) FooterHints() []FooterHint { return h.hints }

func (h *hintedModel) FooterStatus(width int) string {
	if h.status == "" {
		return ""
	}
	return lipgloss.NewStyle().Width(width).Render(h.status)
}

func viewerHints() []FooterHint {
	return []FooterHint{
		{Key: "F2", Label: "Options", Enabled: true},
		{Key: "F3", Label: "Next", Enabled: false},
		{Key: "F7", Label: "Search", Enabled: true},
		{Key: "F10", Label: "Close", Enabled: true},
	}
}

func TestModalFunctionKeyFooterUsesSlots(t *testing.T) {
	m := NewModal("viewer", &hintedModel{hints: viewerHints()})
	m.SetDimensions(100, 20)

	footer := m.buildFooter(true)
	if got := lipgloss.Width(footer); got != 100 {
		t.Fatalf("footer is %d columns wide, want 100", got)
	}
	plain := ansi.Strip(footer)
	for _, want := range []string{" 2Options", " 3Next", " 7Search", "10Close"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q in footer %q", want, plain)
		}
	}
	if strings.Contains(plain, "Esc") {
		t.Fatalf("function key footer should not show Esc, got %q", plain)
	}

	starts := functionKeySlotStarts(10, 100)
	if key, enabled := m.keyAtFooterColumn(starts[6]); key != "F7" || !enabled {
		t.Fatalf("slot 7 maps to %q (enabled=%t), want enabled F7", key, enabled)
	}
	if key, enabled := m.keyAtFooterColumn(starts[2] + 3); key != "F3" || enabled {
		t.Fatalf("slot 3 maps to %q (enabled=%t), want disabled F3", key, enabled)
	}
	if _, enabled := m.keyAtFooterColumn(starts[0]); enabled {
		t.Fatalf("slot 1 has no hint and must not be clickable")
	}
}

func TestModalFunctionKeyFooterKeepsStatusAndExtraKeys(t *testing.T) {
	hints := append(viewerHints(), FooterHint{Key: "End", Label: "Follow", Enabled: true})
	m := NewModal("logs", &hintedModel{hints: hints, status: "3 matches"})
	m.SetDimensions(120, 20)

	plain := ansi.Strip(m.buildFooter(true))
	if !strings.Contains(plain, "EndFollow") {
		t.Fatalf("expected extra End slot, got %q", plain)
	}
	idx := strings.Index(plain, "3 matches")
	if idx < 0 || idx+len("3 matches") < 120-2 {
		t.Fatalf("expected status at the right edge, sized to its text, got index %d in %q", idx, plain)
	}
	if !strings.Contains(plain, "10Close") {
		t.Fatalf("labels should keep their room next to a short status, got %q", plain)
	}
}

func TestModalDialogFooterUsesSlots(t *testing.T) {
	m := NewModal("options", &hintedModel{hints: []FooterHint{
		{Key: "Enter", Label: "Apply", Enabled: true},
		{Key: "Esc", Label: "Cancel", Enabled: true},
	}})
	m.SetDimensions(100, 20)
	plain := ansi.Strip(m.buildFooter(false))
	if got := lipgloss.Width(m.buildFooter(false)); got != 100 {
		t.Fatalf("dialog footer is %d columns wide, want 100", got)
	}
	starts := functionKeySlotStarts(2, 100)
	if !strings.HasPrefix(plain, " EnterApply") || !strings.HasPrefix(plain[starts[1]:], " EscCancel") {
		t.Fatalf("dialog footer should use two equal slots, got %q", plain)
	}
}

var _ tea.Model = (*hintedModel)(nil)

func TestModalCloseOnEscTimeout(t *testing.T) {
	newModal := func() (*Modal, *bool) {
		closed := false
		m := NewModal("viewer", &mockModel{})
		m.SetCloseOnSingleEsc(false)
		m.SetCloseOnEscTimeout(true)
		m.SetOnClose(func() tea.Cmd { closed = true; return nil })
		m.Show()
		return m, &closed
	}

	m, closed := newModal()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.IsVisible() {
		t.Fatalf("a lone Esc must wait for the timeout before closing")
	}
	m.Update(EscTimeoutMsg{})
	if m.IsVisible() || !*closed {
		t.Fatalf("expected modal to close after the Esc timeout (visible=%t closed=%t)", m.IsVisible(), *closed)
	}

	m, closed = newModal()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	m.Update(EscTimeoutMsg{})
	if !m.IsVisible() || *closed {
		t.Fatalf("Esc+digit is F-key emulation and must not close the modal")
	}
}
