package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestUpdatedModel(t *testing.T) {
	current := NewPanel("current")
	next := NewPanel("next")
	tests := []struct {
		name string
		next tea.Model
		want *Panel
	}{
		{name: "same type", next: next, want: next},
		{name: "other type", next: NewTerminal(), want: current},
		{name: "nil", next: nil, want: current},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := updatedModel(current, tc.next); got != tc.want {
				t.Fatalf("updatedModel = %p, want %p", got, tc.want)
			}
		})
	}
}
