package ui

import (
	"testing"

	"github.com/stokaro/kc/pkg/kubeconfig"
)

func TestContextChoices(t *testing.T) {
	a := &kubeconfig.Kubeconfig{Path: "/home/u/.kube/config"}
	b := &kubeconfig.Kubeconfig{Path: "/home/u/.kube/work.yaml"}
	prod := &kubeconfig.Context{Name: "prod", Kubeconfig: b}
	contexts := []*kubeconfig.Context{
		{Name: "mba", Kubeconfig: a},
		prod,
		{Name: "prod", Kubeconfig: a},
	}
	got := contextChoices(contexts, prod)
	want := []scopeChoice{
		{Label: "mba", Value: "0", Note: "config"},
		{Label: "prod", Value: "1", Note: "current"},
		{Label: "prod", Value: "2", Note: "config"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d choices", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("choice %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// With a single kubeconfig file the file name is noise.
	if c := contextChoices(contexts[:1], nil); c[0].Note != "" {
		t.Errorf("note = %q, want none", c[0].Note)
	}
}

// A panel's picker marks its own context and the other panel's.
func TestPanelContextChoicesMarkTheOtherPanel(t *testing.T) {
	kc := &kubeconfig.Kubeconfig{Path: "/home/u/.kube/config"}
	dev := &kubeconfig.Context{Name: "dev", Kubeconfig: kc}
	prod := &kubeconfig.Context{Name: "prod", Kubeconfig: kc}
	stage := &kubeconfig.Context{Name: "stage", Kubeconfig: kc}
	got := panelContextChoices([]*kubeconfig.Context{dev, prod, stage}, prod, dev, "left")
	want := []string{"left", "current", ""}
	for i, c := range got {
		if c.Note != want[i] {
			t.Errorf("choice %s note = %q, want %q", c.Label, c.Note, want[i])
		}
	}
	// Both panels in one context: it is the current one.
	if got := panelContextChoices([]*kubeconfig.Context{dev}, dev, dev, "left"); got[0].Note != "current" {
		t.Errorf("shared context note = %q, want current", got[0].Note)
	}
}

func TestContextNamespace(t *testing.T) {
	if got := contextNamespace(&kubeconfig.Context{}); got != "default" {
		t.Errorf("got %q", got)
	}
	if got := contextNamespace(&kubeconfig.Context{Namespace: "x"}); got != "x" {
		t.Errorf("got %q", got)
	}
}
