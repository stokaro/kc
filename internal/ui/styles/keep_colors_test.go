package styles

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestKeepPanelColors(t *testing.T) {
	base := panelColors()
	if base == "" || !strings.Contains(base, "44") {
		t.Fatalf("panel colors %q do not set the blue background", base)
	}
	red := lipgloss.NewStyle().Foreground(lipgloss.Red).Render("lim")
	tests := []struct {
		name, in, want string
	}{
		{"plain", "pods 2.4/10.4 Gi", "pods 2.4/10.4 Gi"},
		{"after a segment", "23% " + red + "  pods", "23% " + red + base + "  pods"},
		{"zero reset", "a\x1b[31mb\x1b[0mc", "a\x1b[31mb\x1b[0m" + base + "c"},
		{"other sequences stay", "a\x1b[1mb\x1b[22mc", "a\x1b[1mb\x1b[22mc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeepPanelColors(tt.in); got != tt.want {
				t.Errorf("KeepPanelColors(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestKeepPanelColorsInsidePanel(t *testing.T) {
	red := lipgloss.NewStyle().Foreground(lipgloss.Red).Render("lim")
	out := lipgloss.NewStyle().Background(current.Background).Width(20).Render(KeepPanelColors("x " + red + " after"))
	before, _, _ := strings.Cut(out, "after")
	reset := strings.LastIndex(before, "\x1b[m")
	if reset < 0 || !strings.Contains(before[reset:], "44") {
		t.Fatalf("text after the red segment has no panel background: %q", out)
	}
}
