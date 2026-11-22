package models

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

// TestOverviewCertificatesOnARealCache runs the engine against an API server
// of its own: without cert-manager the certificate row is n/a because the
// kind is not served; once the CRD is installed, a new overview reads
// Certificates through its cache as unstructured objects and lists the
// failing and expiring ones.
func TestOverviewCertificatesOnARealCache(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	ctx := t.Context()

	waitCert := func(e *overviewEngine, what string, cond func(overviewFinding) bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		var last overviewFinding
		for time.Now().Before(deadline) {
			r, err := e.snapshot()
			if err != nil {
				t.Fatalf("engine: %v", err)
			}
			if f, ok := r.finding("risks/certificates"); ok {
				last = f
				if cond(f) {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("waited 30s for %s: last certificates finding value=%q unavailable=%q targets=%v", what, last.value, last.unavailable, targetKeys(last))
	}
	newEngine := func() *overviewEngine {
		cl, err := kccluster.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = cl.Start(ctx) }()
		e := newOverviewEngine(Deps{Cl: cl, Ctx: ctx}, AllNamespaces)
		t.Cleanup(e.close)
		return e
	}

	before := newEngine()
	waitCert(before, "the row to say cert-manager is not served", func(f overviewFinding) bool {
		return f.value == "n/a" && f.unavailable == "certificates.cert-manager.io: not served by this cluster"
	})
	before.close()

	installCertificateCRD(t, cfg)
	cli, err := crclient.New(cfg, crclient.Options{Scheme: overviewTestScheme})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, cli, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "certs"}})
	now := time.Now()
	at := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	for _, c := range []crclient.Object{
		certificate("certs", "fine", "True", "Ready", at(60*24*time.Hour)),
		certificate("certs", "expiring", "True", "Ready", at(5*24*time.Hour)),
		certificate("certs", "broken", "False", "Failed", ""),
	} {
		mustCreate(t, cli, c)
	}

	after := newEngine()
	waitCert(after, "the failing and the expiring certificate", func(f overviewFinding) bool {
		return f.unavailable == "" && slices.Equal(targetKeys(f), []string{"certificates:certs/broken", "certificates:certs/expiring"})
	})
}

// installCertificateCRD installs a minimal certificates.cert-manager.io CRD
// without a status subresource, so a create can set the status.
func installCertificateCRD(t *testing.T, cfg *rest.Config) {
	t.Helper()
	ctx := t.Context()
	apiext, err := apiextclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	preserve := true
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "certificates.cert-manager.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "cert-manager.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "certificates", Singular: "certificate", Kind: "Certificate", ListKind: "CertificateList"},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object", XPreserveUnknownFields: &preserve,
				}},
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
		for _, c := range latest.Status.Conditions {
			if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
				return true
			}
		}
		return false
	})
}
