package models_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sttts/kc/internal/explain"
	"github.com/sttts/kc/internal/models"
	table "github.com/sttts/kc/internal/table"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const explainDoc = `{"components":{"schemas":{
 "io.k8s.api.apps.v1.Deployment":{"type":"object","description":"Deployment enables declarative updates.",
  "properties":{
   "spec":{"allOf":[{"$ref":"#/components/schemas/io.k8s.api.apps.v1.DeploymentSpec"}],"description":"Spec. More text."},
   "kind":{"type":"string"}},
  "x-kubernetes-group-version-kind":[{"group":"apps","version":"v1","kind":"Deployment"}]},
 "io.k8s.api.apps.v1.DeploymentSpec":{"type":"object","required":["template"],
  "properties":{
   "replicas":{"type":"integer","description":"Number of desired pods.\nDefaults to 1."},
   "template":{"type":"object","properties":{"spec":{"type":"object","properties":{"containers":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}}}}}}}},
 "io.k8s.api.apps.v1.Opaque":{"type":"object","x-kubernetes-preserve-unknown-fields":true,
  "x-kubernetes-group-version-kind":[{"group":"apps","version":"v1","kind":"Opaque"}]}
}}}`

var deploymentSpec = models.ExplainSpec{
	GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
	GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
}

func explainDeps() models.Deps {
	return models.Deps{CtxName: "kind-a", KubectlContext: "kind-a"}
}

func explainRoot(t *testing.T, gvk schema.GroupVersionKind) *explain.Node {
	t.Helper()
	doc, err := explain.Parse([]byte(explainDoc))
	if err != nil {
		t.Fatal(err)
	}
	n, err := doc.Root(gvk)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func rowsOf(t *testing.T, f models.Folder) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, r := range f.Lines(t.Context(), 0, f.Len(t.Context())) {
		id, cells, _, _ := r.Columns()
		out[id] = cells
	}
	if len(out) == 0 {
		t.Fatal("folder has no rows")
	}
	return out
}

func TestExplainFolderBrowse(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := models.NewExplainFolder(explainDeps(), deploymentSpec, explainRoot(t, deploymentSpec.GVK), nil)

	if got := strings.Join(f.Path(), "/"); got != "explain/apps/v1/Deployment" {
		t.Errorf("path = %q", got)
	}
	if got := f.KubectlCommand(); got != "kubectl explain deployments --api-version=apps/v1" {
		t.Errorf("command = %q", got)
	}
	rows := rowsOf(t, f)
	if _, ok := rows["__back__"]; !ok {
		t.Error("no back row; the user cannot return to the resource")
	}
	if got := rows["spec"]; strings.Join(got, "|") != "/spec|DeploymentSpec||Spec." {
		t.Errorf("spec row = %q", got)
	}
	if got := rows["kind"]; strings.Join(got, "|") != "kind|string||-" {
		t.Errorf("kind row = %q", got)
	}

	item, ok := f.ItemByID(ctx, "spec")
	if !ok {
		t.Fatal("spec item missing")
	}
	enter, ok := item.(models.Enterable)
	if !ok {
		t.Fatal("spec is not enterable")
	}
	if leaf, _ := f.ItemByID(ctx, "kind"); leaf != nil {
		if _, ok := leaf.(models.Enterable); ok {
			t.Error("scalar kind is enterable")
		}
	}
	next, err := enter.Enter()
	if err != nil {
		t.Fatal(err)
	}
	spec := next.(*models.ExplainFolder)
	if got := strings.Join(spec.Path(), "/"); got != "explain/apps/v1/Deployment/spec" {
		t.Errorf("child path = %q", got)
	}
	if got := spec.KubectlCommand(); got != "kubectl explain deployments.spec --api-version=apps/v1" {
		t.Errorf("child command = %q", got)
	}
	specRows := rowsOf(t, spec)
	if got := specRows["template"]; got[0] != "/template" || got[2] != "yes" {
		t.Errorf("template row = %q, want a required branch", got)
	}
	if got := specRows["replicas"]; got[2] != "" || got[3] != "Number of desired pods." {
		t.Errorf("replicas row = %q", got)
	}

	// F3 shows the full description and the kubectl command for the field.
	replicas, _ := spec.ItemByID(ctx, "replicas")
	view, ok := replicas.(models.Viewable)
	if !ok {
		t.Fatal("field is not viewable")
	}
	title, body, _, mime, _, err := view.ViewContent()
	if err != nil {
		t.Fatal(err)
	}
	if title != "Deployment.spec.replicas" || mime != "text/plain" {
		t.Errorf("title %q mime %q", title, mime)
	}
	if !strings.Contains(body, "Defaults to 1.") || !strings.Contains(body, "FIELD:      spec.replicas <integer>") {
		t.Errorf("body:\n%s", body)
	}
	vc := replicas.(interface{ ViewCommand() string })
	if got := vc.ViewCommand(); got != "kubectl explain deployments.spec.replicas --api-version=apps/v1" {
		t.Errorf("view command = %q", got)
	}

	// Arrays are crossed: containers lists the item fields.
	tmpl, _ := spec.ItemByID(ctx, "template")
	tf, _ := tmpl.(models.Enterable).Enter()
	podSpec, _ := mustItem(t, tf, "spec").(models.Enterable).Enter()
	containers, _ := mustItem(t, podSpec, "containers").(models.Enterable).Enter()
	if _, ok := rowsOf(t, containers)["name"]; !ok {
		t.Error("containers does not list the item fields")
	}
	if got := containers.(*models.ExplainFolder).KubectlCommand(); got != "kubectl explain deployments.spec.template.spec.containers --api-version=apps/v1" {
		t.Errorf("nested command = %q", got)
	}
}

