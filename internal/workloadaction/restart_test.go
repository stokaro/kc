package workloadaction_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/internal/workloadaction"
)

var restartClock = time.Date(2026, 11, 22, 10, 30, 0, 0, time.UTC)

func fixedNow() time.Time { return restartClock }

func templateOf(t *testing.T, cl crclient.Client, obj crclient.Object) (template map[string]string, image string) {
	t.Helper()
	fresh := obj.DeepCopyObject().(crclient.Object)
	if err := cl.Get(t.Context(), crclient.ObjectKeyFromObject(obj), fresh); err != nil {
		t.Fatalf("get: %v", err)
	}
	switch o := fresh.(type) {
	case *appsv1.Deployment:
		return o.Spec.Template.Annotations, o.Spec.Template.Spec.Containers[0].Image
	case *appsv1.StatefulSet:
		return o.Spec.Template.Annotations, o.Spec.Template.Spec.Containers[0].Image
	case *appsv1.DaemonSet:
		return o.Spec.Template.Annotations, o.Spec.Template.Spec.Containers[0].Image
	}
	t.Fatalf("unexpected %T", fresh)
	return nil, ""
}

func restartTargets() map[string]crclient.Object {
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
	tmpl := podTemplate("web:3", map[string]string{"team": "shop"})
	d := deployment(2)
	d.Spec.Template = tmpl
	return map[string]crclient.Object{
		"deployments": d,
		"statefulsets": &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
			Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1), Selector: sel, Template: tmpl},
		},
		"daemonsets": &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
			Spec:       appsv1.DaemonSetSpec{Selector: sel, Template: tmpl},
		},
	}
}

func TestRestartSetsTheAnnotationOnce(t *testing.T) {
	for _, resource := range []string{"deployments", "statefulsets", "daemonsets"} {
		t.Run(resource, func(t *testing.T) {
			t.Parallel()
			obj := restartTargets()[resource]
			env, cl, rec := newEnv(t, obj)
			gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: resource}
			action := workloadaction.NewRestartAction(fixedNow)

			change, err := action.Prepare(t.Context(), env, ref(gvr, "web"))
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			c := change.Confirmation()
			if got := detail(c, "Restarted at"); got != "2026-11-22T10:30:00Z" {
				t.Errorf("dialog time = %q, want the RFC 3339 time kubectl writes", got)
			}
			if want := "kubectl rollout restart " + resource + ".apps/web -n shop"; !strings.Contains(notes(c), want) {
				t.Errorf("notes lack %q:\n%s", want, strings.Join(c.Notes, "\n"))
			}

			if _, err := change.Run(t.Context()); err != nil {
				t.Fatalf("run: %v", err)
			}
			first, image := templateOf(t, cl, obj)
			want := map[string]string{"team": "shop", workloadaction.RestartedAtAnnotation: "2026-11-22T10:30:00Z"}
			if !apiequality.Semantic.DeepEqual(first, want) || image != "web:3" {
				t.Fatalf("template annotations = %v image %q, want %v and the image kept", first, image, want)
			}

			// A repeat writes the confirmed value again: the template does
			// not change, so no second rollout starts.
			if _, err := change.Run(t.Context()); err != nil {
				t.Fatalf("second run: %v", err)
			}
			second, _ := templateOf(t, cl, obj)
			if !apiequality.Semantic.DeepEqual(first, second) {
				t.Errorf("second run changed the template: %v -> %v", first, second)
			}
			if got, want := rec.list(), []string{"patch application/merge-patch+json", "patch application/merge-patch+json"}; !slices.Equal(got, want) {
				t.Errorf("requests = %q, want %q", got, want)
			}
		})
	}
}

func TestRestartRefusesAPausedDeployment(t *testing.T) {
	d := deployment(1)
	d.Spec.Paused = true
	env, _, rec := newEnv(t, d)
	_, err := workloadaction.NewRestartAction(fixedNow).Prepare(t.Context(), env, ref(workloadaction.DeploymentsGVR, "web"))
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("prepare = %v, want the paused refusal", err)
	}
	if got := rec.list(); len(got) != 0 {
		t.Errorf("a refused restart sent %q", got)
	}
}

func TestRestartWarnsAboutOnDelete(t *testing.T) {
	onDelete := restartTargets()["statefulsets"].(*appsv1.StatefulSet)
	onDelete.Name = "db"
	onDelete.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
	rolling := restartTargets()["statefulsets"].(*appsv1.StatefulSet)
	env, _, _ := newEnv(t, onDelete, rolling)
	for name, want := range map[string]bool{"db": true, "web": false} {
		change, err := workloadaction.NewRestartAction(fixedNow).Prepare(t.Context(), env, ref(workloadaction.StatefulSetsGVR, name))
		if err != nil {
			t.Fatalf("prepare %s: %v", name, err)
		}
		if got := strings.Contains(notes(change.Confirmation()), "OnDelete"); got != want {
			t.Errorf("%s: OnDelete note = %v, want %v", name, got, want)
		}
	}
}
