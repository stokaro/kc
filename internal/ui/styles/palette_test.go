package styles_test

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	uistyles "github.com/sttts/kc/internal/ui/styles"
)

// TestApplyChangesPanelStyles is not parallel: Apply replaces package state.
func TestApplyChangesPanelStyles(t *testing.T) {
	before := map[string]string{
		"header":   uistyles.PanelHeaderStyle.Render("x"),
		"content":  uistyles.PanelContentStyle.Render("x"),
		"footer":   uistyles.PanelFooterStyle.Render("x"),
		"item":     uistyles.PanelItemStyle.Render("x"),
		"selected": uistyles.PanelItemSelectedStyle.Render("x"),
		"table":    uistyles.PanelTableHeaderStyle.Render("x"),
	}
	t.Cleanup(func() { uistyles.Apply(uistyles.DefaultPalette()) })

	// The default palette reproduces the styles kc starts with.
	uistyles.Apply(uistyles.DefaultPalette())
	after := map[string]string{
		"header":   uistyles.PanelHeaderStyle.Render("x"),
		"content":  uistyles.PanelContentStyle.Render("x"),
		"footer":   uistyles.PanelFooterStyle.Render("x"),
		"item":     uistyles.PanelItemStyle.Render("x"),
		"selected": uistyles.PanelItemSelectedStyle.Render("x"),
		"table":    uistyles.PanelTableHeaderStyle.Render("x"),
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s style changed by Apply(DefaultPalette()): %q -> %q", k, v, after[k])
		}
	}

	p := uistyles.DefaultPalette()
	p.Background = lipgloss.Black
	uistyles.Apply(p)
	if uistyles.Current().Background != lipgloss.Black {
		t.Fatal("Current() does not return the applied palette")
	}
	want := lipgloss.NewStyle().Background(lipgloss.Black).Foreground(lipgloss.White).Render("x")
	if got := uistyles.PanelItemStyle.Render("x"); got != want {
		t.Fatalf("item style after Apply = %q, want %q", got, want)
	}
}

func TestContrastText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		bg   color.Color
		want color.Color
	}{
		{name: "red", bg: lipgloss.Red, want: lipgloss.BrightWhite},
		{name: "blue", bg: lipgloss.Blue, want: lipgloss.BrightWhite},
		{name: "yellow", bg: lipgloss.Yellow, want: lipgloss.Black},
		{name: "bright-cyan", bg: lipgloss.BrightCyan, want: lipgloss.Black},
		{name: "white hex", bg: lipgloss.Color("#ffffff"), want: lipgloss.Black},
		{name: "navy hex", bg: lipgloss.Color("#000080"), want: lipgloss.BrightWhite},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := uistyles.ContrastText(tc.bg); got != tc.want {
				t.Fatalf("ContrastText(%v) = %v, want %v", tc.bg, got, tc.want)
			}
		})
	}
}
