package models

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// TestOverviewInfoWorkloadOnARealCache reads ReplicaSets as metadata
// through a kc cluster's cache and groups a pod by its Deployment. envtest
// runs no controllers, so the test creates the ReplicaSet and the pod with
// their owner references itself.
func TestOverviewInfoWorkloadOnARealCache(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := kctesting.UniqueName("overview-workload")
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	t.Cleanup(func() { _ = cli.Delete(ctx, &corev1.Namespace{Name: ns}) })

	labels := map[string]string{"app": "web"}
	template := corev1.PodTemplateSpec{
		Labels: labels,
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
	}
	dep := &appsv1.Deployment{
		Namespace: ns, Name: "web",
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](3), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
	}
	mustCreate(t, cli, dep)
	rs := &appsv1.ReplicaSet{
		Namespace: ns, Name: "web-abc", OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID, Controller: new(true),
		}},
		Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
	}
	mustCreate(t, cli, rs)
	pod := &corev1.Pod{
		Namespace: ns, Name: "web-abc-1", Labels: labels, OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: new(true),
		}},
		Spec: template.Spec,
	}
	mustCreate(t, cli, pod)

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	info := NewOverviewInfo(Deps{Cl: cl, Ctx: ctx}, ns)
	defer info.Close()

	focus := InfoFocus{Workload: &InfoWorkload{GVR: ovDeploymentsGVR, Namespace: ns, Name: "web"}}
	deadline := time.Now().Add(30 * time.Second)
	var last InfoSummary
	for time.Now().Before(deadline) {
		last = info.Summary(focus)
		for _, b := range last.Bars {
			if b.Label == "Pods" && b.Detail == "0/3 ready, 1 pods" {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("waited 30s for the Deployment's pod: last summary %+v", last)
}
