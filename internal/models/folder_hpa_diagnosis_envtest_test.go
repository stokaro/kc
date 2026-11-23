package models

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// /diagnosis inside an HPA maps the scale target through the RESTMapper,
// reads it from the cache and names the container without a CPU request.
// envtest runs no HPA controller, so the status is empty.
func TestHPADiagnosisEnvtest(t *testing.T) {
	t.Parallel()
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = autoscalingv2.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := t.Context()
	ns := kctesting.UniqueName("hpa-diag")
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	labels := map[string]string{"app": "web"}
	mustCreate(t, cli, &appsv1.Deployment{Namespace: ns, Name: "web", Spec: appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: corev1.PodTemplateSpec{Labels: labels, Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", Image: "busybox", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}},
			{Name: "side", Image: "busybox"},
		}}},
	}})
	mustCreate(t, cli, &autoscalingv2.HorizontalPodAutoscaler{Namespace: ns, Name: "web", Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"},
		MaxReplicas:    5,
		Metrics:        []autoscalingv2.MetricSpec{hpaCPU(50)},
	}})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}

	rows, ok := builtinObjectRows(hpaGVR)
	if !ok {
		t.Fatal("HPAs have no object rows")
	}
	var diag Enterable
	for _, r := range rows(deps, ns, "web", []string{"horizontalpodautoscalers", "web"}) {
		if id, _, _, _ := r.Columns(); strings.HasSuffix(id, "/"+DiagnosisSegment) {
			diag, _ = r.(Enterable)
		}
	}
	if diag == nil {
		t.Fatal("no /diagnosis row inside the HPA")
	}
	folder, err := diag.Enter()
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	defer folder.(interface{ Close() }).Close()

	deadline := time.Now().Add(20 * time.Second)
	for {
		folder.Len(ctx)
		_, req, okReq := folder.Find(ctx, "finding/hpa/requests/0")
		_, _, okStatus := folder.Find(ctx, "finding/hpa/no-status")
		if okReq && okStatus {
			_, cells, _, _ := req.Columns()
			if len(cells) < 4 || !strings.Contains(cells[3], "containers without one: side") {
				t.Fatalf("requests row %q", cells)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no requests and no-status findings (requests %v, status %v)", okReq, okStatus)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The target is cited by its full GVR.
	if _, _, ok := folder.Find(ctx, "finding/hpa/requests/0/apps/v1/deployments/"+ns+"/web"); !ok {
		t.Error("the requests finding does not cite the Deployment")
	}
}
