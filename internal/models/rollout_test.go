package models

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	table "github.com/stokaro/kc/internal/table"
)

func controlledBy(kind, name string, uid types.UID) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, UID: uid, Controller: &yes}}
}

func rolloutDeployment(image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		Namespace: "shop", Name: "web", UID: "dep-uid", Generation: 3,
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(3)),
			Template: corev1.PodTemplateSpec{Labels: map[string]string{"app": "web"},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}}},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3, AvailableReplicas: 3},
	}
}

func rolloutRS(name string, uid types.UID, owner types.UID, revision, image string, replicas int32, created time.Time) appsv1.ReplicaSet {
	return appsv1.ReplicaSet{
		Namespace: "shop", Name: name, UID: uid, OwnerReferences: controlledBy("Deployment", "web", owner),
		Annotations:       map[string]string{"deployment.kubernetes.io/revision": revision},
		CreationTimestamp: metav1.NewTime(created),
		Spec: appsv1.ReplicaSetSpec{Replicas: new(replicas), Template: corev1.PodTemplateSpec{
			Labels: map[string]string{"app": "web", appsv1.DefaultDeploymentUniqueLabelKey: name},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
		}},
		Status: appsv1.ReplicaSetStatus{ReadyReplicas: replicas, AvailableReplicas: replicas},
	}
}

func rolloutPod(name string, rsName string, rsUID types.UID, ready bool, waiting string) corev1.Pod {
	cs := corev1.ContainerStatus{Name: "app", Ready: ready}
	if waiting != "" {
		cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: waiting}
		cs.RestartCount = 4
	}
	return corev1.Pod{
		Namespace: "shop", Name: name, UID: types.UID(name + "-uid"), OwnerReferences: controlledBy("ReplicaSet", rsName, rsUID),
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

func rolloutRowCells(t *testing.T, in rolloutInput) map[string][]string {
	t.Helper()
	return rolloutCells(rolloutRows(Deps{Ctx: t.Context()}, nil, []string{"deployments", "web", RolloutSegment}, in))
}

func rolloutCells(rows []table.Row) map[string][]string {
	out := make(map[string][]string, len(rows))
	for _, r := range rows {
		id, cells, _, _ := r.Columns()
		out[id] = cells
	}
	return out
}

func TestRolloutRevisions(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	dep := rolloutDeployment("web:2")
	in := rolloutInput{
		Deployment: dep,
		ReplicaSets: []appsv1.ReplicaSet{
			rolloutRS("web-1", "rs1", "dep-uid", "1", "web:1", 0, t0),
			rolloutRS("web-2", "rs2", "dep-uid", "2", "web:2", 3, t0.Add(time.Hour)),
			// A ReplicaSet of a deleted Deployment with the same name.
			rolloutRS("web-old", "rs-old", "old-dep-uid", "7", "web:0", 1, t0),
		},
		Pods: []corev1.Pod{
			rolloutPod("web-2-a", "web-2", "rs2", true, ""),
			rolloutPod("web-2-b", "web-2", "rs2", true, ""),
			// Same ReplicaSet name, another instance: not this revision's pod.
			rolloutPod("web-2-x", "web-2", "rs2-replaced", true, ""),
			rolloutPod("web-old-a", "web-old", "rs-old", true, ""),
		},
	}
	revs := rolloutRevisions(in)
	var names []string
	for _, r := range revs {
		names = append(names, r.RS.Name)
	}
	if !slices.Equal(names, []string{"web-2", "web-1"}) {
		t.Fatalf("revisions = %v, want web-2, web-1 (the other Deployment's ReplicaSet left out)", names)
	}
	if !revs[0].Current || revs[1].Current {
		t.Errorf("current = %v, %v", revs[0].Current, revs[1].Current)
	}
	if len(revs[0].Pods) != 2 || len(revs[1].Pods) != 0 {
		t.Errorf("pods = %d, %d", len(revs[0].Pods), len(revs[1].Pods))
	}
	rows := rolloutRowCells(t, in)
	for id, want := range map[string]string{
		"summary/rollout":  "complete",
		"summary/replicas": "desired 3, updated 3, ready 3, available 3",
		"revision/rs2/apps/v1/replicasets/shop/web-2": "revision 2, current: desired 3, ready 3, available 3; web:2; created 2026-11-23T11:00:00Z",
		"revision/rs1/apps/v1/replicasets/shop/web-1": "revision 1, old: desired 0",
		"revision/rs2/pod/v1/pods/shop/web-2-a":       "pod of revision 2: Running, ready 1/1, restarts 0",
		"revision/rs2/changes":                        "from revision 1: container app image web:1 -> web:2",
	} {
		if got, ok := rows[id]; !ok || !strings.Contains(got[3], want) {
			t.Errorf("row %s = %q, want %q", id, got, want)
		}
	}
	for id := range rows {
		if strings.Contains(id, "web-2-x") || strings.Contains(id, "web-old") {
			t.Errorf("row %s belongs to another instance", id)
		}
	}
	if _, ok := rows["revision/rs1/changes"]; ok {
		t.Error("the oldest revision has no previous one to compare with")
	}
}

func TestRolloutStates(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	stalled := rolloutDeployment("web:3")
	stalled.Status = appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 4, UpdatedReplicas: 1, ReadyReplicas: 3, AvailableReplicas: 3, UnavailableReplicas: 1,
		Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
			Message: `ReplicaSet "web-3" has timed out progressing.`, LastUpdateTime: metav1.NewTime(t0)}}}
	progressing := rolloutDeployment("web:3")
	progressing.Status.UpdatedReplicas = 1
	paused := rolloutDeployment("web:3")
	paused.Spec.Paused = true
	stale := rolloutDeployment("web:3")
	stale.Status.ObservedGeneration = 2
	rss := []appsv1.ReplicaSet{
		rolloutRS("web-2", "rs2", "dep-uid", "2", "web:2", 3, t0),
		rolloutRS("web-3", "rs3", "dep-uid", "3", "web:3", 1, t0.Add(time.Hour)),
	}
	pods := []corev1.Pod{rolloutPod("web-3-a", "web-3", "rs3", false, "CrashLoopBackOff"), rolloutPod("web-2-a", "web-2", "rs2", true, "")}
	forbidden := apierrors.NewForbidden(podGVR.GroupResource(), "", errors.New("denied"))
	cases := []struct {
		name    string
		in      rolloutInput
		state   string
		finding map[string]string
	}{
		{name: "stalled", in: rolloutInput{Deployment: stalled, ReplicaSets: rss, Pods: pods}, state: "stalled: ProgressDeadlineExceeded",
			finding: map[string]string{
				"finding/rollout":                           "Rollout stalled: ProgressDeadlineExceeded (observed 2026-11-23T10:00:00Z)",
				"finding/stalled-pods":                      "Pods of revision 3 (web-3) that are not ready",
				"finding/stalled-pods/v1/pods/shop/web-3-a": "CrashLoopBackOff",
			}},
		{name: "progressing", in: rolloutInput{Deployment: progressing, ReplicaSets: rss, Pods: pods}, state: "in progress: 1 of 3 replicas updated"},
		{name: "paused", in: rolloutInput{Deployment: paused}, state: "paused", finding: map[string]string{"finding/rollout": "Rollout paused"}},
		{name: "stale status", in: rolloutInput{Deployment: stale}, state: "status is for generation 2 of 3"},
		{name: "pods unreadable", in: rolloutInput{Deployment: progressing, ReplicaSets: rss, PodsErr: forbidden}, state: "in progress",
			finding: map[string]string{"finding/pods-unreadable": "could not be read: forbidden"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rows := rolloutRowCells(t, tc.in)
			if got := rows["summary/rollout"]; !strings.Contains(got[3], tc.state) {
				t.Errorf("state = %q, want %q", got, tc.state)
			}
			for id, want := range tc.finding {
				if got, ok := rows[id]; !ok || !strings.Contains(got[3], want) {
					t.Errorf("row %s = %q, want %q; rows %d", id, got, want, len(rows))
				}
			}
			if _, ok := rows["note"]; !ok {
				t.Error("no note row")
			}
		})
	}
}

