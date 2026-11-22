package cmdtemplate_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"github.com/stokaro/kc/internal/cmdtemplate"
)

func TestInputRefs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		tmpl    string
		want    []string
		wantErr string
	}{
		{name: "none", tmpl: "kubectl top pods -n {{.Namespace}}"},
		{name: "plain text", tmpl: "$SHELL"},
		{name: "one", tmpl: "kubectl scale --replicas={{.Input.replicas}} deploy/{{.Name}}", want: []string{"replicas"}},
		{name: "order and dedup", tmpl: "{{.Input.b}} {{.Input.a}} {{.Input.b}}", want: []string{"b", "a"}},
		{name: "pipeline", tmpl: `{{.Input.image | printf "%q"}}`, want: []string{"image"}},
		{name: "inside if and range", tmpl: `{{if .Input.force}}--force{{end}}{{range .Items}}{{$.Input.sel}}{{end}}`, want: []string{"force", "sel"}},
		{name: "parenthesized", tmpl: `{{printf "%s-%s" (.Input.a) .Name}}`, want: []string{"a"}},
		{name: "whole map", tmpl: "{{.Input}}", wantErr: "use .Input.<name>"},
		{name: "too deep", tmpl: "{{.Input.a.b}}", wantErr: "use .Input.<name>"},
		{name: "parse error", tmpl: "{{.Input.a", wantErr: "parsing command template"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := cmdtemplate.InputRefs(tc.tmpl)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("refs = %q, want %q", got, tc.want)
			}
		})
	}
}

// renderLikeRenderCommand executes the bound template with the data the
// command renderer provides, so the tests check what finally runs.
func renderLikeRenderCommand(t *testing.T, tmpl string) string {
	t.Helper()
	data := struct {
		Name, Namespace string
		Items           []struct{ Name string }
	}{Name: "web", Namespace: "prod", Items: []struct{ Name string }{{"a"}, {"b"}}}
	tt, err := template.New("command").Parse(tmpl)
	if err != nil {
		t.Fatalf("bound template %q does not parse: %v", tmpl, err)
	}
	var buf bytes.Buffer
	if err := tt.Execute(&buf, data); err != nil {
		t.Fatalf("bound template %q does not render: %v", tmpl, err)
	}
	return buf.String()
}

func TestBind(t *testing.T) {
	t.Parallel()
	values := map[string]string{"replicas": "3", "image": "nginx:1.27", "force": "yes", "empty": "", "braces": "{{.Name}}"}
	tests := []struct {
		name string
		tmpl string
		// bound is the template text after Bind, "" to skip the check.
		bound string
		// rendered is the final command after the usual rendering.
		rendered string
	}{
		{
			name:     "no input is unchanged",
			tmpl:     "kubectl get pod {{.Name}} -n {{ .Namespace }}",
			bound:    "kubectl get pod {{.Name}} -n {{ .Namespace }}",
			rendered: "kubectl get pod web -n prod",
		},
		{
			name:     "plain answer becomes text",
			tmpl:     "kubectl scale deploy/{{.Name}} --replicas={{.Input.replicas}}",
			bound:    "kubectl scale deploy/{{.Name}} --replicas=3",
			rendered: "kubectl scale deploy/web --replicas=3",
		},
		{
			name:     "answer-only pipeline is evaluated",
			tmpl:     `kubectl run x --image={{.Input.image | printf "%q"}}`,
			bound:    `kubectl run x --image="nginx:1.27"`,
			rendered: `kubectl run x --image="nginx:1.27"`,
		},
		{
			name:     "mixed action keeps the other fields",
			tmpl:     `{{printf "%s:%s" .Name .Input.image}}`,
			bound:    `{{printf "%s:%s" .Name "nginx:1.27"}}`,
			rendered: "web:nginx:1.27",
		},
		{
			name:     "if on an answer",
			tmpl:     `delete {{.Name}}{{if .Input.empty}} --force{{end}}`,
			rendered: "delete web",
		},
		{
			name:     "dollar inside range",
			tmpl:     `{{range .Items}}{{.Name}}={{$.Input.replicas}} {{end}}`,
			rendered: "a=3 b=3 ",
		},
		{
			name:     "braces in an answer stay literal",
			tmpl:     "echo {{.Input.braces}} {{.Name}}",
			rendered: "echo {{.Name}} web",
		},
		{
			name:     "trim markers keep their effect",
			tmpl:     "a {{- .Input.replicas -}} b",
			bound:    "a3b",
			rendered: "a3b",
		},
		{
			name:     "define blocks survive",
			tmpl:     `{{define "ns"}}-n {{.Namespace}}{{end}}get {{.Input.replicas}} {{template "ns" .}}`,
			rendered: "get 3 -n prod",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bound, err := cmdtemplate.Bind(tc.tmpl, values)
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}
			if tc.bound != "" && bound != tc.bound {
				t.Fatalf("bound = %q, want %q", bound, tc.bound)
			}
			if got := renderLikeRenderCommand(t, bound); got != tc.rendered {
				t.Fatalf("rendered = %q, want %q", got, tc.rendered)
			}
		})
	}
}

func TestBindErrors(t *testing.T) {
	t.Parallel()
	if _, err := cmdtemplate.Bind("{{.Input.missing}}", map[string]string{}); err == nil || !strings.Contains(err.Error(), "no answer for .Input.missing") {
		t.Fatalf("err = %v, want a missing answer error", err)
	}
	if _, err := cmdtemplate.Bind("{{.Input}}", nil); err == nil {
		t.Fatalf("Bind must reject .Input alone")
	}
}
