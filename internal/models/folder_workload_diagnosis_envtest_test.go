package models

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// /diagnosis inside a Deployment reads the Deployment, its ReplicaSets,
// pods, events and claims from the cache of a real API server and follows
// changes. envtest runs no controllers, kubelet or scheduler, so the test
// writes the ReplicaSet, the pod status and the event itself, and nothing
// garbage-collects the old ReplicaSet when the Deployment is replaced.
func TestWorkloadDiagnosisEnvtest(t *testing.T) {
	t.Parallel()
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := t.Context()
	ns := kctesting.UniqueName("wl-diagnosis")
	mustCreate(t, cli, &corev1.Namespace{Name: ns})

	labels := map[string]string{"app": "web"}
	template := corev1.PodTemplateSpec{
		Labels: labels,
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
	}
	newDeployment := func() *appsv1.Deployment {
		return &appsv1.Deployment{Namespace: ns, Name: "web", Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template,
		}}
	}
	dep := newDeployment()
	mustCreate(t, cli, dep)
	rs := &appsv1.ReplicaSet{
		Namespace: ns, Name: "web-1", Labels: labels, OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: dep.UID, Controller: new(true)},
		},
		Spec: appsv1.ReplicaSetSpec{Replicas: new(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template},
	}
	mustCreate(t, cli, rs)
	pod := &corev1.Pod{
		Namespace: ns, Name: "web-1-a", Labels: labels, OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: rs.UID, Controller: new(true)},
		},
		Spec: template.Spec,
	}
	mustCreate(t, cli, pod)
	unschedulable := func() {
		t.Helper()
		pod.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: "0/1 nodes are available: 1 Insufficient cpu.", LastTransitionTime: metav1.Now(),
		}}}
		if err := cli.Status().Update(ctx, pod); err != nil {
			t.Fatalf("pod status: %v", err)
		}
	}
	unschedulable()
	now := metav1.Now()
	mustCreate(t, cli, &corev1.Event{
		Namespace: ns, Name: "web-1-a.sched",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", APIVersion: "v1", Namespace: ns, Name: pod.Name, UID: pod.UID},
		Type:           corev1.EventTypeWarning, Reason: "FailedScheduling", Message: "0/1 nodes are available",
		FirstTimestamp: now, LastTimestamp: now, Count: 1,
	})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	go func() { _ = cl.Start(ctx) }()
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}

	ctor, ok := ChildFor(relDeploymentsGVR)
	if !ok {
		t.Fatalf("deployments have no child folder")
	}
	child := ctor(deps, ns, "web", []string{"deployments", "web"})
	waitFolder(t, child)
	_, row, ok := child.Find(ctx, ns+"/web/"+DiagnosisSegment)
	if !ok {
		t.Fatalf("no /diagnosis row in the Deployment folder")
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

	// waitFor polls until the row id is present (or absent when present is
	// false) with a last cell that starts with prefix.
	waitFor := func(id, prefix string, present bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		var cells []string
		for {
			folder.Len(ctx)
			_, r, ok := folder.Find(ctx, id)
			cells = nil
			if ok {
				_, cells, _, _ = r.Columns()
			}
			matched := ok && len(cells) > 3 && strings.HasPrefix(cells[3], prefix)
			if matched == present {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("row %s = %q, want present=%v with prefix %q", id, cells, present, prefix)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	finding := "finding/unschedulable/web-1-a"
	waitFor(finding, "Pod web-1-a cannot be scheduled: Unschedulable (observed ", true)
	waitFor(finding+"/v1/pods/"+ns+"/web-1-a", "status.conditions[PodScheduled]=False Unschedulable, uid "+string(pod.UID), true)
	waitFor(finding+"/v1/events/"+ns+"/web-1-a.sched", "Warning FailedScheduling: 0/1 nodes are available (x1, last ", true)
	waitFor("finding/unavailable", "1 of 1 replicas are unavailable", true)
	waitFor("finding/unavailable/maybe", "The pod problems below may explain this", true)

	// Recovery: the pod is scheduled and ready, the Deployment available.
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()},
		{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()},
	}}
	if err := cli.Status().Update(ctx, pod); err != nil {
		t.Fatalf("pod status: %v", err)
	}
	dep.Status = appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if err := cli.Status().Update(ctx, dep); err != nil {
		t.Fatalf("deployment status: %v", err)
	}
	waitFor("finding/no-problem", "No problem found by the checks kc runs", true)
	waitFor(finding, "", false)
	waitFor("summary/pods", "1 owned, 1 ready", true)

	// Replace the Deployment. The old ReplicaSet and its pod stay (no
	// garbage collector) and point at the old UID; the pod fails again.
	unschedulable()
	if err := cli.Delete(ctx, dep); err != nil {
		t.Fatalf("delete: %v", err)
	}
	replaced := newDeployment()
	mustCreate(t, cli, replaced)
	if replaced.UID == dep.UID {
		t.Fatalf("the recreated Deployment kept UID %s", dep.UID)
	}
	waitFor("summary/object", "deployments.apps/web, uid "+string(replaced.UID), true)
	waitFor("summary/pods", "0 owned, 0 ready", true)
	waitFor(finding, "", false)
}
