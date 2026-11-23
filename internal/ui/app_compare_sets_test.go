package ui

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/setdiff"
)

func TestSetCompareChoicesAndTitle(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	obj := func(ns, name string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"namespace": ns, "name": name}, "spec": map[string]any{"replicas": int64(1)}}}
	}
	left := setdiff.Set{Context: "staging", GVR: gvr, Namespace: "shop-stg", Objects: []unstructured.Unstructured{obj("shop-stg", "web"), obj("shop-stg", "old")}}
	right := setdiff.Set{Context: "prod", GVR: gvr, Namespace: "shop", Objects: []unstructured.Unstructured{obj("shop", "web")}}
	r, err := setdiff.Compare(left, right, manifestdiff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	choices := setCompareChoices(r)
	if len(choices) != 2 || choices[0].Label != "only left   old" || choices[1].Label != "equal       web" {
		t.Errorf("choices %+v", choices)
	}
	title := setCompareTitle(r)
	for _, want := range []string{"left staging: deployments.apps in shop-stg", "right prod: deployments.apps in shop", "metadata.namespace ignored"} {
		if !strings.Contains(title, want) {
			t.Errorf("title %q lacks %q", title, want)
		}
	}
	// The kubectl command names the version, so it prints what was
	// compared, and the context of its side.
	cmd := sideViewCommand(models.Deps{CtxName: "prod", KubectlContext: "staging"}, *r.Rows[1].Right)
	if cmd != "kubectl --context prod get deployments.v1.apps web -n shop -o yaml" {
		t.Errorf("view command %q", cmd)
	}
}
