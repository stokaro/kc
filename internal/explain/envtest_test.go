package explain_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/openapi"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/stokaro/kc/internal/explain"
)

// fixtureCRD defines a kind named Deployment in another group, so the
// lookup must use the whole GVK.
func fixtureCRD() *apiextensionsv1.CustomResourceDefinition {
	str := func(desc string) apiextensionsv1.JSONSchemaProps {
		return apiextensionsv1.JSONSchemaProps{Type: "string", Description: desc}
	}
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "deployments.fixture.kc.dev"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "fixture.kc.dev",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "deployments", Singular: "deployment", Kind: "Deployment", ListKind: "DeploymentList"},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type:        "object",
					Description: "Fixture deployment.",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"spec": {
							Type:        "object",
							Description: "Spec of the fixture deployment.",
							Required:    []string{"target"},
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"target":       str("Target names the cluster to deploy to."),
								"undocumented": {Type: "integer"},
								"steps": {
									Type: "array",
									Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &apiextensionsv1.JSONSchemaProps{
										Type:       "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{"name": str("Step name.")},
									}},
								},
								"routes": {
									Type: "object",
									AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{Allows: true, Schema: &apiextensionsv1.JSONSchemaProps{
										Type:       "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{"backend": str("Backend service.")},
									}},
								},
							},
						},
					},
				}},
			}},
		},
	}
}

// TestAgainstAPIServer reads the real /openapi/v3 of a kube-apiserver: a
// built-in kind and a CRD with the same kind name must both resolve.
func TestAgainstAPIServer(t *testing.T) {
	env := &envtest.Environment{CRDs: []*apiextensionsv1.CustomResourceDefinition{fixtureCRD()}}
	cfg, err := env.Start()
	if err != nil {
		t.Skipf("envtest unavailable: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	client := openapi.NewClientWithContext(dc.RESTClient())
	loader := explain.NewLoader(4)
	ctx := t.Context()

	appsDoc, err := loader.Load(ctx, "envtest", client, appsV1)
	if err != nil {
		t.Fatal(err)
	}
	apps := root(t, appsDoc, appsDeployment)
	for _, tc := range []struct {
		path     []string
		typ      string
		required bool
	}{
		{[]string{"spec", "template"}, "PodTemplateSpec", true},
		{[]string{"spec", "template", "spec", "containers"}, "[]Container", true},
		{[]string{"spec", "template", "spec", "containers", "name"}, "string", true},
		{[]string{"spec", "template", "spec", "containers", "resources", "limits"}, "map[string]Quantity", false},
		{[]string{"spec", "strategy", "rollingUpdate", "maxSurge"}, "IntOrString", false},
		{[]string{"metadata", "labels"}, "map[string]string", false},
		{[]string{"metadata", "creationTimestamp"}, "string", false},
	} {
		f := field(t, apps, tc.path...)
		if f.Type != tc.typ || f.Required != tc.required {
			t.Errorf("apps/v1 Deployment %v: type %q required %v, want %q %v", tc.path, f.Type, f.Required, tc.typ, tc.required)
		}
		if f.Description == "" {
			t.Errorf("apps/v1 Deployment %v: no description", tc.path)
		}
	}
	policy := field(t, apps, "spec", "template", "spec", "containers", "imagePullPolicy")
	if !slices.Contains(policy.Enum, "IfNotPresent") {
		t.Errorf("imagePullPolicy enum = %v", policy.Enum)
	}

	// CRD schemas reach /openapi/v3 a moment after the CRD is established.
	var crdDoc *explain.Document
	deadline := time.Now().Add(30 * time.Second)
	for {
		crdDoc, err = loader.Load(ctx, "envtest", client, fixtureV1)
		if err == nil {
			if _, rerr := crdDoc.Root(fixtureDeployment); rerr == nil {
				break
			} else {
				err = rerr
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture CRD schema not published under /openapi/v3 within 30s; last error: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	crd := root(t, crdDoc, fixtureDeployment)
	if f := field(t, crd, "spec", "target"); f.Type != "string" || !f.Required || f.Description == "" {
		t.Errorf("fixture spec.target: %+v", f)
	}
	if f := field(t, crd, "spec", "undocumented"); f.Description != "" {
		t.Errorf("fixture spec.undocumented has description %q", f.Description)
	}
	if f := field(t, crd, "spec", "steps"); f.Type != "[]Object" || f.Node == nil {
		t.Errorf("fixture spec.steps: type %q nested %v", f.Type, f.Node != nil)
	}
	if f := field(t, crd, "spec", "routes", "backend"); f.Description != "Backend service." {
		t.Errorf("fixture spec.routes.backend description %q", f.Description)
	}
	if _, ok := field(t, crd, "spec").Node.Field("template"); ok {
		t.Error("fixture Deployment shows the apps/v1 spec")
	}

	// A group-version the server does not serve is missing documentation,
	// not a failure.
	_, err = loader.Load(ctx, "envtest", client, schema.GroupVersion{Group: "nope.kc.dev", Version: "v1"})
	if !errors.Is(err, explain.ErrNotPublished) {
		t.Errorf("unknown group-version: err = %v, want ErrNotPublished", err)
	}
}
