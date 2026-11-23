package models

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var diagNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func dgDeployment(uid types.UID, mut func(*appsv1.Deployment)) *appsv1.Deployment {
	d := &appsv1.Deployment{
		APIVersion: "apps/v1", Kind: "Deployment",
		Namespace: "ns", Name: "web", UID: uid,
		Spec:   appsv1.DeploymentSpec{Replicas: new(int32(1))},
		Status: appsv1.DeploymentStatus{AvailableReplicas: 1},
	}
	if mut != nil {
		mut(d)
	}
	return d
}

func dgReplicaSet(name string, uid, owner types.UID) appsv1.ReplicaSet {
	return appsv1.ReplicaSet{
		Namespace: "ns", Name: name, UID: uid,
		OwnerReferences: appsControllerRef("Deployment", "web", owner)}
}

// dgPod is a pod controlled by owner (a ReplicaSet) with one container
// "app"; mut sets its status.
func dgPod(name string, uid, owner types.UID, mut func(*corev1.Pod)) corev1.Pod {
	p := corev1.Pod{
		APIVersion: "v1", Kind: "Pod",
		Namespace: "ns", Name: name, UID: uid,
		OwnerReferences: appsControllerRef("ReplicaSet", "web-1", owner),
		Spec:            corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:1"}, {Name: "side", Image: "side:1"}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}, {Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "side", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	if mut != nil {
		mut(&p)
	}
	return p
}

func dgEvent(name string, uid types.UID, fieldPath, typ, reason, msg string, last time.Time) corev1.Event {
	return corev1.Event{
		Namespace: "ns", Name: name,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "ns", UID: uid, FieldPath: fieldPath},
		Type:           typ, Reason: reason, Message: msg, Count: 2,
		LastTimestamp: metav1.NewTime(last),
	}
}

func dgUnschedulable(p *corev1.Pod) {
	p.Status.Phase = corev1.PodPending
	p.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/3 nodes are available: 3 Insufficient cpu.", LastTransitionTime: metav1.NewTime(diagNow.Add(-5 * time.Minute)),
	}}
	p.Status.ContainerStatuses = nil
}

func dgStatus(p *corev1.Pod, name string, s corev1.ContainerStatus) {
	s.Name = name
	for i := range p.Status.ContainerStatuses {
		if p.Status.ContainerStatuses[i].Name == name {
			p.Status.ContainerStatuses[i] = s
		}
	}
	p.Status.Conditions[1].Status = corev1.ConditionFalse
}

// deploymentInput is a Deployment "web" with ReplicaSet "web-1" and pods.
func deploymentInput(t *testing.T, dep *appsv1.Deployment, pods ...corev1.Pod) workloadInput {
	return workloadInput{
		GVR: relDeploymentsGVR, Object: toUnstructured(t, dep), Now: diagNow,
		ReplicaSets: []appsv1.ReplicaSet{dgReplicaSet("web-1", "rs-1", dep.UID)},
		Pods:        pods,
	}
}

func diagWorkload(t *testing.T, in workloadInput) diagReport {
	t.Helper()
	d, err := diagnoseWorkload(in)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	return d
}

// dgCodes lists the finding codes in order.
func dgCodes(d diagReport) []string {
	out := make([]string, 0, len(d.Findings))
	for _, f := range d.Findings {
		out = append(out, f.Code)
	}
	return out
}

// dgEvidence lists the evidence of a finding's links in order.
func dgEvidence(f diagFinding) []string {
	out := make([]string, 0, len(f.Links))
	for _, l := range f.Links {
		out = append(out, l.Evidence)
	}
	return out
}

func linkNames(f diagFinding) []string {
	out := make([]string, 0, len(f.Links))
	for _, l := range f.Links {
		out = append(out, kubectlResource(l.GVR)+"/"+l.Name)
	}
	return out
}

