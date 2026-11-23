package models

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

// /diagnosis inside a Service reads the Service, its pods and its
// EndpointSlices from the cache of a real API server and follows changes.
// envtest runs no EndpointSlice controller, so the test writes the slice.
func TestServiceDiagnosisEnvtest(t *testing.T) {
	t.Parallel()
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = discoveryv1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := t.Context()
	const ns = "diagnosis"
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	mustCreate(t, cli, &corev1.Service{Namespace: ns, Name: "web", Spec: corev1.ServiceSpec{
		Selector: map[string]string{"app": "web"},
		Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http")}},
	}})
	// The pod's label does not match the selector: a typo.
	pod := &corev1.Pod{Namespace: ns, Name: "web-1", Labels: map[string]string{"app": "wbe"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}}}}
	mustCreate(t, cli, pod)

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	go func() { _ = cl.Start(ctx) }()
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}

	ctor, ok := ChildFor(serviceGVR)
	if !ok {
		t.Fatalf("services have no child folder")
	}
	child := ctor(deps, ns, "web", []string{"services", "web"})
	waitFolder(t, child)
	_, row, ok := child.Find(ctx, ns+"/web/"+DiagnosisSegment)
	if !ok {
		t.Fatalf("no /diagnosis row in the Service folder")
	}
	start := time.Now()
	folder, err := row.(Enterable).Enter()
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	// Enter runs on the UI thread and must not wait for the cache.
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Enter took %v", d)
	}
	defer folder.(interface{ Close() }).Close()

	waitForRow := func(id, want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		var cells []string
		for {
			folder.Len(ctx)
			if _, r, ok := folder.Find(ctx, id); ok {
				_, cells, _, _ = r.Columns()
				if len(cells) > 3 && cells[3] == want {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("row %s = %q, want %q", id, cells, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitForRow("finding/no-pods", "No running pod matches the selector app=web")
	waitForRow("finding/no-pods/v1/pods/"+ns+"/web-1", "labels app=wbe")

	// Fix the label; the watch on pods refreshes the folder. envtest
	// runs no kubelet, so the pod has no IP and is not ready.
	pod.Labels = map[string]string{"app": "web"}
	if err := cli.Update(ctx, pod); err != nil {
		t.Fatalf("relabel: %v", err)
	}
	waitForRow("summary/pods", "1 selected, 0 ready")
	waitForRow("backend/v1/pods/"+ns+"/web-1", "not ready; no pod IP; http=8080")

	// A slice written by hand is counted through the watch on slices.
	mustCreate(t, cli, &discoveryv1.EndpointSlice{
		Namespace: ns, Name: "web-abc", Labels: map[string]string{discoveryv1.LabelServiceName: "web"},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: new(false)}}},
	})
	waitForRow("summary/endpoints", "1 in 1 slices, 0 ready, 0 serving, 0 terminating, IPv4")
}
