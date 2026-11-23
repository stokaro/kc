package appconfig_test

import (
	"strings"
	"testing"

	yaml "sigs.k8s.io/yaml"

	"github.com/stokaro/kc/pkg/appconfig"
)

func TestHealthConfigYAML(t *testing.T) {
	t.Parallel()
	const doc = `
health:
  example.com/v1/widgets:
    - condition: Ready
      status: True
      health: ok
    - condition: Ready
      status: false
      health: notReady
    - jsonPath: .status.replicas
      value: 0
      health: notReady
    - jsonPath: .status.ratio
      value: 1.5
      health: ok
`
	var cfg appconfig.Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rules := cfg.Health["example.com/v1/widgets"]
	if len(rules) != 4 {
		t.Fatalf("rules = %d, want 4", len(rules))
	}
	// YAML reads True, false and 0 as a bool and a number; the rules keep
	// them as the text Kubernetes and the JSONPath print.
	got := []string{string(rules[0].Status), string(rules[1].Status), string(rules[2].Value), string(rules[3].Value)}
	want := []string{"True", "False", "0", "1.5"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("scalars = %v, want %v", got, want)
	}
	if errs := appconfig.ValidateHealth(cfg.Health); len(errs) != 0 {
		t.Fatalf("ValidateHealth = %v, want none", errs)
	}
	var bad appconfig.Config
	if err := yaml.Unmarshal([]byte("health:\n  example.com/v1/widgets:\n    - condition: Ready\n      status: [True]\n      health: ok\n"), &bad); err == nil {
		t.Fatalf("a list as status unmarshaled: %+v", bad.Health)
	}
}

func TestValidateHealth(t *testing.T) {
	t.Parallel()
	ok := appconfig.HealthRule{Condition: "Ready", Health: appconfig.HealthOK}
	tests := []struct {
		name  string
		key   string
		rules []appconfig.HealthRule
		want  []string // substrings of the errors, in order; none for valid
	}{
		{name: "valid condition and field rules", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			ok,
			{Condition: "Ready", Status: "false", Health: appconfig.HealthNotReady},
			{Condition: "Ready", Status: "unknown", Health: appconfig.HealthNotReady},
			{JSONPath: "{.status.phase}", Value: "Done", Health: appconfig.HealthSucceeded},
			{JSONPath: ".status.phase", Value: "Error", Health: appconfig.HealthFailed},
		}},
		{name: "core group key", key: "v1/configmaps", rules: []appconfig.HealthRule{ok}},
		{name: "bad key", key: "widgets", rules: []appconfig.HealthRule{ok}, want: []string{`health key "widgets"`}},
		{name: "key with spaces", key: "example.com/v1/ widgets", rules: []appconfig.HealthRule{ok}, want: []string{"health key"}},
		{name: "no rules", key: "example.com/v1/widgets", want: []string{"no rules"}},
		{name: "health missing and unknown", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			{Condition: "Ready"},
			{Condition: "Ready", Status: "False", Health: "degraded"},
			{Condition: "Ready", Status: "Unknown", Health: "OK"},
		}, want: []string{"rule 1: health is empty", `rule 2: health "degraded"`, `rule 3: health "OK"`}},
		{name: "condition and field in one rule", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			{Condition: "Ready", JSONPath: ".status.phase", Health: appconfig.HealthOK},
		}, want: []string{"either condition or jsonPath"}},
		{name: "neither condition nor field", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			{Health: appconfig.HealthOK},
		}, want: []string{"set condition or jsonPath"}},
		{name: "bad condition status", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			{Condition: "Ready", Status: "yes", Health: appconfig.HealthOK},
		}, want: []string{`status "yes"`}},
		{name: "value on a condition rule", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			{Condition: "Ready", Value: "True", Health: appconfig.HealthOK},
		}, want: []string{"value applies to jsonPath rules"}},
		{name: "status on a field rule", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			{JSONPath: ".status.phase", Status: "True", Value: "Done", Health: appconfig.HealthOK},
		}, want: []string{"status applies to condition rules"}},
		{name: "field rule without value", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			{JSONPath: ".status.phase", Health: appconfig.HealthOK},
		}, want: []string{"value is empty"}},
		{name: "bad jsonPath", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			{JSONPath: "{.status.phase", Value: "Done", Health: appconfig.HealthOK},
		}, want: []string{"jsonPath {.status.phase"}},
		{name: "overlapping rules", key: "example.com/v1/widgets", rules: []appconfig.HealthRule{
			ok,
			{Condition: "Ready", Status: "true", Health: appconfig.HealthFailed},
			{JSONPath: ".status.phase", Value: "Done", Health: appconfig.HealthOK},
			{JSONPath: "{.status.phase}", Value: "Done", Health: appconfig.HealthFailed},
			{Condition: "ready", Health: appconfig.HealthFailed},
		}, want: []string{"rule 2: matches the same condition Ready=True as rule 1", "rule 4: matches the same jsonPath"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := appconfig.ValidateHealth(map[string][]appconfig.HealthRule{tt.key: tt.rules})
			if len(errs) != len(tt.want) {
				t.Fatalf("errors = %v, want %d matching %q", errs, len(tt.want), tt.want)
			}
			for i, w := range tt.want {
				if !strings.Contains(errs[i].Error(), w) {
					t.Errorf("error %d = %q, want it to contain %q", i, errs[i], w)
				}
			}
		})
	}
}
