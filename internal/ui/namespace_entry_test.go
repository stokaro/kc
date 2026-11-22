package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/pkg/kubeconfig"
)

func typeText(m *ScopePickerModel, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestScopePickerTypedEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		typed  bool
		input  string
		picked string // "" when Enter picks nothing
	}{
		{name: "typed name not in the list", typed: true, input: "team-a", picked: "team-a"},
		{name: "a name in the list is picked from the list", typed: true, input: "default", picked: "default"},
		{name: "a filter that matches a listed name picks it before the typed text", typed: true, input: "def", picked: "default"},
		{name: "an invalid name is not offered", typed: true, input: "Team_A", picked: ""},
		{name: "without typed entry a filter only narrows", typed: false, input: "team-a", picked: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			picked := ""
			choices := namespaceChoices([]string{"default", "kube-system"}, "default")
			m := NewScopePickerModel(choices, "default", func(c scopeChoice) tea.Cmd { picked = c.Value; return nil }, nil)
			if tc.typed {
				m.AllowTypedEntry()
			}
			m.SetDimensions(40, 10)
			typeText(m, tc.input)
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if picked != tc.picked {
				t.Fatalf("picked %q, want %q", picked, tc.picked)
			}
		})
	}
}

func TestScopePickerTypedEntryIsShown(t *testing.T) {
	t.Parallel()
	m := NewScopePickerModel(namespaceChoices(nil, "team-a"), "team-a", nil, nil)
	m.AllowTypedEntry()
	m.SetDimensions(40, 5)
	typeText(m, "team-b")
	view := ansi.Strip(m.View().Content)
	lines := strings.Split(view, "\n")
	if !strings.Contains(lines[0], "team-b") || !strings.Contains(lines[0], "typed") {
		t.Fatalf("first row = %q, want the typed name", lines[0])
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "Name or filter: team-b") {
		t.Fatalf("footer = %q", last)
	}
	// Backspace back to an empty filter drops the typed row.
	for range "team-b" {
		m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if slices.Contains(m.visible, typedEntry) {
		t.Fatal("typed row stays with an empty filter")
	}
}

func TestRememberNamespace(t *testing.T) {
	t.Parallel()
	var recent []string
	for _, ns := range []string{"a", "b", models.AllNamespaces, "a", "c"} {
		recent = rememberNamespace(recent, ns)
	}
	if want := []string{"c", "a", "b"}; !slices.Equal(recent, want) {
		t.Fatalf("recent = %v, want %v", recent, want)
	}
	for i := range maxRecentNamespaces + 5 {
		recent = rememberNamespace(recent, fmt.Sprintf("ns-%d", i))
	}
	if len(recent) != maxRecentNamespaces || recent[0] != fmt.Sprintf("ns-%d", maxRecentNamespaces+4) {
		t.Fatalf("recent = %v, want the %d newest", recent, maxRecentNamespaces)
	}
}

func TestFallbackNamespaceNames(t *testing.T) {
	t.Parallel()
	got := fallbackNamespaceNames("team-a", [2]string{"team-b", models.AllNamespaces}, []string{"team-c", "team-a"})
	if want := []string{"team-a", "team-b", "team-c"}; !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	if got := fallbackNamespaceNames("", [2]string{}, nil); len(got) != 0 {
		t.Fatalf("names = %v, want none", got)
	}
}

func TestNamespacePickerTakesATypedNameWhenListingIsForbidden(t *testing.T) {
	app := NewApp()
	app.currentCtx = &kubeconfig.Context{Name: "ctx", Namespace: "team-a"}
	app.panelScope = [2]string{"team-a", "team-a"}
	app.rememberPanelNamespace("team-old")

	app.showNamespacePicker(namespacesLoadedMsg{panel: 1, err: errNamespaceListForbidden})
	modal := app.modalManager.modals["namespace_picker"]
	if modal == nil || !modal.visible {
		t.Fatal("the picker did not open")
	}
	picker, ok := modal.content.(*ScopePickerModel)
	if !ok {
		t.Fatalf("modal content is %T", modal.content)
	}
	var labels []string
	for _, c := range picker.choices {
		labels = append(labels, c.Value)
	}
	if want := []string{models.AllNamespaces, "team-a", "team-old"}; !slices.Equal(labels, want) {
		t.Fatalf("choices = %q, want %q", labels, want)
	}
	if !picker.typed {
		t.Fatal("typed entry is off")
	}
	// Selecting the typed name calls the picker's onSelect, which rescopes
	// the panel; that needs a cluster, so check the choice it would pass.
	typeText(picker, "team-b")
	if got := picker.choiceAt(picker.selected); got.Value != "team-b" {
		t.Fatalf("selected %q, want the typed team-b", got.Value)
	}
}
