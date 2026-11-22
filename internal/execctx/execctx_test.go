package execctx_test

import (
	"slices"
	"testing"

	"github.com/stokaro/kc/internal/execctx"
)

func TestKubectlArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ctx  execctx.Context
		want []string
	}{
		{
			name: "context only",
			ctx:  execctx.Context{Kubeconfig: "/k/a.yaml", Context: "b"},
			want: []string{"--kubeconfig", "/k/a.yaml", "--context", "b"},
		},
		{
			name: "namespace",
			ctx:  execctx.Context{Kubeconfig: "/k/a.yaml", Context: "b", Namespace: " team "},
			want: []string{"--kubeconfig", "/k/a.yaml", "--context", "b", "--namespace", "team"},
		},
		{
			name: "all namespaces keeps the context namespace",
			ctx:  execctx.Context{Kubeconfig: "/k/a.yaml", Context: "b", Impersonation: execctx.Impersonation{User: "alice"}},
			want: []string{"--kubeconfig", "/k/a.yaml", "--context", "b", "--as", "alice"},
		},
		{
			name: "full impersonation with repeated groups",
			ctx: execctx.Context{
				Kubeconfig: "/k/a.yaml",
				Context:    "b",
				Namespace:  "team",
				Impersonation: execctx.Impersonation{
					User:   " system:serviceaccount:team:bot ",
					UID:    "1234",
					Groups: []string{"dev", " ", "system:authenticated"},
				},
			},
			want: []string{
				"--kubeconfig", "/k/a.yaml", "--context", "b", "--namespace", "team",
				"--as", "system:serviceaccount:team:bot", "--as-uid", "1234",
				"--as-group", "dev", "--as-group", "system:authenticated",
			},
		},
		{
			name: "groups without user",
			ctx:  execctx.Context{Context: "b", Impersonation: execctx.Impersonation{Groups: []string{"g1", "g2"}}},
			want: []string{"--context", "b", "--as-group", "g1", "--as-group", "g2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.ctx.KubectlArgs(); !slices.Equal(got, tt.want) {
				t.Fatalf("KubectlArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnv(t *testing.T) {
	t.Parallel()
	base := []string{"HOME=/h", "KUBECONFIG=/old", "PATH=/bin", "KUBECONFIG=/older", "KUBECONFIGX=keep"}
	tests := []struct {
		name       string
		kubeconfig string
		want       []string
	}{
		{name: "replace", kubeconfig: "/new", want: []string{"HOME=/h", "PATH=/bin", "KUBECONFIGX=keep", "KUBECONFIG=/new"}},
		{name: "remove", kubeconfig: "", want: []string{"HOME=/h", "PATH=/bin", "KUBECONFIGX=keep"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := execctx.Env(base, tt.kubeconfig); !slices.Equal(got, tt.want) {
				t.Fatalf("Env() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImpersonationIsZero(t *testing.T) {
	t.Parallel()
	if !(execctx.Impersonation{User: " ", Groups: []string{""}}).IsZero() {
		t.Fatal("blank impersonation should be zero")
	}
	if (execctx.Impersonation{Groups: []string{"g"}}).IsZero() {
		t.Fatal("a group is impersonation")
	}
}
