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
	"github.com/stokaro/kc/pkg/appconfig"
)

// /timeline inside a Deployment records what the shared cache reports
// about it from a real API server: an image change, a status-only change,
// the retained ReplicaSet, a deletion and a recreation, and nothing after
// the recording is stopped. envtest runs no controllers, so the test
// writes the status and the ReplicaSet itself.
func TestTimelineEnvtest(t *testing.T) {
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
	const ns = "timeline"
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	labels := map[string]string{"app": "web"}
	newDeployment := func(image string) *appsv1.Deployment {
		return &appsv1.Deployment{Namespace: ns, Name: "web", Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
			},
		}}
	}
	dep := newDeployment("busybox:1")
	mustCreate(t, cli, dep)

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
	_, row, ok := child.Find(ctx, ns+"/web/"+TimelineSegment)
	if !ok {
		t.Fatalf("no /timeline row in the Deployment folder")
	}
	start := time.Now()
	folder, err := row.(Enterable).Enter()
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Enter took %v", d)
	}
	tf := folder.(*TimelineFolder)
	defer tf.Close()
	defer tf.rec.Stop()

	cells := func(id string) ([]string, bool) {
		tf.Len(ctx)
		_, r, ok := tf.Find(ctx, id)
		if !ok {
			return nil, false
		}
		_, c, _, _ := r.Columns()
		return c, true
	}
	// waitFor polls until row id has kind and a change containing want.
	waitFor := func(id, kind, want string) []string {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			c, ok := cells(id)
			if ok && len(c) > 3 && c[1] == kind && strings.Contains(c[3], want) {
				return c
			}
			if time.Now().After(deadline) {
				t.Fatalf("row %s = %q, want kind %q with %q", id, c, kind, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	view := func(id string) string {
		t.Helper()
		_, r, ok := tf.Find(ctx, id)
		if !ok {
			t.Fatalf("no row %s", id)
		}
		_, body, lang, _, _, err := r.(Viewable).ViewContent()
		if err != nil || lang != "diff" {
			t.Fatalf("view %s: lang %q, err %v", id, lang, err)
		}
		return body
	}

	initial := waitFor("rev/1", "initial", "first observed revision")
	if initial[2] != dep.ResourceVersion {
		t.Fatalf("initial resourceVersion = %q, want %q", initial[2], dep.ResourceVersion)
	}

	// An image change.
	dep.Spec.Template.Spec.Containers[0].Image = "busybox:2"
	if err := cli.Update(ctx, dep); err != nil {
		t.Fatalf("update: %v", err)
	}
	waitFor("rev/2", "spec", "image app busybox:1 -> busybox:2")
	diff := view("rev/2")
	for _, want := range []string{"gvr apps/v1/deployments, namespace timeline, name web@", "uid " + string(dep.UID), "-      - image: busybox:1", "+      - image: busybox:2"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff lacks %q:\n%s", want, diff)
		}
	}

	// A status-only change is a status entry, and its diff is the status.
	dep.Status = appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, ObservedGeneration: dep.Generation}
	if err := cli.Status().Update(ctx, dep); err != nil {
		t.Fatalf("status: %v", err)
	}
	waitFor("rev/3", "status", "status.readyReplicas (none) -> 1")
	if body := view("rev/3"); !strings.Contains(body, "+  readyReplicas: 1") || strings.Contains(body, "image:") {
		t.Fatalf("status diff:\n%s", body)
	}

	// A ReplicaSet the Deployment controls is listed as retained history.
	mustCreate(t, cli, &appsv1.ReplicaSet{
		Namespace: ns, Name: "web-1", Labels: labels,
		Annotations:     map[string]string{"deployment.kubernetes.io/revision": "1"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: dep.UID, Controller: new(true)}},
		Spec:            appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: dep.Spec.Template},
	})
	waitFor("history/apps/v1/replicasets/"+ns+"/web-1", "replicasets.apps", "")
	if c, _ := cells("history/apps/v1/replicasets/" + ns + "/web-1"); !strings.HasPrefix(c[3], "revision 1, retained by the controller") {
		t.Fatalf("history row = %q", c)
	}

	// Delete and recreate: a new lifetime with a new UID.
	if err := cli.Delete(ctx, dep); err != nil {
		t.Fatalf("delete: %v", err)
	}
	waitFor("rev/4", "deleted", "deleted")
	replaced := newDeployment("busybox:3")
	mustCreate(t, cli, replaced)
	waitFor("rev/5", "recreated", "uid "+string(replaced.UID))
	// The old UID's ReplicaSet is not history of the new object.
	if _, ok := cells("history/apps/v1/replicasets/" + ns + "/web-1"); ok {
		t.Fatalf("the replaced Deployment's ReplicaSet is listed for the new one")
	}

	// Stop: later changes are not recorded, the entries stay.
	if _, r, ok := tf.Find(ctx, "stop"); !ok {
		t.Fatalf("no stop row")
	} else if _, err := r.(Enterable).Enter(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	replaced.Spec.Template.Spec.Containers[0].Image = "busybox:4"
	if err := cli.Update(ctx, replaced); err != nil {
		t.Fatalf("update: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, ok := cells("rev/6"); ok {
		t.Fatalf("a stopped recording recorded a change")
	}
	if c, _ := cells("summary/recording"); !strings.Contains(c[3], ", stopped ") {
		t.Fatalf("recording row = %q", c)
	}
	if _, ok := cells("rev/1"); !ok {
		t.Fatalf("stopping dropped the entries")
	}
}
