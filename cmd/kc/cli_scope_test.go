package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/sttts/kc/internal/ui"
)

// parseRunConfig parses args as main does and returns the UI options.
func parseRunConfig(t *testing.T, args ...string) (ui.RunConfig, error) {
	t.Helper()
	var cli cliFlags
	parser, err := kong.New(&cli, kong.Name("kc"))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	kctx, err := parser.Parse(args)
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	intent, ns, err := deriveStartupIntent(kctx.Command(), &cli)
	if err != nil {
		return ui.RunConfig{}, err
	}
	return newRunConfig(&cli, intent, ns), nil
}

func TestStartupScopeFlags(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name      string
		args      []string
		context   string
		namespace string
		all       bool
		verb      ui.KubectlVerb
		intentNs  string
		wantErr   string
	}{
		{name: "defaults", args: nil},
		{name: "context only", args: []string{"--context", "x"}, context: "x"},
		{name: "context with equals", args: []string{"--context=staging"}, context: "staging"},
		{name: "short all namespaces", args: []string{"-A"}, all: true},
		{name: "long all namespaces", args: []string{"--all-namespaces"}, all: true},
		{name: "namespace", args: []string{"-n", "ns"}, namespace: "ns"},
		{name: "all namespaces wins over namespace", args: []string{"-n", "ns", "-A"}, all: true},
		{
			name: "context and all namespaces before get", args: []string{"--context", "staging", "-A", "get", "pods"},
			context: "staging", all: true, verb: ui.KubectlVerbGet,
		},
		{
			name: "flags after get", args: []string{"get", "pods", "--context", "staging", "-A"},
			context: "staging", all: true, verb: ui.KubectlVerbGet,
		},
		{
			name: "get comma list across namespaces", args: []string{"-A", "get", "pods,svc"},
			all: true, verb: ui.KubectlVerbGet,
		},
		{
			name: "get with namespace", args: []string{"--context", "x", "-n", "ns", "get", "pods"},
			context: "x", namespace: "ns", verb: ui.KubectlVerbGet, intentNs: "ns",
		},
		{
			name: "logs with context", args: []string{"--context", "x", "logs", "web", "-n", "ns"},
			context: "x", namespace: "ns", verb: ui.KubectlVerbLogs, intentNs: "ns",
		},
		{name: "get name across namespaces", args: []string{"-A", "get", "pods", "web"}, wantErr: "across all namespaces"},
		{name: "get type/name across namespaces", args: []string{"get", "pod/web", "-A"}, wantErr: "across all namespaces"},
		{name: "logs across namespaces", args: []string{"-A", "logs", "web"}, wantErr: "kc logs does not accept -A"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseRunConfig(t, tc.args...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Context != tc.context {
				t.Errorf("Context = %q, want %q", cfg.Context, tc.context)
			}
			if cfg.Namespace != tc.namespace {
				t.Errorf("Namespace = %q, want %q", cfg.Namespace, tc.namespace)
			}
			if cfg.AllNamespaces != tc.all {
				t.Errorf("AllNamespaces = %v, want %v", cfg.AllNamespaces, tc.all)
			}
			if cfg.StartupIntent.Verb != tc.verb {
				t.Errorf("Verb = %q, want %q", cfg.StartupIntent.Verb, tc.verb)
			}
			if cfg.StartupIntent.Namespace != tc.intentNs {
				t.Errorf("intent Namespace = %q, want %q", cfg.StartupIntent.Namespace, tc.intentNs)
			}
		})
	}
}

func TestStartupScopeKeepsImpersonation(t *testing.T) {
	t.Parallel()
	cfg, err := parseRunConfig(t, "--context", "x", "-A", "--as", "alice", "--as-group", "g1", "--as-group", "g2", "--as-uid", "42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Context != "x" || !cfg.AllNamespaces {
		t.Fatalf("scope = %q/%v, want x/true", cfg.Context, cfg.AllNamespaces)
	}
	if cfg.ImpersonateUser != "alice" || cfg.ImpersonateUID != "42" || !slices.Equal(cfg.ImpersonateGroups, []string{"g1", "g2"}) {
		t.Fatalf("impersonation = %q %q %v", cfg.ImpersonateUser, cfg.ImpersonateUID, cfg.ImpersonateGroups)
	}
}
