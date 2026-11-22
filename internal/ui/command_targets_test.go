package ui

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

// White-box: commandTargets and isCommandApplicableToTargets are the
// unexported glue between the panel selection and the F9 menu.

var (
	podsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

func targetRow(gvr schema.GroupVersionResource, namespace, name string) *models.ObjectRow {
	id := gvr.GroupVersion().String() + "/" + gvr.Resource + "/" + namespace + "/" + name
	return models.NewObjectRow(id, []string{name}, nil, gvr, namespace, name, nil)
}

func marked(items ...models.Item) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		out = append(out, Item{Item: it, Selected: true})
	}
	return out
}

func TestCommandTargetsTemplateData(t *testing.T) {
	t.Parallel()

	cursor := targetRow(podsGVR, "default", "cursor")
	webDev := targetRow(podsGVR, "dev", "web")
	webProd := targetRow(podsGVR, "prod", "web")
	api := targetRow(deploymentsGVR, "prod", "api")

	tests := []struct {
		name        string
		highlighted models.Item
		marked      []Item
		want        []CommandItemContext
	}{
		{
			name: "nothing highlighted, nothing marked",
		},
		{
			name:        "highlighted row is the single item when nothing is marked",
			highlighted: cursor,
			want: []CommandItemContext{
				{Name: "cursor", Namespace: "default", Version: "v1", Resource: "pods"},
			},
		},
		{
			name:        "unmarked highlighted row does not join the marked rows",
			highlighted: cursor,
			marked:      marked(webDev),
			want: []CommandItemContext{
				{Name: "web", Namespace: "dev", Version: "v1", Resource: "pods"},
			},
		},
		{
			name:        "all-namespace list keeps same-name objects apart",
			highlighted: cursor,
			marked:      marked(webDev, webProd),
			want: []CommandItemContext{
				{Name: "web", Namespace: "dev", Version: "v1", Resource: "pods"},
				{Name: "web", Namespace: "prod", Version: "v1", Resource: "pods"},
			},
		},
		{
			name:   "each item keeps its own GVR",
			marked: marked(webProd, api),
			want: []CommandItemContext{
				{Name: "web", Namespace: "prod", Version: "v1", Resource: "pods"},
				{Name: "api", Namespace: "prod", Group: "apps", Version: "v1", Resource: "deployments"},
			},
		},
		{
			name:        "highlighted marked row appears once, in marked order",
			highlighted: webProd,
			marked:      marked(webDev, webProd),
			want: []CommandItemContext{
				{Name: "web", Namespace: "dev", Version: "v1", Resource: "pods"},
				{Name: "web", Namespace: "prod", Version: "v1", Resource: "pods"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			targets := commandTargets(tc.highlighted, tc.marked)
			got := commandItemContexts(targets, schema.GroupVersionResource{})
			if len(got) != len(tc.want) {
				t.Fatalf("got %d items %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("item %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestRenderCommandMultipleItems(t *testing.T) {
	t.Parallel()

	items := commandTargets(targetRow(podsGVR, "default", "cursor"), marked(
		targetRow(podsGVR, "dev", "web"),
		targetRow(deploymentsGVR, "prod", "api"),
	))
	tmpl := `{{range .Items}}{{.Resource}}.{{.Group}}/{{.Name}}@{{.Namespace}} {{end}}first={{.Name}}`

	got, err := RenderCommand(tmpl, items, schema.GroupVersionResource{})
	if err != nil {
		t.Fatalf("RenderCommand: %v", err)
	}
	want := "pods./web@dev deployments.apps/api@prod first=web"
	if got != want {
		t.Fatalf("RenderCommand = %q, want %q", got, want)
	}
}

func TestRenderCommandFallbackGVR(t *testing.T) {
	t.Parallel()

	items := []models.Item{namespaceCommandItem{namespace: "dev"}}
	got, err := RenderCommand("{{.Resource}} {{.Namespace}}", items, podsGVR)
	if err != nil {
		t.Fatalf("RenderCommand: %v", err)
	}
	if want := "pods dev"; got != want {
		t.Fatalf("RenderCommand = %q, want %q", got, want)
	}
}

func TestIsCommandApplicableToTargets(t *testing.T) {
	t.Parallel()

	pod := targetRow(podsGVR, "dev", "web")
	otherPod := targetRow(podsGVR, "prod", "web")
	deploy := targetRow(deploymentsGVR, "prod", "api")

	single := appconfig.CommandConfig{Name: "single", Type: appconfig.CommandTypeSelector}
	multi := appconfig.CommandConfig{Name: "multi", Type: appconfig.CommandTypeSelector, SupportsMultiSelection: true}
	podsOnly := appconfig.CommandConfig{
		Name: "pods", Type: appconfig.CommandTypeSelector, SupportsMultiSelection: true,
		ShowFor: appconfig.CommandShowForConfig{Resources: []string{"pods"}},
	}

	tests := []struct {
		name    string
		cmd     appconfig.CommandConfig
		targets []models.Item
		want    bool
	}{
		{name: "single command, one target", cmd: single, targets: []models.Item{pod}, want: true},
		{name: "single command hidden for two targets", cmd: single, targets: []models.Item{pod, otherPod}, want: false},
		{name: "multi command, two targets", cmd: multi, targets: []models.Item{pod, otherPod}, want: true},
		{name: "selector command needs a target", cmd: multi, want: false},
		{name: "showFor matches every target", cmd: podsOnly, targets: []models.Item{pod, otherPod}, want: true},
		{name: "showFor rejects a mixed selection", cmd: podsOnly, targets: []models.Item{pod, deploy}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isCommandApplicableToTargets(tc.cmd, tc.targets, ""); got != tc.want {
				t.Fatalf("isCommandApplicableToTargets = %v, want %v", got, tc.want)
			}
		})
	}
}
