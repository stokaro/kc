package helm_test

import (
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/helm"
)

func TestSplitManifest(t *testing.T) {
	t.Parallel()
	type obj struct{ apiVersion, kind, name, namespace, source string }
	cases := []struct {
		name     string
		manifest string
		want     []obj
		wantErrs []string
	}{
		{
			name: "helm manifest with source comments",
			manifest: "---\n# Source: web/templates/sa.yaml\napiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: web\n" +
				"---\n# Source: web/templates/deploy.yaml\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: other\n  labels:\n    app: web\n",
			want: []obj{
				{"v1", "ServiceAccount", "web", "", "web/templates/sa.yaml"},
				{"apps/v1", "Deployment", "web", "other", "web/templates/deploy.yaml"},
			},
		},
		{
			name:     "an object without labels is still listed",
			manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n",
			want:     []obj{{"v1", "ConfigMap", "plain", "", ""}},
		},
		{
			name:     "empty and comment-only documents are skipped",
			manifest: "---\n---\n# Source: web/templates/empty.yaml\n# nothing rendered\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n---\n",
			want:     []obj{{"v1", "Secret", "s", "", ""}},
		},
		{
			name:     "v1 List is expanded",
			manifest: "apiVersion: v1\nkind: List\nitems:\n- apiVersion: v1\n  kind: ConfigMap\n  metadata:\n    name: a\n- apiVersion: v1\n  kind: ConfigMap\n  metadata:\n    name: b\n    namespace: x\n",
			want:     []obj{{"v1", "ConfigMap", "a", "", ""}, {"v1", "ConfigMap", "b", "x", ""}},
		},
		{
			name:     "a document without kind is reported and the rest kept",
			manifest: "# Source: web/templates/bad.yaml\napiVersion: v1\nmetadata:\n  name: x\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ok\n",
			want:     []obj{{"v1", "ConfigMap", "ok", "", ""}},
			wantErrs: []string{"web/templates/bad.yaml"},
		},
		{
			name:     "invalid YAML is reported",
			manifest: "apiVersion: v1\nkind: [\n",
			wantErrs: []string{"document 1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			objs, errs := helm.SplitManifest(tc.manifest)
			if len(objs) != len(tc.want) {
				t.Fatalf("got %d objects %+v, want %d", len(objs), objs, len(tc.want))
			}
			for i, w := range tc.want {
				g := objs[i]
				got := obj{g.APIVersion, g.Kind, g.Name, g.Namespace, g.Source}
				if got != w {
					t.Errorf("object %d = %+v, want %+v", i, got, w)
				}
				if !strings.Contains(g.Doc, "kind: "+w.kind) {
					t.Errorf("object %d doc does not hold the object: %q", i, g.Doc)
				}
			}
			if len(errs) != len(tc.wantErrs) {
				t.Fatalf("errors = %v, want %d", errs, len(tc.wantErrs))
			}
			for i, w := range tc.wantErrs {
				if !strings.Contains(errs[i].Error(), w) {
					t.Errorf("error %d = %v, want it to mention %q", i, errs[i], w)
				}
			}
		})
	}
}
