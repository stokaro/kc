package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/ui/skin"
	uistyles "github.com/sttts/kc/internal/ui/styles"
	"github.com/sttts/kc/pkg/appconfig"
)

func contextColors(t *testing.T, rules ...appconfig.ContextColorConfig) skin.ContextColors {
	t.Helper()
	cc, err := skin.CompileContextColors(rules)
	if err != nil {
		t.Fatal(err)
	}
	return cc
}

func renderPanelFor(t *testing.T, context string, cc skin.ContextColors) []string {
	t.Helper()
	p := NewPanel("test")
	p.SetContextColors(cc)
	p.SetScope(panelScopeLabel{Context: context, Namespace: "default"})
	return strings.Split(p.Render(t.Context(), 60, 8, true), "\n")
}

func TestPanelFrameUsesContextColor(t *testing.T) {
	cc := contextColors(t, appconfig.ContextColorConfig{Match: "*prod*", Color: "red"})

	// The lines as the frame drew them before context colors existed:
	// white lines on blue.
	plainCorner := lipgloss.NewStyle().Foreground(lipgloss.White).Background(lipgloss.Blue).Render("┌")
	redCorner := lipgloss.NewStyle().Foreground(lipgloss.Red).Background(lipgloss.Blue).Render("┌")
	redSide := lipgloss.NewStyle().Foreground(lipgloss.Red).Background(lipgloss.Blue).Render("│")
	redLabel := lipgloss.NewStyle().Background(lipgloss.Red).Foreground(lipgloss.BrightWhite).Bold(true).Render(" eu-prod ")

	t.Run("matching context", func(t *testing.T) {
		lines := renderPanelFor(t, "eu-prod", cc)
		if !strings.HasPrefix(lines[0], redCorner) {
			t.Fatalf("top line %q does not start with a red corner %q", lines[0], redCorner)
		}
		if !strings.HasPrefix(lines[1], redSide) {
			t.Fatalf("side %q is not red", lines[1])
		}
		bottom := lines[len(lines)-1]
		if !strings.HasPrefix(ansi.Strip(bottom), "└─ eu-prod : default ") {
			t.Fatalf("bottom border = %q", ansi.Strip(bottom))
		}
		if !strings.Contains(bottom, redLabel) {
			t.Fatalf("context label is not drawn on red: %q", bottom)
		}
	})

	t.Run("other context keeps the mc look", func(t *testing.T) {
		got := renderPanelFor(t, "kind-dev", cc)
		want := renderPanelFor(t, "kind-dev", skin.ContextColors{})
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("a context without a rule must render as with no rules\n got: %q\nwant: %q", got, want)
		}
		if !strings.HasPrefix(got[0], plainCorner) {
			t.Fatalf("top line %q does not start with the white-on-blue corner %q", got[0], plainCorner)
		}
		if strings.Contains(strings.Join(got, "\n"), "\x1b[31") || strings.Contains(strings.Join(got, "\n"), ";31m") {
			t.Fatalf("unmatched context drew red: %q", got)
		}
	})
}

// TestApplyLookReportsProblems is not parallel: applyLook replaces the
// global panel palette.
func TestApplyLookReportsProblems(t *testing.T) {
	t.Cleanup(func() { uistyles.Apply(uistyles.DefaultPalette()) })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("panel:\n  frame: purple\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := NewApp()
	cfg := appconfig.Default()
	cfg.Panel.Skin = "bad"
	cfg.Contexts = []appconfig.ContextColorConfig{
		{Match: "*prod*", Color: "red"},
		{Match: "*dev*", Color: "nope"},
	}
	err := a.applyLook(cfg, dir)
	if err == nil {
		t.Fatal("expected errors for the bad skin and the bad context color")
	}
	for _, want := range []string{"bad.yaml", "panel.frame", `contexts[1]: color "nope"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if uistyles.Current() != uistyles.DefaultPalette() {
		t.Errorf("a bad skin must fall back to the default palette")
	}
	// The good context rule still applies to both panels.
	for _, p := range []*Panel{a.leftPanel, a.rightPanel} {
		p.SetScope(panelScopeLabel{Context: "prod", Namespace: "default"})
		if c, ok := p.contextTint(); !ok || c != lipgloss.Red {
			t.Errorf("panel tint = %v, %v; want red", c, ok)
		}
	}

	if err := a.applyLook(appconfig.Default(), dir); err != nil {
		t.Fatalf("default config: %v", err)
	}
	if uistyles.Current() != uistyles.DefaultPalette() {
		t.Fatalf("default config must keep the mc palette")
	}
}
