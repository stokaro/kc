package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestTextInputModalRendersMcLayout(t *testing.T) {
	t.Parallel()
	m := NewTextInputModal(TextInputModalConfig{
		Title:       "Test",
		Label:       "Label",
		Description: "desc",
		HideTitle:   true,
	})
	m.SetDimensions(40, 10)
	m.FocusInput()
	m.SetValue("example")
	got := strings.Split(ansi.Strip(viewString(m.View())), "\n")
	want := []string{
		" Label                                  ",
		" example                                ",
		" desc                                   ",
		"────────────────────────────────────────",
		"          [< OK >] [ Cancel ]           ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("view:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rows := m.RuleRows(); len(rows) != 1 || rows[0] != 3 {
		t.Fatalf("rule rows = %v, want [3]", rows)
	}
}

func TestTextInputModalKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		keys    []tea.KeyPressMsg
		confirm bool
	}{
		{name: "enter in the field confirms", keys: []tea.KeyPressMsg{{Code: tea.KeyEnter}}, confirm: true},
		{name: "tab tab enter presses cancel", keys: []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: tea.KeyTab}, {Code: tea.KeyEnter}}},
		{name: "M-c cancels", keys: []tea.KeyPressMsg{{Code: 'c', Mod: tea.ModAlt}}},
		{name: "M-o confirms", keys: []tea.KeyPressMsg{{Code: 'o', Mod: tea.ModAlt}}, confirm: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewTextInputModal(TextInputModalConfig{InitialValue: "x"})
			var cmd tea.Cmd
			for _, k := range tc.keys {
				_, cmd = m.Update(k)
			}
			if cmd == nil {
				t.Fatal("no result")
			}
			res, ok := cmd().(TextInputModalResultMsg)
			if !ok || res.Confirm != tc.confirm || !res.Close {
				t.Fatalf("result = %+v, want confirm=%v", res, tc.confirm)
			}
		})
	}
}