func TestTemplateChanges(t *testing.T) {
	t.Parallel()
	base := corev1.PodTemplateSpec{Labels: map[string]string{"app": "web"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "web:1"}}}}
	cases := []struct {
		name   string
		change func(*corev1.PodTemplateSpec)
		want   []string
	}{
		{name: "hash label only", change: func(p *corev1.PodTemplateSpec) { p.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = "abc" }},
		{name: "image", change: func(p *corev1.PodTemplateSpec) { p.Spec.Containers[0].Image = "web:2" }, want: []string{"container app image web:1 -> web:2"}},
		{name: "env", change: func(p *corev1.PodTemplateSpec) { p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "A", Value: "1"}} }, want: []string{"containers changed"}},
		{name: "sidecar added", change: func(p *corev1.PodTemplateSpec) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "proxy", Image: "envoy:1"})
		}, want: []string{"container proxy added (envoy:1)"}},
		{name: "restart annotation", change: func(p *corev1.PodTemplateSpec) {
			p.Annotations = map[string]string{"kubectl.kubernetes.io/restartedAt": "2026-11-23T10:00:00Z"}
		}, want: []string{"annotations changed"}},
		{name: "node selector", change: func(p *corev1.PodTemplateSpec) { p.Spec.NodeSelector = map[string]string{"zone": "a"} }, want: []string{"other pod spec fields changed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			newer := base.DeepCopy()
			tc.change(newer)
			if got := templateChanges(&base, newer); !slices.Equal(got, tc.want) {
				t.Errorf("changes = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTemplateDiffRow(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	in := rolloutInput{Deployment: rolloutDeployment("web:2"), ReplicaSets: []appsv1.ReplicaSet{
		rolloutRS("web-1", "rs1", "dep-uid", "1", "web:1", 0, t0),
		rolloutRS("web-2", "rs2", "dep-uid", "2", "web:2", 3, t0.Add(time.Hour)),
	}}
	for _, r := range rolloutRows(Deps{Ctx: t.Context()}, nil, nil, in) {
		row, ok := r.(*templateDiffRow)
		if !ok {
			continue
		}
		title, body, lang, _, _, err := row.ViewContent()
		if err != nil {
			t.Fatal(err)
		}
		if title != "shop: revision 1 -> 2" || lang != "diff" {
			t.Errorf("title %q lang %q", title, lang)
		}
		if !strings.Contains(body, "-  - image: web:1") || !strings.Contains(body, "+  - image: web:2") || strings.Contains(body, "pod-template-hash") {
			t.Errorf("diff:\n%s", body)
		}
		return
	}
	t.Fatal("no template diff row")
}
