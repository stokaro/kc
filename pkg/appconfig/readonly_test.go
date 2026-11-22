package appconfig_test

import (
	"testing"

	yaml "sigs.k8s.io/yaml"

	"github.com/stokaro/kc/pkg/appconfig"
)

func TestMatchContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"kind-dev", "kind-dev", true},
		{"kind-dev", "kind-dev2", false},
		{"kind-dev", "xkind-dev", false},
		{"*prod*", "prod", true},
		{"*prod*", "eu-prod-1", true},
		{"*prod*", "staging", false},
		{"*prod*", "arn:aws:eks:eu-west-1:1234:cluster/prod", true},
		{"prod-*", "prod-eu", true},
		{"prod-*", "prod-", true},
		{"prod-*", "eu-prod-x", false},
		{"stage-?", "stage-1", true},
		{"stage-?", "stage-12", false},
		{"stage-?", "stage-", false},
		{"*", "", true},
		{"*", "anything/at/all", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"a*b", "abab", true},
		// Only * and ? are special; the rest is literal and case-sensitive.
		{"a.b[1]", "a.b[1]", true},
		{"a.b[1]", "aXb1", false},
		{"Prod", "prod", false},
		// Multi-byte runes count as one character for ?.
		{"pr?d", "prÖd", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"~"+tt.name, func(t *testing.T) {
			t.Parallel()
			if got := appconfig.MatchContext(tt.pattern, tt.name); got != tt.want {
				t.Fatalf("MatchContext(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
			}
		})
	}
}

func TestReadOnlyConfigFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     appconfig.ReadOnlyConfig
		context string
		want    bool
	}{
		{name: "zero value allows writes", cfg: appconfig.ReadOnlyConfig{}, context: "prod", want: false},
		{name: "enabled covers every context", cfg: appconfig.ReadOnlyConfig{Enabled: true}, context: "kind-dev", want: true},
		{name: "enabled covers an empty context", cfg: appconfig.ReadOnlyConfig{Enabled: true}, context: "", want: true},
		{name: "glob matches", cfg: appconfig.ReadOnlyConfig{Contexts: []string{"*prod*"}}, context: "eu-prod", want: true},
		{name: "glob refuses", cfg: appconfig.ReadOnlyConfig{Contexts: []string{"*prod*"}}, context: "kind-dev", want: false},
		{name: "exact name among several", cfg: appconfig.ReadOnlyConfig{Contexts: []string{"*prod*", "shared"}}, context: "shared", want: true},
		{name: "blank patterns are ignored", cfg: appconfig.ReadOnlyConfig{Contexts: []string{"", "  "}}, context: "x", want: false},
		{name: "a star does not match an empty context", cfg: appconfig.ReadOnlyConfig{Contexts: []string{"*"}}, context: "", want: false},
		{name: "patterns are trimmed", cfg: appconfig.ReadOnlyConfig{Contexts: []string{" prod "}}, context: "prod", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cfg.For(tt.context); got != tt.want {
				t.Fatalf("For(%q) = %v, want %v", tt.context, got, tt.want)
			}
		})
	}
}

func TestReadOnlyConfigYAML(t *testing.T) {
	t.Parallel()
	data := []byte("readOnly:\n  enabled: false\n  contexts:\n    - \"*prod*\"\n    - kind-shared\ncommands:\n  - name: Restart\n    command: kubectl rollout restart deploy/x\n    dangerous: true\n")
	cfg := appconfig.Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.ReadOnly.For("eu-prod") || !cfg.ReadOnly.For("kind-shared") || cfg.ReadOnly.For("kind-dev") {
		t.Fatalf("readOnly contexts not applied: %+v", cfg.ReadOnly)
	}
	if len(cfg.Commands) != 1 || !cfg.Commands[0].Dangerous {
		t.Fatalf("dangerous flag not decoded: %+v", cfg.Commands)
	}
}