func TestDiagnoseWorkload(t *testing.T) {
	t.Parallel()
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", errors.New("denied"))
	tests := []struct {
		name  string
		in    func(t *testing.T) workloadInput
		check func(t *testing.T, d diagReport)
	}{{
		name: "failed rollout with an unschedulable pod and its event",
		in: func(t *testing.T) workloadInput {
			dep := dgDeployment("dep-1", func(d *appsv1.Deployment) {
				d.Status.AvailableReplicas = 0
				d.Status.Conditions = []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable", LastTransitionTime: metav1.NewTime(diagNow.Add(-20 * time.Minute))},
					{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: `ReplicaSet "web-1" has timed out progressing.`, LastTransitionTime: metav1.NewTime(diagNow.Add(-10 * time.Minute))},
				}
			})
			in := deploymentInput(t, dep, dgPod("web-1-a", "pod-a", "rs-1", dgUnschedulable))
			in.Events = []corev1.Event{
				dgEvent("web-1-a.1", "pod-a", "", corev1.EventTypeWarning, "FailedScheduling", "0/3 nodes are available", diagNow.Add(-time.Minute)),
				dgEvent("other.1", "pod-other", "", corev1.EventTypeWarning, "FailedScheduling", "not ours", diagNow),
			}
			return in
		},
		check: func(t *testing.T, d diagReport) {
			rollout := findingByCode(t, d, "rollout-failed")
			if rollout.Severity != diagError || !rollout.Observed.Equal(diagNow.Add(-10*time.Minute)) || rollout.Hint == "" {
				t.Fatalf("rollout finding = %+v", rollout)
			}
			if got := dgEvidence(rollout); !slices.Contains(got, "status.conditions[Progressing]=False ProgressDeadlineExceeded") {
				t.Fatalf("rollout evidence = %v", got)
			}
			unavailable := findingByCode(t, d, "unavailable")
			if !unavailable.Observed.Equal(diagNow.Add(-20*time.Minute)) || unavailable.Title != "1 of 1 replicas are unavailable" {
				t.Fatalf("unavailable = %+v", unavailable)
			}
			f := findingByCode(t, d, "unschedulable/web-1-a")
			if f.Detail != "0/3 nodes are available: 3 Insufficient cpu." || !f.Observed.Equal(diagNow.Add(-5*time.Minute)) {
				t.Fatalf("unschedulable = %+v", f)
			}
			if got, want := linkNames(f), []string{"pods/web-1-a", "events/web-1-a.1"}; !slices.Equal(got, want) {
				t.Fatalf("links = %v, want %v (the other pod's event is not cited)", got, want)
			}
			if got := dgEvidence(f)[0]; got != "status.conditions[PodScheduled]=False Unschedulable, uid pod-a" {
				t.Fatalf("pod evidence = %q", got)
			}
			if f.Logs != nil {
				t.Fatalf("an unscheduled pod has no logs: %v", f.Logs)
			}
		},
	}, {
		name: "image pull error cites the container's events only",
		in: func(t *testing.T) workloadInput {
			pod := dgPod("web-1-a", "pod-a", "rs-1", func(p *corev1.Pod) {
				dgStatus(p, "app", corev1.ContainerStatus{Image: "app:1", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "Back-off pulling image"}}})
			})
			in := deploymentInput(t, dgDeployment("dep-1", nil), pod)
			in.Events = []corev1.Event{
				dgEvent("a.1", "pod-a", "spec.containers{app}", corev1.EventTypeWarning, "Failed", "Failed to pull image app:1", diagNow.Add(-2*time.Minute)),
				dgEvent("a.2", "pod-a", "spec.containers{side}", corev1.EventTypeWarning, "Unhealthy", "probe failed", diagNow),
				dgEvent("a.3", "pod-a", "spec.containers{app}", corev1.EventTypeNormal, "Pulling", "Pulling image", diagNow),
			}
			return in
		},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "image/web-1-a/app")
			if got, want := linkNames(f), []string{"pods/web-1-a", "events/a.1"}; !slices.Equal(got, want) {
				t.Fatalf("links = %v, want %v", got, want)
			}
			if !f.Observed.Equal(diagNow) {
				// The newest event about the container dates it, normal or not.
				t.Fatalf("observed = %v", f.Observed)
			}
			if len(f.Logs) != 0 || !strings.Contains(f.Next, "app:1") {
				t.Fatalf("finding = %+v", f)
			}
			if slices.Contains(dgCodes(d), "image/web-1-a/side") {
				t.Fatalf("the healthy container has a finding: %v", dgCodes(d))
			}
		},
	}, {
		name: "OOM kill offers the logs of that container only",
		in: func(t *testing.T) workloadInput {
			pod := dgPod("web-1-a", "pod-a", "rs-1", func(p *corev1.Pod) {
				p.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}
				dgStatus(p, "app", corev1.ContainerStatus{
					RestartCount: 3,
					State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(diagNow.Add(-30 * time.Second)),
					}},
				})
			})
			return deploymentInput(t, dgDeployment("dep-1", nil), pod)
		},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "oom/web-1-a/app")
			if !f.Observed.Equal(diagNow.Add(-30*time.Second)) || !strings.Contains(f.Next, "limits.memory 64Mi") {
				t.Fatalf("finding = %+v", f)
			}
			want := []diagLog{
				{Label: "logs", Spec: LogsSpec{Namespace: "ns", Pod: "web-1-a", Container: "app", Follow: true, TailLines: DefaultLogsTailLines}},
				{Label: "logs (previous)", Spec: LogsSpec{Namespace: "ns", Pod: "web-1-a", Container: "app", Follow: true, TailLines: DefaultLogsTailLines, Previous: true}},
			}
			if !slices.Equal(f.Logs, want) {
				t.Fatalf("logs = %+v, want %+v", f.Logs, want)
			}
			if got := dgEvidence(f)[0]; got != "status.containerStatuses[app].lastState.terminated: exit code 137, OOMKilled, uid pod-a" {
				t.Fatalf("evidence = %q", got)
			}
		},
	}, {
		name: "an old restart of a running container is history",
		in: func(t *testing.T) workloadInput {
			pod := dgPod("web-1-a", "pod-a", "rs-1", func(p *corev1.Pod) {
				p.Status.ContainerStatuses[0].RestartCount = 1
				p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: metav1.NewTime(diagNow.Add(-2 * restartWindow))}
			})
			return deploymentInput(t, dgDeployment("dep-1", nil), pod)
		},
		check: func(t *testing.T, d diagReport) {
			if got := dgCodes(d); !slices.Equal(got, []string{"no-problem"}) {
				t.Fatalf("findings = %v", got)
			}
		},
	}, {
		name: "a recent restart is a warning with both logs",
		in: func(t *testing.T) workloadInput {
			pod := dgPod("web-1-a", "pod-a", "rs-1", func(p *corev1.Pod) {
				p.Status.ContainerStatuses[1].RestartCount = 1
				p.Status.ContainerStatuses[1].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error", FinishedAt: metav1.NewTime(diagNow.Add(-time.Minute))}
			})
			return deploymentInput(t, dgDeployment("dep-1", nil), pod)
		},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "restarts/web-1-a/side")
			if f.Severity != diagWarning || len(f.Logs) != 2 || f.Logs[0].Spec.Container != "side" || f.Logs[1].Spec.Container != "side" {
				t.Fatalf("finding = %+v", f)
			}
		},
	}, {
		name: "failed init container of a failed Job",
		in: func(t *testing.T) workloadInput {
			job := &batchv1.Job{
				APIVersion: "batch/v1", Kind: "Job",
				Namespace: "ns", Name: "migrate", UID: "job-1",
				Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
					Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded",
					Message: "Job has reached the specified backoff limit", LastTransitionTime: metav1.NewTime(diagNow.Add(-time.Minute)),
				}}},
			}
			pod := corev1.Pod{
				Namespace: "ns", Name: "migrate-x", UID: "pod-j", OwnerReferences: []metav1.OwnerReference{
					{APIVersion: "batch/v1", Kind: "Job", Name: "migrate", UID: "job-1", Controller: new(true)},
				},
				Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "wait-db"}}, Containers: []corev1.Container{{Name: "run"}}},
				Status: corev1.PodStatus{
					Phase: corev1.PodFailed,
					InitContainerStatuses: []corev1.ContainerStatus{{Name: "wait-db", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 1, Reason: "Error", FinishedAt: metav1.NewTime(diagNow.Add(-2 * time.Minute)),
					}}}},
					ContainerStatuses: []corev1.ContainerStatus{{Name: "run", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}},
				},
			}
			return workloadInput{GVR: relJobsGVR, Object: toUnstructured(t, job), Now: diagNow, Pods: []corev1.Pod{pod}}
		},
		check: func(t *testing.T, d diagReport) {
			if got, want := dgCodes(d), []string{"job-failed", "exit/migrate-x/wait-db"}; !slices.Equal(got, want) {
				t.Fatalf("findings = %v, want %v", got, want)
			}
			f := findingByCode(t, d, "exit/migrate-x/wait-db")
			if !strings.HasPrefix(f.Title, "Init container wait-db of pod migrate-x exited with code 1") ||
				!strings.HasPrefix(f.Next, "Initialization blocks the main containers.") {
				t.Fatalf("finding = %+v", f)
			}
			if len(f.Logs) != 1 || f.Logs[0].Spec.Container != "wait-db" || f.Logs[0].Spec.Previous {
				t.Fatalf("logs = %+v", f.Logs)
			}
		},
	}, {
		name: "only a claim the pod references is checked",
		in: func(t *testing.T) workloadInput {
			pod := dgPod("db-0", "pod-db", "rs-1", func(p *corev1.Pod) {
				p.Spec.Volumes = []corev1.Volume{
					{Name: "cfg", ConfigMap: &corev1.ConfigMapVolumeSource{}},
					{Name: "data", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
					{Name: "logs", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "logs"}},
				}
			})
			in := deploymentInput(t, dgDeployment("dep-1", nil), pod)
			in.Claims = []corev1.PersistentVolumeClaim{
				{Namespace: "ns", Name: "data", UID: "pvc-data", Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}},
				{Namespace: "ns", Name: "logs", UID: "pvc-logs", Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}},
				{Namespace: "ns", Name: "unused", UID: "pvc-unused", Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}},
			}
			e := dgEvent("data.1", "pvc-data", "", corev1.EventTypeNormal, "WaitForFirstConsumer", "waiting for first consumer", diagNow.Add(-time.Minute))
			in.Events = []corev1.Event{e}
			return in
		},
		check: func(t *testing.T, d diagReport) {
			if got, want := dgCodes(d), []string{"claim/db-0/data"}; !slices.Equal(got, want) {
				t.Fatalf("findings = %v, want %v", got, want)
			}
			f := d.Findings[0]
			if got, want := dgEvidence(f), []string{
				"spec.volumes[1].persistentVolumeClaim names data",
				"status.phase=Pending, uid pvc-data",
				"Normal WaitForFirstConsumer: waiting for first consumer (x2, last 2026-11-23T11:59:00Z)",
			}; !slices.Equal(got, want) {
				t.Fatalf("evidence = %q, want %q", got, want)
			}
			if !f.Observed.Equal(diagNow.Add(-time.Minute)) {
				t.Fatalf("observed = %v", f.Observed)
			}
		},
	}, {
		name: "a missing claim is an error",
		in: func(t *testing.T) workloadInput {
			pod := dgPod("db-0", "pod-db", "rs-1", func(p *corev1.Pod) {
				p.Spec.Volumes = []corev1.Volume{{Name: "data", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}
			})
			return deploymentInput(t, dgDeployment("dep-1", nil), pod)
		},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "claim/db-0/data")
			if f.Severity != diagError || f.Links[1].Problem != problemNotFound {
				t.Fatalf("finding = %+v", f)
			}
		},
	}, {
		name: "denied sources are shown as unavailable, not as absent",
		in: func(t *testing.T) workloadInput {
			pod := dgPod("db-0", "pod-db", "rs-1", func(p *corev1.Pod) {
				dgUnschedulable(p)
				p.Spec.Volumes = []corev1.Volume{{Name: "data", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}
			})
			in := deploymentInput(t, dgDeployment("dep-1", nil), pod)
			in.Events, in.EventsErr = nil, forbidden
			in.Claims, in.ClaimsErr = nil, forbidden
			return in
		},
		check: func(t *testing.T, d diagReport) {
			for _, code := range []string{"events-unknown", "claim/db-0/data"} {
				if f := findingByCode(t, d, code); f.Severity != diagIncomplete {
					t.Fatalf("%s severity = %v", code, f.Severity)
				}
			}
			f := findingByCode(t, d, "unschedulable/db-0")
			if last := f.Links[len(f.Links)-1]; last.GVR != ovEventsGVR || last.Problem != problemForbidden {
				t.Fatalf("event source = %+v, want it shown as forbidden", last)
			}
		},
	}, {
		name: "denied pods leave the pod checks unknown",
		in: func(t *testing.T) workloadInput {
			in := deploymentInput(t, dgDeployment("dep-1", nil))
			in.PodsErr = forbidden
			return in
		},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "pods-unknown")
			if f.Severity != diagIncomplete || f.Links[0].Problem != problemForbidden {
				t.Fatalf("finding = %+v", f)
			}
			if slices.Contains(dgCodes(d), "no-problem") {
				t.Fatalf("an unknown result claims no problem: %v", dgCodes(d))
			}
		},
	}, {
		name: "a recovered workload has no problem",
		in: func(t *testing.T) workloadInput {
			return deploymentInput(t, dgDeployment("dep-1", func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 1 }), dgPod("web-1-a", "pod-a", "rs-1", nil))
		},
		check: func(t *testing.T, d diagReport) {
			if got := dgCodes(d); !slices.Equal(got, []string{"no-problem"}) {
				t.Fatalf("findings = %v", got)
			}
			if !slices.Contains(d.Summary, "pods: 1 owned, 1 ready") {
				t.Fatalf("summary = %v", d.Summary)
			}
		},
	}, {
		name: "a replaced Deployment does not borrow the old pods",
		in: func(t *testing.T) workloadInput {
			// The Deployment was recreated with the same name: the old
			// ReplicaSet and its failing pod still point at the old UID.
			in := deploymentInput(t, dgDeployment("dep-new", func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 1 }),
				dgPod("web-1-a", "pod-a", "rs-1", dgUnschedulable),
				dgPod("web-2-a", "pod-b", "rs-2", nil))
			in.ReplicaSets = []appsv1.ReplicaSet{dgReplicaSet("web-1", "rs-1", "dep-old"), dgReplicaSet("web-2", "rs-2", "dep-new")}
			return in
		},
		check: func(t *testing.T, d diagReport) {
			if got := dgCodes(d); !slices.Equal(got, []string{"no-problem"}) {
				t.Fatalf("findings = %v", got)
			}
			if !slices.Contains(d.Summary, "pods: 1 owned, 1 ready") || !slices.Contains(d.Summary, "object: deployments.apps/web, uid dep-new") {
				t.Fatalf("summary = %v", d.Summary)
			}
		},
	}, {
		name: "a recreated pod does not cite the old pod's events",
		in: func(t *testing.T) workloadInput {
			pod := dgPod("web-1-a", "pod-new", "rs-1", dgUnschedulable)
			return workloadInput{
				GVR: podGVR, Object: toUnstructured(t, &pod), Now: diagNow,
				Events: []corev1.Event{
					dgEvent("old.1", "pod-old", "", corev1.EventTypeWarning, "FailedScheduling", "old", diagNow),
					dgEvent("new.1", "pod-new", "", corev1.EventTypeWarning, "FailedScheduling", "new", diagNow),
				},
			}
		},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "unschedulable/web-1-a")
			if got, want := linkNames(f), []string{"pods/web-1-a", "events/new.1"}; !slices.Equal(got, want) {
				t.Fatalf("links = %v, want %v", got, want)
			}
		},
	}, {
		name: "pods beyond the bound are counted, not explained",
		in: func(t *testing.T) workloadInput {
			var pods []corev1.Pod
			for i := range maxDiagnosedPods + 2 {
				pods = append(pods, dgPod(fmt.Sprintf("web-1-%02d", i), types.UID(fmt.Sprintf("pod-%d", i)), "rs-1", dgUnschedulable))
			}
			return deploymentInput(t, dgDeployment("dep-1", nil), pods...)
		},
		check: func(t *testing.T, d diagReport) {
			n := 0
			for _, c := range dgCodes(d) {
				if strings.HasPrefix(c, "unschedulable/") {
					n++
				}
			}
			more := findingByCode(t, d, "more-pods")
			if n != maxDiagnosedPods || !strings.HasPrefix(more.Title, "2 more pods") {
				t.Fatalf("explained %d pods; more = %q", n, more.Title)
			}
		},
	}, {
		name: "a StatefulSet counts ready replicas",
		in: func(t *testing.T) workloadInput {
			sts := &appsv1.StatefulSet{
				APIVersion: "apps/v1", Kind: "StatefulSet",
				Namespace: "ns", Name: "db", UID: "sts-1",
				Spec:   appsv1.StatefulSetSpec{Replicas: new(int32(3))},
				Status: appsv1.StatefulSetStatus{ReadyReplicas: 2},
			}
			return workloadInput{GVR: relStatefulSetsGVR, Object: toUnstructured(t, sts), Now: diagNow}
		},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "unavailable")
			if f.Title != "1 of 3 replicas are unavailable" || !f.Observed.Equal(diagNow) || f.Hint != "" {
				t.Fatalf("finding = %+v", f)
			}
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.check(t, diagWorkload(t, tt.in(t)))
		})
	}
}

