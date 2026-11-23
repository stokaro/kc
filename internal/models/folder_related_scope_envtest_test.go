package models

import (
	"context"
	"errors"
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

// A user who may read pods in one namespace only gets a requires folder
// that marks what it may not read, and a used-by folder that lists the pods
// and marks the controller it may not read, without waiting on informers
// that could never sync. Every namespace cache is given up on Close.
func TestRelatedFoldersNamespaceUser(t *testing.T) {
	t.Parallel()
	user, ns := kctesting.UniqueName("related-ns-user"), kctesting.UniqueName("related-limited")
	e := newNamespaceUserEnv(t, user, ns)
	admin := e.admin.GetClient()
	ctx := t.Context()
	rs := &appsv1.ReplicaSet{
		Namespace: ns, Name: "web-1",
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
			},
		},
	}
	mustCreate(t, admin, rs)
	mustCreate(t, admin, &corev1.ConfigMap{Namespace: ns, Name: "cfg"})
	pod := &corev1.Pod{
		Namespace: ns, Name: "web-1-abc",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: rs.UID, Controller: new(true)}},
		Spec: corev1.PodSpec{
			ServiceAccountName: "runner",
			NodeName:           "node-1",
			Containers:         []corev1.Container{{Name: "c", Image: "busybox", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{Name: "cfg"}}}}},
		},
	}
	mustCreate(t, admin, pod)

	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	requires, err := NewRelatedFolder(readCtx, e.deps, RelatedDependsOn, podGVR, ns, pod.Name, []string{"pods", pod.Name})
	if err != nil {
		t.Fatalf("requires folder: %v", err)
	}
	if got := requires.Path(); got[len(got)-1] != DependsOnSegment {
		t.Fatalf("requires path = %v", got)
	}
	waitRows(t, requires, 3)
	assertRows(t, "requires", requires, map[string][]string{
		"v1/serviceaccounts/" + ns + "/runner": {"runner", "serviceaccounts", ns, "serviceAccountName: forbidden"},
		"v1/configmaps/" + ns + "/cfg":         {"cfg", "configmaps", ns, "containers[c].envFrom: forbidden"},
		"v1/nodes//node-1":                     {"node-1", "nodes", "", "spec.nodeName: forbidden"},
	})

	usedBy, err := NewRelatedFolder(readCtx, e.deps, RelatedUsedBy, configMapGVR, ns, "cfg", []string{"configmaps", "cfg"})
	if err != nil {
		t.Fatalf("used-by folder: %v", err)
	}
	// The pod templates of the workload types it may not list make the
	// result incomplete: one row says so and names them.
	waitRows(t, usedBy, 3)
	assertRows(t, "used-by", usedBy, map[string][]string{
		"v1/pods/" + ns + "/web-1-abc":         {"/web-1-abc", "pods", ns, "containers[c].envFrom"},
		"apps/v1/replicasets/" + ns + "/web-1": {"web-1", "replicasets.apps", ns, "controller of pods/web-1-abc: forbidden"},
		"unresolved//workloads/" + ns + "/(not listed)": {"(not listed)", "workloads", ns,
			"pod templates of deployments.apps, statefulsets.apps, daemonsets.apps, jobs.batch, cronjobs.batch: forbidden; the result is incomplete"},
	})

	// The pods were read through a namespace cache, which the folders hold.
	if holds := e.openHolds(); len(holds) == 0 {
		t.Fatalf("no namespace cache is held; the pods did not come from one")
	}
	requires.Close()
	usedBy.Close()
	if holds := e.openHolds(); len(holds) != 0 {
		t.Fatalf("namespace caches still held after Close: %v", holds)
	}
	for _, err := range e.errors() {
		t.Errorf("reported error: %v", err)
	}
}

// The requires folder lists what a pod names, marks what is missing and
// follows a target that appears.
func TestRequiresFolderEnvtest(t *testing.T) {
	t.Parallel()
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := t.Context()
	ns := kctesting.UniqueName("related-requires")
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	mustCreate(t, cli, &corev1.ConfigMap{Namespace: ns, Name: "cfg"})
	mustCreate(t, cli, &corev1.Pod{
		Namespace: ns, Name: "web",
		Spec: corev1.PodSpec{
			ServiceAccountName: "runner",
			Containers:         []corev1.Container{{Name: "c", Image: "busybox", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{Name: "creds"}}}}},
			Volumes:            []corev1.Volume{{Name: "v", ConfigMap: &corev1.ConfigMapVolumeSource{Name: "cfg"}}},
		},
	})
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if _, err := NewRelatedFolder(readCtx, deps, RelatedDependsOn, configMapGVR, ns, "cfg", nil); !errors.Is(err, ErrNoDependsOn) {
		t.Fatalf("configmap requires: err = %v, want ErrNoDependsOn", err)
	}
	f, err := NewRelatedFolder(readCtx, deps, RelatedDependsOn, podGVR, ns, "web", []string{"pods", "web"})
	if err != nil {
		t.Fatalf("requires folder: %v", err)
	}
	defer f.Close()
	waitRows(t, f, 3)
	assertRows(t, "requires", f, map[string][]string{
		"v1/configmaps/" + ns + "/cfg":         {"/cfg", "configmaps", ns, "volumes[v]"},
		"v1/secrets/" + ns + "/creds":          {"creds", "secrets", ns, "containers[c].envFrom: not found"},
		"v1/serviceaccounts/" + ns + "/runner": {"runner", "serviceaccounts", ns, "serviceAccountName: not found"},
	})

	// The missing secret appears through the watch on secrets.
	mustCreate(t, cli, &corev1.Secret{Namespace: ns, Name: "creds"})
	id := "v1/secrets/" + ns + "/creds"
	deadline := time.Now().Add(20 * time.Second)
	for {
		f.Len(ctx)
		_, row, ok := f.Find(ctx, id)
		var cells []string
		if ok {
			_, cells, _, _ = row.Columns()
		}
		if len(cells) > 3 && cells[3] == "containers[c].envFrom" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("row %s = %q after the secret was created, want it without a problem", id, cells)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
