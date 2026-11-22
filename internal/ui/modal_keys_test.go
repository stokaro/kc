package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestFunctionKeyConstant(t *testing.T) {
	tests := []struct {
		n    int
		want rune
		ok   bool
	}{
		{n: 0},
		{n: 1, want: tea.KeyF1, ok: true},
		{n: 10, want: tea.KeyF10, ok: true},
		{n: 24, want: tea.KeyF24, ok: true},
		{n: 25},
	}
	for _, tc := range tests {
		got, ok := functionKeyConstant(tc.n)
		if got != tc.want || ok != tc.ok {
			t.Errorf("functionKeyConstant(%d) = %v, %v; want %v, %v", tc.n, got, ok, tc.want, tc.ok)
		}
	}
}

func TestKeyMsgForLabel(t *testing.T) {
	tests := []struct {
		label string
		want  string
		ok    bool
	}{
		{label: ""},
		{label: "Esc", want: "esc", ok: true},
		{label: "F3", want: "f3", ok: true},
		{label: "ctrl+s", want: "ctrl+s", ok: true},
		{label: "Ctrl+Alt+x", want: "ctrl+alt+x", ok: true},
		{label: "shift+enter", want: "shift+enter", ok: true},
		{label: "ctrl+xy"},
		{label: "word"},
	}
	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			msg, ok := keyMsgForLabel(tc.label)
			if ok != tc.ok {
				t.Fatalf("keyMsgForLabel(%q) ok = %v, want %v", tc.label, ok, tc.ok)
			}
			if !ok {
				return
			}
			if got := msg.Key().Keystroke(); got != tc.want {
				t.Fatalf("keyMsgForLabel(%q) = %q, want %q", tc.label, got, tc.want)
			}
		})
	}
}
