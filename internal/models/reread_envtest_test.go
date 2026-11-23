package models

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

// A list of resource types keeps its API discovery until the periodic
// refresh; C-r drops it, so a CRD installed since shows at once.
func TestRereadResourceTypesRunsDiscoveryAgain(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	// No periodic discovery refresh during the test.
	cl, err := kccluster.New(cfg, kccluster.WithRefreshInterval(time.Hour))
	if err != nil {
		t.Fatalf("kccluster: %v", err)
	}
	ctx := t.Context()
	kctesting.RunInBackground(t, "cluster", cl.Start)

	ns := &corev1.Namespace{Name: "reread-ns"}
	mustCreate(t, cl.GetClient(), ns)
	deps := Deps{
		Cl:      cl,
		Ctx:     ctx,
		CtxName: "envtest",
		KubeConfig: clientcmdapi.Config{
			CurrentContext: "envtest",
			Contexts:       map[string]*clientcmdapi.Context{"envtest": {Namespace: ns.Name}},
		},
		AppConfig: appconfig.Default(),
	}
	deps.AppConfig.Resources.ShowNonEmptyOnly = false
	folder := NewNamespacedResourcesFolder(deps, ns.Name, []string{"namespaces", ns.Name})
	waitFolder(t, folder)

	apiext, err := apiextclient.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("apiext client: %v", err)
	}
	crd := &apiextensionsv1.CustomResourceDefinition{
		Name: "gadgets.reread.example.com",
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "reread.example.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "gadgets", Singular: "gadget", Kind: "Gadget", ListKind: "GadgetList"},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"}},
			}},
		},
	}
	if _, err := apiext.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD: %v", err)
	}
	kctesting.Eventually(t, 10*time.Second, 100*time.Millisecond, func() bool {
		latest, err := apiext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, cond := range latest.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				return true
			}
		}
		return false
	})

	listsGadgets := func() bool {
		rows := folder.Lines(ctx, 0, folder.Len(ctx))
		for _, r := range rows {
			if id, _, _, ok := r.Columns(); ok && strings.Contains(id, "gadgets") {
				return true
			}
		}
		return false
	}
	folder.Refresh()
	if listsGadgets() {
		t.Fatal("a refresh listed the new type; the test needs cached discovery to prove the reread")
	}
	// The API server may serve the new group a moment after Established.
	kctesting.Eventually(t, 10*time.Second, 100*time.Millisecond, func() bool {
		folder.Reread(ctx)
		return listsGadgets()
	})
}
