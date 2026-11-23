package models

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

// installOpenCRD installs a namespaced CRD that keeps any field and has no
// status subresource, so a create can carry the status a controller would
// write.
func installOpenCRD(t *testing.T, cfg *rest.Config, group, kind, plural, version string) {
	t.Helper()
	ctx := t.Context()
	apiext, err := apiextclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	preserve := true
	crd := &apiextensionsv1.CustomResourceDefinition{
		Name: plural + "." + group,
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: plural, Singular: strings.ToLower(kind), Kind: kind, ListKind: kind + "List"},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: version, Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: &preserve}},
			}},
		},
	}
	if _, err := apiext.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD %s: %v", crd.Name, err)
	}
	kctesting.Eventually(t, 10*time.Second, 100*time.Millisecond, func() bool {
		latest, err := apiext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, c := range latest.Status.Conditions {
			if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
				return true
			}
		}
		return false
	})
}

// The owners folder of a Flux-applied Deployment names its Kustomization
// and checks the claim against the inventory, following changes to it;
// the Kustomization's diagnosis links its source at the offered revision.
func TestGitOpsOwnersAndDiagnosisEnvtest(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	ctx := t.Context()
	installOpenCRD(t, cfg, "kustomize.toolkit.fluxcd.io", "Kustomization", "kustomizations", "v1")
	installOpenCRD(t, cfg, "source.toolkit.fluxcd.io", "GitRepository", "gitrepositories", "v1")
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	cli, err := crclient.New(cfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, cli, &corev1.Namespace{Name: "shop"})
	mustCreate(t, cli, &corev1.Namespace{Name: "flux-system"})
	labels := map[string]string{"app": "web"}
	dep := &appsv1.Deployment{Namespace: "shop", Name: "web", Labels: fluxLabels("apps"), Spec: appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: corev1.PodTemplateSpec{Labels: labels, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}}},
	}}
	mustCreate(t, cli, dep)
	mustCreate(t, cli, uobj("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "repo", map[string]any{
		"spec":   map[string]any{"url": "https://git.example.com/shop.git"},
		"status": map[string]any{"artifact": map[string]any{"revision": "main@sha1:bbb"}},
	}))
	ks := kustomization("apps", map[string]any{
		"lastAppliedRevision": "main@sha1:aaa",
		"conditions":          []any{ready("True", "ReconciliationSucceeded", "", 1)},
		"inventory":           inventory("shop_web_apps_Deployment")["inventory"],
	}, map[string]any{"path": "./apps", "sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}})
	mustCreate(t, cli, ks)

	cl, err := kccluster.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}

	waitRow := func(f Folder, id, want string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		var cells []string
		for {
			f.Len(ctx)
			if _, r, ok := f.Find(ctx, id); ok {
				_, cells, _, _ = r.Columns()
				if len(cells) > 3 && strings.Contains(cells[3], want) {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("row %s = %q, want %q", id, cells, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	owners, err := NewRelatedFolder(ctx, deps, RelatedOwners, relDeploymentsGVR, "shop", "web", []string{"deployments", "web"})
	if err != nil {
		t.Fatalf("owners: %v", err)
	}
	defer owners.Close()
	ownerID := "kustomize.toolkit.fluxcd.io/v1/kustomizations/flux-system/apps"
	waitRow(owners, ownerID, "listed in its inventory")

	// The next apply drops the Deployment from the inventory: the row
	// follows the watch on the Kustomization.
	latest := &unstructured.Unstructured{}
	latest.SetGroupVersionKind(ks.GroupVersionKind())
	if err := cli.Get(ctx, crclient.ObjectKey{Namespace: "flux-system", Name: "apps"}, latest); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(latest.Object, nil, "status", "inventory", "entries"); err != nil {
		t.Fatal(err)
	}
	if err := cli.Update(ctx, latest); err != nil {
		t.Fatal(err)
	}
	waitRow(owners, ownerID, "label only: not in its inventory")

	diag, err := newDiagnosisFolder(deps, kustomizationsGVR, "flux-system", "apps", []string{"kustomizations", "apps"})
	if err != nil {
		t.Fatal(err)
	}
	defer diag.(interface{ Close() }).Close()
	waitRow(diag, "summary/source", "GitRepository flux-system/repo at main@sha1:bbb")
	waitRow(diag, "finding/source-ahead", "Source offers main@sha1:bbb, applied is main@sha1:aaa")
	waitRow(diag, "backend/source.toolkit.fluxcd.io/v1/gitrepositories/flux-system/repo", "spec.sourceRef")
}