func mustItem(t *testing.T, f models.Folder, id string) models.Item {
	t.Helper()
	item, ok := f.ItemByID(t.Context(), id)
	if !ok {
		t.Fatalf("item %q missing in %v", id, f.Path())
	}
	return item
}

func TestExplainFolderStates(t *testing.T) {
	t.Parallel()
	forbidden := apierrors.NewForbidden(schema.GroupResource{}, "", errors.New("openapi denied"))
	tests := []struct {
		name string
		node *explain.Node
		err  error
		want string
	}{
		{name: "not published", err: explain.ErrNotPublished, want: "No documentation: the cluster publishes no schema for apps/v1 Deployment"},
		{name: "kind not documented", err: explain.ErrKindNotDocumented, want: "No documentation:"},
		{name: "request failed", err: forbidden, want: "Schema request failed: "},
		{name: "no properties", node: explainRoot(t, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Opaque"}), want: "No fields documented"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := models.NewExplainFolder(explainDeps(), deploymentSpec, tt.node, tt.err)
			rows := f.Lines(t.Context(), 0, 10)
			if len(rows) != 2 {
				t.Fatalf("rows = %d, want back and status", len(rows))
			}
			_, cells, _, _ := rows[1].Columns()
			if !strings.HasPrefix(cells[0], tt.want) {
				t.Errorf("status = %q, want prefix %q", cells[0], tt.want)
			}
			if _, ok := rows[1].(models.Enterable); ok {
				t.Error("status row is enterable")
			}
			if f.KubectlCommand() == "" {
				t.Error("no kubectl command; the user cannot reproduce the failure")
			}
		})
	}
}

func TestExplainCommandContextAndCoreGroup(t *testing.T) {
	t.Parallel()
	deps := models.Deps{CtxName: "prod", KubectlContext: "dev"}
	spec := models.ExplainSpec{
		GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
	}
	f := models.NewExplainFolder(deps, spec, nil, explain.ErrNotPublished)
	if got := f.KubectlCommand(); got != "kubectl --context prod explain pods --api-version=v1" {
		t.Errorf("command = %q", got)
	}
	if got := strings.Join(f.Path(), "/"); got != "explain/v1/Pod" {
		t.Errorf("path = %q", got)
	}
}

var _ table.List = (*models.ExplainFolder)(nil)
