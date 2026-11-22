package helm_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/helm"
)

// fixture is a release record the way Helm marshals it, including fields
// kc does not decode.
func fixture(name string, version int) map[string]any {
	return map[string]any{
		"name":      name,
		"namespace": "apps",
		"version":   version,
		"info": map[string]any{
			"first_deployed": "2026-01-02T03:04:05.123456789Z",
			"last_deployed":  "2026-02-03T04:05:06Z",
			"deleted":        "",
			"description":    "Upgrade complete",
			"status":         "deployed",
			"notes":          "Visit http://example",
		},
		"chart": map[string]any{
			"metadata":  map[string]any{"name": "web", "version": "1.2.3", "appVersion": "4.5"},
			"templates": []any{map[string]any{"name": "templates/a.yaml", "data": "YQ=="}},
			"values":    map[string]any{"replicas": 1},
		},
		"config":   map[string]any{"replicas": 3, "image": map[string]any{"tag": "v2"}},
		"manifest": "---\n# Source: web/templates/cm.yaml\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web\n",
		"hooks": []any{map[string]any{
			"name": "web-test", "kind": "Pod", "path": "web/templates/test.yaml",
			"manifest": "apiVersion: v1\nkind: Pod\nmetadata:\n  name: web-test\n", "events": []string{"test"},
		}},
	}
}

func gzipBase64(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecode(t *testing.T) {
	t.Parallel()
	good := mustJSON(t, fixture("web", 3))
	cases := []struct {
		name    string
		in      []byte
		wantErr string
	}{
		{name: "gzip", in: gzipBase64(t, good)},
		{name: "plain JSON without gzip", in: []byte(base64.StdEncoding.EncodeToString(good))},
		{name: "trailing newline", in: append(gzipBase64(t, good), '\n')},
		{name: "not base64", in: []byte("%%%"), wantErr: "not base64"},
		{name: "truncated gzip", in: []byte(base64.StdEncoding.EncodeToString(append([]byte{0x1f, 0x8b, 0x08}, 0, 0, 0))), wantErr: "not gzip"},
		{name: "not JSON", in: gzipBase64(t, []byte("{nope")), wantErr: "not JSON"},
		{name: "no name", in: gzipBase64(t, []byte(`{"version":1}`)), wantErr: "no name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rel, err := helm.Decode(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if rel.Name != "web" || rel.Namespace != "apps" || rel.Version != 3 {
				t.Fatalf("identity = %s/%s v%d", rel.Namespace, rel.Name, rel.Version)
			}
			if got := rel.ChartName(); got != "web-1.2.3" {
				t.Errorf("ChartName = %q", got)
			}
			if got := rel.AppVersion(); got != "4.5" {
				t.Errorf("AppVersion = %q", got)
			}
			if rel.Info.Status != "deployed" || rel.Info.Notes != "Visit http://example" {
				t.Errorf("info = %+v", rel.Info)
			}
			want := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
			if !rel.Info.LastDeployed.Equal(want) {
				t.Errorf("LastDeployed = %v, want %v", rel.Info.LastDeployed, want)
			}
			if !rel.Info.Deleted.IsZero() {
				t.Errorf("Deleted = %v, want zero for \"\"", rel.Info.Deleted)
			}
			if len(rel.Hooks) != 1 || rel.Hooks[0].Name != "web-test" {
				t.Errorf("hooks = %+v", rel.Hooks)
			}
		})
	}
}

// Encode is what tests use to build fixtures; it must round-trip.
func TestEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	enc, err := helm.Encode(fixture("web", 7))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := helm.Decode([]byte(enc))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != 7 {
		t.Fatalf("version = %d", rel.Version)
	}
}

func TestValuesYAML(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		config string
		want   string
	}{
		{name: "absent", config: "", want: "{}\n"},
		{name: "null", config: "null", want: "{}\n"},
		{name: "nested, integers stay integers", config: `{"replicas":3,"image":{"tag":"v2"}}`, want: "image:\n  tag: v2\nreplicas: 3\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rel := &helm.Release{Name: "r", Config: json.RawMessage(tc.config)}
			got, err := rel.ValuesYAML()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ValuesYAML = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHooksManifest(t *testing.T) {
	t.Parallel()
	rel := &helm.Release{Hooks: []helm.Hook{
		{Path: "c/templates/a.yaml", Manifest: "kind: Pod\n"},
		{Path: "c/templates/b.yaml", Manifest: "kind: Job"},
	}}
	want := "---\n# Source: c/templates/a.yaml\nkind: Pod\n---\n# Source: c/templates/b.yaml\nkind: Job\n"
	if got := rel.HooksManifest(); got != want {
		t.Fatalf("HooksManifest = %q, want %q", got, want)
	}
	if got := (&helm.Release{}).HooksManifest(); got != "" {
		t.Fatalf("no hooks = %q", got)
	}
}
