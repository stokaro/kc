package logmux_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/sttts/kc/internal/logmux"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
)

type recordingSetter struct {
	calls [][]logmux.Target
}

func (r *recordingSetter) SetTargets(ts []logmux.Target) {
	r.calls = append(r.calls, slices.Clone(ts))
}

func (r *recordingSetter) last(t *testing.T) []string {
	t.Helper()
	if len(r.calls) == 0 {
		t.Fatal("no target set was published")
	}
	var out []string
	for _, tg := range r.calls[len(r.calls)-1] {
		out = append(out, tg.Name())
	}
	return out
}

func running() corev1.ContainerState {
	return corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
}

func pod(name string, lbls map[string]string, restarts int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: name, UID: types.UID("uid-" + name), Labels: lbls},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "init"}},
			Containers:     []corev1.Container{{Name: "app"}, {Name: "sidecar"}},
		},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: running(), RestartCount: restarts},
				{Name: "sidecar", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
			},
		},
	}
}

func toUnstructured(t *testing.T, p *corev1.Pod) *unstructured.Unstructured {
	t.Helper()
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestPodTargets(t *testing.T) {
	t.Parallel()
	p := pod("api-1", nil, 2)
	p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}}
	p.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{Name: "debug", State: running()}}

	got := logmux.PodTargets(p)
	var names []string
	for _, tg := range got {
		names = append(names, tg.Name())
	}
	// The waiting sidecar has no log yet and is left out.
	if want := []string{"api-1/init", "api-1/app", "api-1/debug"}; !slices.Equal(names, want) {
		t.Fatalf("targets %v, want %v", names, want)
	}
	if got[1].Restarts != 2 || got[1].PodUID != "uid-api-1" || got[1].Namespace != "web" {
		t.Fatalf("app target = %+v, want restarts 2, uid-api-1, namespace web", got[1])
	}
	if logmux.PodTargets(nil) != nil {
		t.Fatal("nil pod has targets")
	}
}

func TestFollowerTracksMatchingPods(t *testing.T) {
	t.Parallel()
	sel := labels.SelectorFromSet(labels.Set{"app": "api"})
	set := &recordingSetter{}
	f := logmux.NewFollower(set, logmux.SelectorMatcher("web", sel))
	api := map[string]string{"app": "api"}

	// Pods that do not match publish nothing: another app, another
	// namespace.
	f.OnAdd(pod("db-1", map[string]string{"app": "db"}, 0), true)
	other := pod("api-x", api, 0)
	other.Namespace = "other"
	f.OnAdd(other, true)
	if len(set.calls) != 0 {
		t.Fatalf("non-matching pods published %v", set.calls)
	}

	// The cache holds unstructured pods.
	f.OnAdd(toUnstructured(t, pod("api-2", api, 0)), true)
	f.OnAdd(pod("api-1", api, 0), true)
	if got, want := set.last(t), []string{"api-1/init", "api-1/app", "api-2/init", "api-2/app"}; !slices.Equal(got, want) {
		t.Fatalf("targets %v, want %v", got, want)
	}
	if f.Pods() != 2 {
		t.Fatalf("Pods() = %d, want 2", f.Pods())
	}

	// A restart changes the target identity.
	f.OnUpdate(nil, pod("api-1", api, 1))
	last := set.calls[len(set.calls)-1]
	if last[1].Name() != "api-1/app" || last[1].Restarts != 1 {
		t.Fatalf("after restart the app target is %+v, want restarts 1", last[1])
	}

	// Relabeled away: the pod leaves the set.
	f.OnUpdate(nil, pod("api-2", map[string]string{"app": "moved"}, 0))
	if got, want := set.last(t), []string{"api-1/init", "api-1/app"}; !slices.Equal(got, want) {
		t.Fatalf("targets %v, want %v", got, want)
	}

	// A delete seen only as a tombstone still removes the pod.
	f.OnDelete(toolscache.DeletedFinalStateUnknown{Key: "web/api-1", Obj: pod("api-1", api, 1)})
	if got := set.last(t); len(got) != 0 {
		t.Fatalf("targets %v after the last pod was deleted, want none", got)
	}

	// Deleting a pod that never matched publishes nothing.
	n := len(set.calls)
	f.OnDelete(pod("db-1", map[string]string{"app": "db"}, 0))
	if len(set.calls) != n {
		t.Fatal("deleting a non-matching pod published a target set")
	}
}

func TestFollowerNameMatcher(t *testing.T) {
	t.Parallel()
	set := &recordingSetter{}
	f := logmux.NewFollower(set, logmux.NameMatcher("web", "api-1"))
	f.OnAdd(pod("api-2", nil, 0), true)
	f.OnAdd(pod("api-1", nil, 0), true)
	if got, want := set.last(t), []string{"api-1/init", "api-1/app"}; !slices.Equal(got, want) {
		t.Fatalf("targets %v, want %v", got, want)
	}
	if len(set.calls) != 1 {
		t.Fatalf("%d sets published, want 1 (api-2 does not match)", len(set.calls))
	}
}

func TestWorkloadSelector(t *testing.T) {
	t.Parallel()
	workload := func(selector any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "api", "namespace": "web"},
			"spec":     map[string]any{},
		}}
		if selector != nil {
			u.Object["spec"].(map[string]any)["selector"] = selector
		}
		return u
	}
	tests := []struct {
		name     string
		selector any
		want     string
		wantErr  error
	}{
		{name: "match labels", selector: map[string]any{"matchLabels": map[string]any{"app": "api"}}, want: "app=api"},
		{
			name: "match expressions",
			selector: map[string]any{"matchExpressions": []any{
				map[string]any{"key": "tier", "operator": "In", "values": []any{"a", "b"}},
			}},
			want: "tier in (a,b)",
		},
		{name: "missing", selector: nil, wantErr: logmux.ErrNoSelector},
		{name: "empty", selector: map[string]any{}, wantErr: logmux.ErrNoSelector},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := logmux.WorkloadSelector(workload(tc.selector))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if sel.String() != tc.want {
				t.Fatalf("selector = %q, want %q", sel.String(), tc.want)
			}
		})
	}
}
