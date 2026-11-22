package appconfig_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	yaml "sigs.k8s.io/yaml"

	"github.com/stokaro/kc/pkg/appconfig"
)

func TestParseColumnsKey(t *testing.T) {
	tests := []struct {
		key                      string
		group, version, resource string
		wantErr                  bool
	}{
		{key: "apps/v1/deployments", group: "apps", version: "v1", resource: "deployments"},
		{key: "v1/pods", version: "v1", resource: "pods"},
		{key: "cert-manager.io/v1/certificates", group: "cert-manager.io", version: "v1", resource: "certificates"},
		{key: "pods", wantErr: true},
		{key: "deployments.apps", wantErr: true},
		{key: "apps/v1/deployments/extra", wantErr: true},
		{key: "apps//deployments", wantErr: true},
		{key: "/v1/pods", wantErr: true},
		{key: "v1/ pods", wantErr: true},
		{key: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			g, v, r, err := appconfig.ParseColumnsKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if g != tt.group || v != tt.version || r != tt.resource {
				t.Fatalf("got %q %q %q, want %q %q %q", g, v, r, tt.group, tt.version, tt.resource)
			}
			if got := appconfig.ColumnsKey(strings.TrimPrefix(g+"/"+v, "/"), r); got != tt.key {
				t.Fatalf("ColumnsKey round trip = %q, want %q", got, tt.key)
			}
		})
	}
}

func TestCustomColumnTemplate(t *testing.T) {
	tests := []struct {
		expr    string
		want    string
		wantErr bool
	}{
		{expr: ".spec.replicas", want: "{.spec.replicas}"},
		{expr: "spec.replicas", want: "{.spec.replicas}"},
		{expr: "{.spec.replicas}", want: "{.spec.replicas}"},
		{expr: "{spec.replicas}", want: "{.spec.replicas}"},
		{expr: ".spec.containers[*].image", want: "{.spec.containers[*].image}"},
		{expr: "{.a}{.b}", wantErr: true},
		{expr: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := appconfig.CustomColumnTemplate(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCustomColumnValidate(t *testing.T) {
	tests := []struct {
		name    string
		col     appconfig.CustomColumn
		wantErr string
	}{
		{name: "added column", col: appconfig.CustomColumn{Name: "IMAGE", JSONPath: ".spec.containers[*].image"}},
		{name: "wide added column", col: appconfig.CustomColumn{Name: "UID", JSONPath: "{.metadata.uid}", Wide: true}},
		{name: "hide one", col: appconfig.CustomColumn{Name: "Up-to-date", Hide: true}},
		{name: "hide all", col: appconfig.CustomColumn{Name: "*", Hide: true}},
		{name: "empty name", col: appconfig.CustomColumn{JSONPath: ".a"}, wantErr: "name is empty"},
		{name: "no path no hide", col: appconfig.CustomColumn{Name: "X"}, wantErr: "jsonPath is empty"},
		{name: "path and hide", col: appconfig.CustomColumn{Name: "X", JSONPath: ".a", Hide: true}, wantErr: "not both"},
		{name: "hide wide", col: appconfig.CustomColumn{Name: "X", Hide: true, Wide: true}, wantErr: "wide applies"},
		{name: "hide name", col: appconfig.CustomColumn{Name: "name", Hide: true}, wantErr: "cannot be hidden"},
		{name: "bad relaxed form", col: appconfig.CustomColumn{Name: "X", JSONPath: "{.a}{.b}"}, wantErr: "unexpected path string"},
		{name: "bad jsonpath", col: appconfig.CustomColumn{Name: "X", JSONPath: ".spec.containers[*"}, wantErr: "X:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.col.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateColumns(t *testing.T) {
	cols := map[string][]appconfig.CustomColumn{
		"apps/v1/deployments": {
			{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"},
			{Name: "BAD", JSONPath: ".spec[*"},
		},
		"pods":    {{Name: "NODE", JSONPath: ".spec.nodeName"}},
		"v1/pods": {{Name: "NODE", JSONPath: ".spec.nodeName"}},
	}
	errs := appconfig.ValidateColumns(cols)
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), `columns "apps/v1/deployments" entry 1: BAD`) {
		t.Fatalf("first error = %v", errs[0])
	}
	if !strings.Contains(errs[1].Error(), `columns key "pods"`) {
		t.Fatalf("second error = %v", errs[1])
	}
	if errs := appconfig.ValidateColumns(nil); len(errs) != 0 {
		t.Fatalf("nil config: %v", errs)
	}
}

func TestColumnsConfigParsing(t *testing.T) {
	const doc = `
columns:
  apps/v1/deployments:
    - name: IMAGE
      jsonPath: '.spec.template.spec.containers[*].image'
    - name: Up-to-date
      hide: true
  v1/pods:
    - name: NODE
      jsonPath: '{.spec.nodeName}'
      wide: true
`
	want := map[string][]appconfig.CustomColumn{
		"apps/v1/deployments": {
			{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"},
			{Name: "Up-to-date", Hide: true},
		},
		"v1/pods": {{Name: "NODE", JSONPath: "{.spec.nodeName}", Wide: true}},
	}

	t.Run("unmarshal", func(t *testing.T) {
		cfg := appconfig.Default()
		if err := yaml.Unmarshal([]byte(doc), cfg); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg.Columns, want) {
			t.Fatalf("got %#v, want %#v", cfg.Columns, want)
		}
	})

	t.Run("load", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.MkdirAll(filepath.Join(home, ".kc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".kc", "config.yaml"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := appconfig.Load()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg.Columns, want) {
			t.Fatalf("got %#v, want %#v", cfg.Columns, want)
		}
	})

	t.Run("default has none and saves none", func(t *testing.T) {
		cfg := appconfig.Default()
		if cfg.Columns != nil {
			t.Fatalf("default columns = %v, want none", cfg.Columns)
		}
		out, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "\ncolumns:") {
			t.Fatalf("default config writes a columns key:\n%s", out)
		}
	})
}
