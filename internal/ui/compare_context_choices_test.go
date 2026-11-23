package ui

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stokaro/kc/pkg/kubeconfig"
)

// The compare with a context (C-x c) offers the other contexts only: the
// current one compared with itself is always equal, and it was the first
// row, so Enter alone compared a list with itself. A context of the same
// name from another kubeconfig file is a different context and stays.
func TestOtherContextChoices(t *testing.T) {
	t.Parallel()
	a := &kubeconfig.Kubeconfig{Path: "/k/a.yaml"}
	b := &kubeconfig.Kubeconfig{Path: "/k/b.yaml"}
	contexts := []*kubeconfig.Context{
		{Name: "kind-kc", Kubeconfig: a},
		{Name: "mirror", Kubeconfig: b},
		{Name: "prod", Kubeconfig: a},
		{Name: "kind-kc", Kubeconfig: b},
	}
	tests := []struct {
		name    string
		current *kubeconfig.Context
		want    []string // label, note and the context the value points at
	}{
		{name: "first is current", current: contexts[0], want: []string{"mirror b.yaml -> 1", "prod a.yaml -> 2", "kind-kc b.yaml -> 3"}},
		{name: "middle is current", current: contexts[2], want: []string{"kind-kc a.yaml -> 0", "mirror b.yaml -> 1", "kind-kc b.yaml -> 3"}},
		{name: "no current", current: nil, want: []string{"kind-kc a.yaml -> 0", "mirror b.yaml -> 1", "prod a.yaml -> 2", "kind-kc b.yaml -> 3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, c := range otherContextChoices(contexts, tt.current) {
				var i int
				if _, err := fmt.Sscan(c.Value, &i); err != nil || i < 0 || i >= len(contexts) {
					t.Fatalf("choice %+v has no context index", c)
				}
				if contexts[i].Name != c.Label {
					t.Errorf("choice %q points at context %q", c.Label, contexts[i].Name)
				}
				got = append(got, fmt.Sprintf("%s %s -> %d", c.Label, c.Note, i))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("choices = %q, want %q", got, tt.want)
			}
		})
	}
	if got := otherContextChoices(contexts[:1], contexts[0]); len(got) != 0 {
		t.Errorf("a kubeconfig with the current context only offers %v", got)
	}
}
