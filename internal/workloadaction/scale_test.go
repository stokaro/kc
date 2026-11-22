package workloadaction_test

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/internal/workloadaction"
)

func scaleTargets() map[string]crclient.Object {
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
	return map[string]crclient.Object{
		"deployments": deployment(3),
		"statefulsets": &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
			Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3), Selector: sel, Template: podTemplate("web:3", nil)},
		},
		"replicasets": &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
			Spec:       appsv1.ReplicaSetSpec{Replicas: ptr.To[int32](3), Selector: sel, Template: podTemplate("web:3", nil)},
		},
	}
}

func replicasOf(t *testing.T, cl crclient.Client, obj crclient.Object) int32 {
	t.Helper()
	fresh := obj.DeepCopyObject().(crclient.Object)
	if err := cl.Get(t.Context(), crclient.ObjectKeyFromObject(obj), fresh); err != nil {
		t.Fatalf("get %T: %v", obj, err)
	}
	switch o := fresh.(type) {
	case *appsv1.Deployment:
		return *o.Spec.Replicas
	case *appsv1.StatefulSet:
		return *o.Spec.Replicas
	case *appsv1.ReplicaSet:
		return *o.Spec.Replicas
	}
	t.Fatalf("unexpected %T", fresh)
	return 0
}

func TestScaleThroughTheScaleSubresource(t *testing.T) {
	tests := []struct {
		resource string
		target   string
	}{
		{resource: "deployments", target: "deployments.apps/web"},
		{resource: "statefulsets", target: "statefulsets.apps/web"},
		{resource: "replicasets", target: "replicasets.apps/web"},
	}
	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			t.Parallel()
			obj := scaleTargets()[tt.resource]
			env, cl, rec := newEnv(t, obj)
			gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: tt.resource}

			prepared, err := workloadaction.PrepareScale(t.Context(), env, ref(gvr, "web"))
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			asker, ok := prepared.(workloadaction.Asker)
			if !ok {
				t.Fatalf("prepared %T asks for no replica count", prepared)
			}
			if q := asker.Question(); q.Initial != "3" {
				t.Errorf("dialog prefilled with %q, want the current count 3", q.Initial)
			}
			change, err := asker.Answer("5")
			if err != nil {
				t.Fatalf("answer 5: %v", err)
			}
			c := change.Confirmation()
			if got := detail(c, "Replicas"); got != "3 -> 5" {
				t.Errorf("Replicas detail = %q, want 3 -> 5", got)
			}
			if want := "kubectl scale " + tt.target + " --replicas=5 -n shop"; !strings.Contains(notes(c), want) {
				t.Errorf("notes lack %q:\n%s", want, strings.Join(c.Notes, "\n"))
			}

			for range 2 { // the patch names the count, so a repeat is a no-op
				if _, err := change.Run(t.Context()); err != nil {
					t.Fatalf("run: %v", err)
				}
				if got := replicasOf(t, cl, obj); got != 5 {
					t.Fatalf("replicas = %d, want 5", got)
				}
			}
			want := []string{"get scale", "patch scale application/merge-patch+json", "patch scale application/merge-patch+json"}
			if got := rec.list(); !slices.Equal(got, want) {
				t.Errorf("requests = %q, want %q (scale subresource only, no patch of the object)", got, want)
			}
		})
	}
}

func TestScaleAnswerRefusesBadCounts(t *testing.T) {
	env, _, _ := newEnv(t, deployment(2))
	prepared, err := workloadaction.PrepareScale(t.Context(), env, ref(workloadaction.DeploymentsGVR, "web"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	asker := prepared.(workloadaction.Asker)
	for _, bad := range []string{"", "-1", "two", "1.5", "99999999999"} {
		if _, err := asker.Answer(bad); err == nil {
			t.Errorf("Answer(%q) accepted", bad)
		}
	}
	zero, err := asker.Answer(" 0 ")
	if err != nil {
		t.Fatalf("Answer(0): %v", err)
	}
	if n := notes(zero.Confirmation()); !strings.Contains(n, "stops every Pod") {
		t.Errorf("scaling to 0 does not warn:\n%s", n)
	}
	if n := notes(prepared.Confirmation()); strings.Contains(n, "stops every Pod") {
		t.Errorf("scaling to 2 warns about 0:\n%s", n)
	}
}

func TestScaleControlledReplicaSetSaysTheOwnerScalesBack(t *testing.T) {
	owned := scaleTargets()["replicasets"].(*appsv1.ReplicaSet)
	owned.Name = "web-abc"
	owned.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "uid-web", Controller: ptr.To(true)}}
	free := scaleTargets()["replicasets"].(*appsv1.ReplicaSet)
	free.Name = "solo"
	env, _, _ := newEnv(t, owned, free)

	for name, want := range map[string]bool{"web-abc": true, "solo": false} {
		prepared, err := workloadaction.PrepareScale(t.Context(), env, ref(workloadaction.ReplicaSetsGVR, name))
		if err != nil {
			t.Fatalf("prepare %s: %v", name, err)
		}
		got := strings.Contains(notes(prepared.Confirmation()), "controlled by Deployment web")
		if got != want {
			t.Errorf("%s: owner note shown = %v, want %v:\n%s", name, got, want, notes(prepared.Confirmation()))
		}
	}
}

func TestScaleMissingObjectFails(t *testing.T) {
	env, _, _ := newEnv(t)
	if _, err := workloadaction.PrepareScale(t.Context(), env, ref(workloadaction.DeploymentsGVR, "gone")); err == nil {
		t.Fatal("prepare of a missing deployment succeeded")
	}
	if _, err := workloadaction.PrepareScale(t.Context(), env, ref(workloadaction.DaemonSetsGVR, "web")); err == nil {
		t.Fatal("prepare of a DaemonSet scale succeeded")
	}
}
