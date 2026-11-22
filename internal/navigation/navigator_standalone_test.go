package navigation

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/explain"
	"github.com/stokaro/kc/internal/models"
)

func TestNavigatorStandalonePath(t *testing.T) {
	ctx := t.Context()
	root := mkFolder(nil, "namespaces")
	ns := mkFolder([]string{"namespaces"}, "default")
	objs := mkFolder([]string{"namespaces", "default"}, "web")

	nav := NewNavigator(root)
	nav.SetSelectionID("namespaces")
	nav.Push(ns)
	nav.SetSelectionID("default")
	nav.Push(objs)
	before := nav.Path(ctx)
	if before != "/namespaces/default" {
		t.Fatalf("path before = %q", before)
	}

	spec := models.ExplainSpec{
		GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
	}
	nav.SetSelectionID("web")
	nav.Push(models.NewExplainFolder(models.Deps{}, spec, nil, explain.ErrNotPublished))
	if got := nav.Path(ctx); got != "/explain/apps/v1/Deployment" {
		t.Errorf("explain path = %q", got)
	}

	nav.Back()
	if got := nav.Path(ctx); got != before {
		t.Errorf("path after back = %q, want %q", got, before)
	}
	if got := nav.CurrentSelectionID(); got != "web" {
		t.Errorf("selection after back = %q, want web", got)
	}
}
