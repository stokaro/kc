package logmux_test

import (
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"

	"github.com/stokaro/kc/internal/logmux"
)

type recordingSetter struct {
	calls [][]logmux.Candidate
}

func (r *recordingSetter) SetCandidates(cs []logmux.Candidate) {
	r.calls = append(r.calls, slices.Clone(cs))
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
		Namespace: "web", Name: name, UID: types.UID("uid-" + name), Labels: lbls,
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

// crashLooping is the status of a container that crashed and waits in
// back-off: its last instance terminated, the next has not started.
func crashLooping(name string, restarts int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:                 name,
		RestartCount:         restarts,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, ContainerID: "containerd://abc"}},
	}
}

func TestPodCandidates(t *testing.T) {
	t.Parallel()
	p := pod("api-1", nil, 2)
	p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "report"}, corev1.Container{Name: "lost"}, corev1.Container{Name: "odd"})
	lost := crashLooping("lost", 3)
	lost.LastTerminationState.Terminated.ContainerID = "" // ContainerStatusUnknown after a node restart
	p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, crashLooping("report", 5), lost, corev1.ContainerStatus{Name: "odd"})
	p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{Name: "debug"}, {Name: "late"}}
	p.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{Name: "debug", State: running()}}

	got := logmux.PodCandidates(p)
	want := []struct {
		name     string
		waiting  string
		previous bool
		restarts int32
	}{
		{name: "api-1/init"},
		{name: "api-1/app", restarts: 2},
		{name: "api-1/sidecar", waiting: "ContainerCreating"},
		{name: "api-1/report", waiting: "CrashLoopBackOff", previous: true, restarts: 5},
		{name: "api-1/lost", waiting: "CrashLoopBackOff", restarts: 3},
		{name: "api-1/odd", waiting: "Waiting"},
		{name: "api-1/debug"},
		{name: "api-1/late", waiting: "Pending"},
	}
	if len(got) != len(want) {
		t.Fatalf("candidates %+v, want %d", got, len(want))
	}
	for i, w := range want {
		c := got[i]
		if c.Name() != w.name || c.Waiting != w.waiting || c.Previous != w.previous || c.Restarts != w.restarts {
			t.Errorf("candidate %d = %s waiting %q previous %v restarts %d, want %+v", i, c.Name(), c.Waiting, c.Previous, c.Restarts, w)
		}
		if c.PodUID != "uid-api-1" || c.Namespace != "web" {
			t.Errorf("candidate %d = %+v, want uid-api-1 in web", i, c.Target)
		}
	}

	// A pod the scheduler refused has no container statuses yet.
	pending := &corev1.Pod{
		Namespace: "web", Name: "api-2",
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable},
		}},
	}
	if got := logmux.PodCandidates(pending); len(got) != 1 || got[0].Waiting != "Unschedulable" || got[0].Previous || got[0].Done != "" {
		t.Fatalf("unschedulable pod candidates %+v", got)
	}

	// In a pod that is over, a container without a log never gets one: the
	// kubelet rejected the pod (no statuses), or it failed before its init
	// containers finished (PodInitializing). A container whose last
	// instance has a log still has it.
	over := &corev1.Pod{
		Namespace: "web", Name: "job-1",
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "init"}},
			Containers:     []corev1.Container{{Name: "app"}, {Name: "crashed"}, {Name: "unlisted"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed, Reason: "DeadlineExceeded",
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}},
			ContainerStatuses: []corev1.ContainerStatus{
				waitingFor("app", "PodInitializing"),
				crashLooping("crashed", 1),
			},
		},
	}
	got = logmux.PodCandidates(over)
	if len(got) != 4 || got[0].Done != "" || got[0].Waiting != "" ||
		got[1].Done != "pod Failed (DeadlineExceeded)" || got[1].Waiting != "" ||
		got[2].Done != "" || got[2].Waiting != "CrashLoopBackOff" || !got[2].Previous ||
		got[3].Done != "pod Failed (DeadlineExceeded)" || got[3].Waiting != "" {
		t.Fatalf("candidates of a failed pod %+v", got)
	}
	if logmux.PodCandidates(nil) != nil {
		t.Fatal("nil pod has candidates")
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
	if got, want := set.last(t), []string{"api-1/init", "api-1/app", "api-1/sidecar", "api-2/init", "api-2/app", "api-2/sidecar"}; !slices.Equal(got, want) {
		t.Fatalf("targets %v, want %v", got, want)
	}
	if sidecar := set.calls[len(set.calls)-1][2]; sidecar.Waiting != "ContainerCreating" || sidecar.Previous {
		t.Fatalf("the waiting sidecar is published as %+v", sidecar)
	}

	// A restart changes the target identity.
	f.OnUpdate(nil, pod("api-1", api, 1))
	last := set.calls[len(set.calls)-1]
	if last[1].Name() != "api-1/app" || last[1].Restarts != 1 || last[1].Waiting != "" {
		t.Fatalf("after restart the app target is %+v, want restarts 1, running", last[1])
	}

	// A crash keeps the identity and publishes the back-off.
	crashed := pod("api-1", api, 1)
	crashed.Status.ContainerStatuses[0] = crashLooping("app", 1)
	f.OnUpdate(nil, crashed)
	last = set.calls[len(set.calls)-1]
	if last[1].Target != (logmux.Target{Namespace: "web", Pod: "api-1", Container: "app", PodUID: "uid-api-1", Restarts: 1}) ||
		last[1].Waiting != "CrashLoopBackOff" || !last[1].Previous {
		t.Fatalf("after the crash the app target is %+v, want restarts 1 in back-off with a previous instance", last[1])
	}

	// Relabeled away: the pod leaves the set.
	f.OnUpdate(nil, pod("api-2", map[string]string{"app": "moved"}, 0))
	if got, want := set.last(t), []string{"api-1/init", "api-1/app", "api-1/sidecar"}; !slices.Equal(got, want) {
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
	if got, want := set.last(t), []string{"api-1/init", "api-1/app", "api-1/sidecar"}; !slices.Equal(got, want) {
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
			"spec":     make(map[string]any),
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
		{name: "empty", selector: make(map[string]any), wantErr: logmux.ErrNoSelector},
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
