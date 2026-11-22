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

func TestContextNamespace(t *testing.T) {
	if got := contextNamespace(&kubeconfig.Context{}); got != "default" {
		t.Errorf("got %q", got)
	}
	if got := contextNamespace(&kubeconfig.Context{Namespace: "x"}); got != "x" {
		t.Errorf("got %q", got)
	}
}
