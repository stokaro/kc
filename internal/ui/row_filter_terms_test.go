package ui

// White-box like the rest of package ui: the dialog constructor that takes
// the list's check is unexported.

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
)

func TestRowFilterDialogChecksTheList(t *testing.T) {
	t.Parallel()
	check := func(f models.RowFilter) error {
		if strings.Contains(f.Text(), "gpu") {
			return errors.New(`gpu>1: this list has no "gpu" column`)
		}
		return nil
	}
	enter := func(text string) (*RowFilterModel, tea.Msg) {
		m := newRowFilterModelFor(0, text, check)
		m.SetDimensions(70, 8)
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			return m, nil
		}
		return m, cmd()
	}

	m, msg := enter("api gpu>1")
	if res, ok := msg.(RowFilterResultMsg); ok && res.Confirm {
		t.Fatalf("a filter the list refuses was accepted: %#v", res)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), `no "gpu" column`) {
		t.Fatalf("dialog does not show the list's error: %q", ansi.Strip(m.View().Content))
	}

	_, msg = enter("api cpu>500m")
	if res, ok := msg.(RowFilterResultMsg); !ok || !res.Confirm || res.Text != "api cpu>500m" {
		t.Fatalf("result = %#v, want the filter confirmed", msg)
	}
}
