package models

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	table "github.com/stokaro/kc/internal/table"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// The /rollout row of a Deployment opens a folder that groups the
// ReplicaSets and pods the Deployment controls (envtest runs no
// controllers, so the test creates them) and follows a new revision.
func TestRolloutFolderEnvtest(t *testing.T) {
	t.Parallel()
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	ns := kctesting.UniqueName("rollout")
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	labels := map[string]string{"app": "web"}
	dep := &appsv1.Deployment{Namespace: ns, Name: "web", Spec: appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: corev1.PodTemplateSpec{Labels: labels, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "web:2"}}}},
	}}
	mustCreate(t, cli, dep)
	newRS := func(name, revision, image string) *appsv1.ReplicaSet {
		yes := true
		rsLabels := map[string]string{"app": "web", appsv1.DefaultDeploymentUniqueLabelKey: name}
		return &appsv1.ReplicaSet{Namespace: ns, Name: name, Labels: rsLabels,
			Annotations:     map[string]string{"deployment.kubernetes.io/revision": revision},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: dep.UID, Controller: &yes}},
			Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: rsLabels},
				Template: corev1.PodTemplateSpec{Labels: rsLabels, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}}}}}
	}
	rs1 := newRS("web-1", "1", "web:1")
	mustCreate(t, cli, rs1)
	yes := true
	mustCreate(t, cli, &corev1.Pod{Namespace: ns, Name: "web-1-a", Labels: labels,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: rs1.UID, Controller: &yes}},
		Spec:            corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "web:1"}}}})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}
	rows := extraObjectRows(deps, relDeploymentsGVR, ns, "web", []string{"deployments", "web"})
	i := slices.IndexFunc(rows, func(r table.Row) bool { id, _, _, _ := r.Columns(); return strings.HasSuffix(id, "/"+RolloutSegment) })
	if i < 0 {
		t.Fatal("no /rollout row in the Deployment")
	}
	folder, err := rows[i].(Enterable).Enter()
	if err != nil {
		t.Fatal(err)
	}
	defer folder.(interface{ Close() }).Close()
	wait := func(id, want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		var cells []string
		for {
			folder.Len(ctx)
			if _, r, ok := folder.Find(ctx, id); ok {
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
	wait("revision/"+string(rs1.UID)+"/apps/v1/replicasets/"+ns+"/web-1", "revision 1, old")
	wait("revision/"+string(rs1.UID)+"/pod/v1/pods/"+ns+"/web-1-a", "pod of revision 1")

	// The next revision appears through the ReplicaSet watch.
	rs2 := newRS("web-2", "2", "web:2")
	mustCreate(t, cli, rs2)
	wait("revision/"+string(rs2.UID)+"/apps/v1/replicasets/"+ns+"/web-2", "revision 2, current")
	wait("revision/"+string(rs2.UID)+"/changes", "container app image web:1 -> web:2")
}