// The rendered rows keep the facts, the sources, the logs of the exact
// container, the possible explanation and the next step apart.
func TestRenderWorkloadFinding(t *testing.T) {
	t.Parallel()
	d := diagReport{
		Summary: []string{"object: pods/web-1-a, uid pod-a"},
		Findings: []diagFinding{{
			Code: "oom/web-1-a/app", Severity: diagError, Title: "Container app of pod web-1-a was OOMKilled",
			Observed: diagNow,
			Links:    []diagLink{{GVR: podGVR, Namespace: "ns", Name: "web-1-a", Evidence: "status.containerStatuses[app]"}},
			Logs: []diagLog{{Label: "logs (previous)", Spec: LogsSpec{
				Namespace: "ns", Pod: "web-1-a", Container: "app", Previous: true,
			}}},
			Hint: "maybe this", Next: "do that",
		}},
		Note: "note text",
	}
	rows := renderDiagnosis(Deps{}, nil, []string{"pods", "web-1-a", DiagnosisSegment}, d)
	var got [][]string
	for _, r := range rows {
		id, cells, _, _ := r.Columns()
		got = append(got, append([]string{id}, cells...))
	}
	want := [][]string{
		{"summary/object", "object", "", "", "pods/web-1-a, uid pod-a"},
		{"finding/oom/web-1-a/app", "error", "", "", "Container app of pod web-1-a was OOMKilled (observed 2026-11-23T12:00:00Z)"},
		{"finding/oom/web-1-a/app/v1/pods/ns/web-1-a", "/web-1-a", "pods", "ns", "status.containerStatuses[app]"},
		{"finding/oom/web-1-a/app/logs/app/previous", "logs (previous)", "log", "ns", "pod web-1-a, container app, previous instance"},
		{"finding/oom/web-1-a/app/maybe", "maybe", "", "", "maybe this"},
		{"finding/oom/web-1-a/app/next", "next", "", "", "do that"},
		{"note", "note", "", "", "note text"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n got %q\nwant %q", got, want)
	}
	logs, ok := rows[3].(LogsProvider)
	if !ok {
		t.Fatalf("log row = %T, want a LogsProvider", rows[3])
	}
	if got := logs.LogsSpec(); got.Pod != "web-1-a" || got.Container != "app" || !got.Previous {
		t.Fatalf("log spec = %+v", got)
	}
}
