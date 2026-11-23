package models

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// /diagnosis inside an Ingress reads the Ingress, its Services and its
// IngressClass from the cache, checks the TLS Secret by metadata, follows
// Service changes, and its backend rows open the Service diagnosis.
func TestIngressDiagnosisEnvtest(t *testing.T) {
	t.Parallel()
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := t.Context()
	ns := kctesting.UniqueName("ingress-diag")
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	mustCreate(t, cli, &corev1.Service{Namespace: ns, Name: "web", Spec: corev1.ServiceSpec{
		Ports: []corev1.ServicePort{{Name: "http", Port: 80}},
	}})
	mustCreate(t, cli, &corev1.Secret{Namespace: ns, Name: "web-tls", Data: map[string][]byte{"tls.crt": []byte("x")}})
	prefix := networkingv1.PathTypePrefix
	path := func(p, svc string) networkingv1.HTTPIngressPath {
		return networkingv1.HTTPIngressPath{Path: p, PathType: &prefix, Backend: networkingv1.IngressBackend{
			Service: &networkingv1.IngressServiceBackend{Name: svc, Port: networkingv1.ServiceBackendPort{Number: 80}},
		}}
	}
	mustCreate(t, cli, &networkingv1.Ingress{Namespace: ns, Name: "web", Spec: networkingv1.IngressSpec{
		IngressClassName: new(kctesting.UniqueName("no-such-class")),
		TLS: []networkingv1.IngressTLS{
			{Hosts: []string{"shop.example.com"}, SecretName: "web-tls"},
			{Hosts: []string{"shop.example.com"}, SecretName: "gone-tls"},
		},
		Rules: []networkingv1.IngressRule{{Host: "shop.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{path("/", "web"), path("/api", "api")},
		}}}},
	}})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}

	start := time.Now()
	folder, err := newDiagnosisFolder(deps, ingressGVR, ns, "web", []string{"ingresses", "web"})
	if err != nil {
		t.Fatalf("diagnosis folder: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("building the folder took %v", d)
	}
	defer folder.(interface{ Close() }).Close()

	waitForRow := func(id, want string) Item {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		var cells []string
		for {
			folder.Len(ctx)
			if _, r, ok := folder.Find(ctx, id); ok {
				_, cells, _, _ = r.Columns()
				if len(cells) > 3 && strings.Contains(cells[3], want) {
					return r.(Item)
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("row %s = %q, want %q", id, cells, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitForRow("finding/backend-missing/1.2", "Backend Service "+ns+"/api does not exist")
	waitForRow("finding/class-missing", "does not exist")
	waitForRow("finding/secret-missing/tls/2", "TLS Secret "+ns+"/gone-tls does not exist")
	if _, _, ok := folder.Find(ctx, "finding/secret-missing/tls/1"); ok {
		t.Error("the existing TLS Secret is reported missing")
	}
	backend := waitForRow("backend/v1/services/"+ns+"/web", "-> port 80")

	// The missing Service appears; the watch on Services refreshes.
	mustCreate(t, cli, &corev1.Service{Namespace: ns, Name: "api", Spec: corev1.ServiceSpec{
		Ports: []corev1.ServicePort{{Port: 80}},
	}})
	waitForRow("backend/v1/services/"+ns+"/api", "-> port 80")

	// Enter on a backend opens the Service's own diagnosis.
	sub, err := backend.(Enterable).Enter()
	if err != nil {
		t.Fatalf("enter backend: %v", err)
	}
	defer sub.(interface{ Close() }).Close()
	rel, ok := sub.(*RelatedObjectsFolder)
	if !ok || rel.Kind() != RelatedDiagnosis || rel.gvr != serviceGVR || rel.name != "web" {
		t.Fatalf("backend opened %T %+v, want the Service diagnosis of web", sub, sub)
	}
}
