package models_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sttts/kc/internal/models"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestObjectRowManifest(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	newRow := func() *models.ObjectRow {
		return models.NewObjectRow("id", []string{"/web"}, []string{"web"}, gvr, "ns", "web", models.WhiteStyle())
	}

	t.Run("without a reader", func(t *testing.T) {
		t.Parallel()
		if _, err := newRow().Manifest(t.Context()); !errors.Is(err, models.ErrNoViewContent) {
			t.Fatalf("err = %v, want ErrNoViewContent", err)
		}
	})

	t.Run("with a reader", func(t *testing.T) {
		t.Parallel()
		want := &unstructured.Unstructured{}
		want.SetName("web")
		var src models.ManifestSource = newRow().WithManifest("kind-a", func(context.Context) (*unstructured.Unstructured, error) {
			return want, nil
		})
		got, err := src.Manifest(t.Context())
		if err != nil || got != want {
			t.Fatalf("Manifest() = %v, %v", got, err)
		}
		if src.ContextName() != "kind-a" {
			t.Fatalf("ContextName() = %q", src.ContextName())
		}
	})
}
