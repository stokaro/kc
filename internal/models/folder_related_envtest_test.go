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
	"github.com/stokaro/kc/pkg/appconfig"
)

// The owners and used-by folders read the shared cache of a real API
// server, and the used-by folder follows new users through its watch.
func TestRelatedFoldersEnvtest(t *testing.T) {
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
	const ns = "related"
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	mustCreate(t, cli, &corev1.ConfigMap{Namespace: ns, Name: "cfg"})
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
	mustCreate(t, cli, rs)
	yes := true
	usingPod := func(name string) *corev1.Pod {
		return &corev1.Pod{
			Namespace: ns, Name: name, Labels: map[string]string{"app": "web"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
				Volumes:    []corev1.Volume{{Name: "v", ConfigMap: &corev1.ConfigMapVolumeSource{Name: "cfg"}}},
			},
		}
	}
	owned := usingPod("web-1-abc")
	owned.OwnerReferences = []metav1.OwnerReference{
		{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: rs.UID, Controller: &yes},
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "gone", UID: "no-such-uid"},
	}
	mustCreate(t, cli, owned)
	mustCreate(t, cli, &corev1.Pod{Namespace: ns, Name: "unrelated", Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	go func() { _ = cl.Start(ctx) }()
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}
	objPath := []string{"pods", "web-1-abc"}

	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	owners, err := NewRelatedFolder(readCtx, deps, RelatedOwners, podGVR, ns, "web-1-abc", objPath)
	if err != nil {
		t.Fatalf("owners folder: %v", err)
	}
	defer owners.Close()
	if got := owners.Path(); len(got) != 3 || got[2] != OwnersSegment {
		t.Fatalf("owners path = %v", got)
	}
	waitRows(t, owners, 2)
	assertRows(t, "owners", owners, map[string][]string{
		"apps/v1/replicasets/related/web-1": {"/web-1", "replicasets.apps", ns, "controller"},
		"apps/v1/deployments/related/gone":  {"gone", "deployments.apps", ns, "owner: not found"},
	})
	// The controller is the first row after "..".
	if id, _, _, _ := owners.Lines(ctx, 0, 2)[1].Columns(); id != "apps/v1/replicasets/related/web-1" {
		t.Fatalf("first owner = %q, want the controller", id)
	}

	if _, err := NewRelatedFolder(readCtx, deps, RelatedOwners, configMapGVR, ns, "cfg", nil); !errors.Is(err, ErrNoOwners) {
		t.Fatalf("configmap owners: err = %v, want ErrNoOwners", err)
	}
	if _, err := NewRelatedFolder(readCtx, deps, RelatedUsedBy, deploymentGVR, ns, "x", nil); !errors.Is(err, ErrNoUsedBy) {
		t.Fatalf("deployment used-by: err = %v, want ErrNoUsedBy", err)
	}

	usedBy, err := NewRelatedFolder(readCtx, deps, RelatedUsedBy, configMapGVR, ns, "cfg", []string{"configmaps", "cfg"})
	if err != nil {
		t.Fatalf("used-by folder: %v", err)
	}
	defer usedBy.Close()
	// The pod and the ReplicaSet that runs it; the non-controller owner
	// is not followed.
	waitRows(t, usedBy, 2)
	assertRows(t, "used-by", usedBy, map[string][]string{
		"v1/pods/related/web-1-abc":         {"/web-1-abc", "pods", ns, "volumes[v]"},
		"apps/v1/replicasets/related/web-1": {"/web-1", "replicasets.apps", ns, "controller of pods/web-1-abc"},
	})
	if _, _, ok := usedBy.Find(ctx, "v1/pods/related/unrelated"); ok {
		t.Fatalf("used-by lists a pod that does not use the configmap")
	}

	// A new user appears through the watch on pods.
	mustCreate(t, cli, usingPod("late"))
	waitRows(t, usedBy, 3)
	assertRows(t, "used-by after create", usedBy, map[string][]string{
		"v1/pods/related/late": {"/late", "pods", ns, "volumes[v]"},
	})
}

// waitRows waits until the folder lists n rows besides "..".
func waitRows(t *testing.T, f interface{ Len(context.Context) int }, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var got int
	for {
		if got = f.Len(t.Context()) - 1; got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("folder lists %d rows besides .., want %d", got, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
