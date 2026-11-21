package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/models"
)

func TestRenderScopeLabel(t *testing.T) {
	out, hits := renderScopeLabel(panelScopeLabel{Context: "mba", Namespace: "kube-system"}, "─", 1, 60)
	if got := ansi.Strip(out); got != "─ mba : kube-system " {
		t.Fatalf("label = %q", got)
	}
	// "└─ mba : kube-system ": the corner is column 0.
	if hits.ctxFrom != 2 || hits.ctxTo != 7 || hits.nsFrom != 8 || hits.nsTo != 21 {
		t.Fatalf("hits = %+v", hits)
	}
	p := &Panel{scopeHits: hits}
	for x, want := range map[int]string{3: "context", 10: "namespace", 1: "", 30: ""} {
		if got := p.scopeHit(x); got != want {
			t.Errorf("scopeHit(%d) = %q, want %q", x, got, want)
		}
	}

	out, _ = renderScopeLabel(panelScopeLabel{Context: "mba", Namespace: models.AllNamespaces}, "─", 1, 60)
	if !strings.Contains(ansi.Strip(out), "all namespaces") {
		t.Fatalf("all namespaces label = %q", ansi.Strip(out))
	}
	if out, _ := renderScopeLabel(panelScopeLabel{}, "─", 1, 60); out != "" {
		t.Fatalf("no context should draw nothing, got %q", out)
	}
}

func TestScopePickerFiltersAndSelects(t *testing.T) {
	var picked string
	choices := namespaceChoices([]string{"default", "kube-public", "kube-system"}, "default")
	if choices[0].Value != models.AllNamespaces || choices[1].Note != "current" {
		t.Fatalf("choices = %+v", choices)
	}
	m := NewScopePickerModel(choices, "default", func(c scopeChoice) tea.Cmd { picked = c.Value; return nil }, nil)
	m.SetDimensions(30, 10)
	for _, r := range "sys" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if picked != "kube-system" {
		t.Fatalf("picked %q, want kube-system", picked)
	}
	// Esc first clears the filter.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.filter != "" || len(m.visible) != len(choices) {
		t.Fatalf("Esc should clear the filter, got %q with %d entries", m.filter, len(m.visible))
	}
}
