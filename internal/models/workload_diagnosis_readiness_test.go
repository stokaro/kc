package models

import (
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// rdPod is pod ns/web with one container "app", running and not ready
// since ten minutes, as the kubelet reports a failing readiness probe.
func rdPod(mut func(*corev1.Pod)) corev1.Pod {
	since := metav1.NewTime(diagNow.Add(-10 * time.Minute))
	p := corev1.Pod{
		APIVersion: "v1", Kind: "Pod",
		Namespace: "ns", Name: "web", UID: "pod-web",
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: "app:1",
			ReadinessProbe: &corev1.Probe{HTTPGet: &corev1.HTTPGetAction{Path: "/ready"}},
		}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.ContainersReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady", LastTransitionTime: since},
				{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady", Message: "containers with unready status: [app]", LastTransitionTime: since},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: false, Started: new(true), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	if mut != nil {
		mut(&p)
	}
	return p
}

func rdSetCondition(p *corev1.Pod, c corev1.PodCondition) {
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == c.Type {
			p.Status.Conditions[i] = c
			return
		}
	}
	p.Status.Conditions = append(p.Status.Conditions, c)
}

func rdReady(p *corev1.Pod) {
	rdSetCondition(p, corev1.PodCondition{Type: corev1.ContainersReady, Status: corev1.ConditionTrue})
	rdSetCondition(p, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
	p.Status.ContainerStatuses[0].Ready = true
}

func TestDiagnoseWorkloadReadiness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		pod    corev1.Pod
		events []corev1.Event
		codes  []string
		check  func(t *testing.T, d diagReport)
	}{{
		name:   "running pod with an unready container",
		pod:    rdPod(nil),
		events: []corev1.Event{dgEvent("web.1", "pod-web", "spec.containers{app}", corev1.EventTypeWarning, "Unhealthy", "Readiness probe failed: HTTP probe failed with statuscode: 503", diagNow.Add(-time.Minute))},
		codes:  []string{"unready/web"},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "unready/web")
			if f.Severity != diagWarning || !f.Observed.Equal(diagNow.Add(-10*time.Minute)) {
				t.Fatalf("finding = %+v", f)
			}
			if f.Title != "Pod web is not ready: container app not ready (ContainersNotReady)" || f.Detail != "containers with unready status: [app]" {
				t.Fatalf("title %q, detail %q", f.Title, f.Detail)
			}
			want := []string{
				"status.conditions[Ready]=False ContainersNotReady, uid pod-web",
				"status.conditions[ContainersReady]=False ContainersNotReady",
				"status.containerStatuses[app]: running, ready=false, has a readinessProbe",
			}
			if got := dgEvidence(f); len(got) != 4 || !slices.Equal(got[:3], want) || !strings.Contains(got[3], "Unhealthy") {
				t.Fatalf("evidence = %q, want %q and the Unhealthy event", got, want)
			}
			if strings.Contains(strings.ToLower(f.Title+f.Detail), "probe failed") {
				t.Fatalf("the finding claims a probe failure kc did not see: %+v", f)
			}
			if len(f.Logs) != 1 || f.Logs[0].Spec.Container != "app" || f.Logs[0].Spec.Previous {
				t.Fatalf("logs = %+v", f.Logs)
			}
		},
	}, {
		name: "a pending readiness gate is not a container problem",
		pod: rdPod(func(p *corev1.Pod) {
			rdReady(p)
			p.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: "target-health.elbv2.k8s.aws/web"}}
			rdSetCondition(p, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ReadinessGatesNotReady",
				Message: `corresponding condition of pod readiness gate "target-health.elbv2.k8s.aws/web" does not exist.`, LastTransitionTime: metav1.NewTime(diagNow.Add(-10 * time.Minute))})
		}),
		codes: []string{"readiness-gate/web"},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "readiness-gate/web")
			if f.Title != "Pod web is not ready: readiness gate target-health.elbv2.k8s.aws/web not set" || len(f.Logs) != 0 {
				t.Fatalf("finding = %+v", f)
			}
			if got := dgEvidence(f); !slices.Contains(got, "status.conditions[ContainersReady]=True ") {
				t.Fatalf("evidence = %q, want ContainersReady=True", got)
			}
		},
	}, {
		name: "a gate set to False shows its status",
		pod: rdPod(func(p *corev1.Pod) {
			rdReady(p)
			p.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: "example.com/lb"}}
			rdSetCondition(p, corev1.PodCondition{Type: "example.com/lb", Status: corev1.ConditionFalse})
			rdSetCondition(p, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ReadinessGatesNotReady"})
		}),
		codes: []string{"readiness-gate/web"},
		check: func(t *testing.T, d diagReport) {
			if f := findingByCode(t, d, "readiness-gate/web"); !strings.HasSuffix(f.Title, "readiness gate example.com/lb=False") {
				t.Fatalf("title = %q", f.Title)
			}
		},
	}, {
		name: "a pod that became unready within the grace is starting",
		pod: rdPod(func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].Started = new(false)
			p.Spec.Containers[0].StartupProbe = &corev1.Probe{}
			rdSetCondition(p, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady", LastTransitionTime: metav1.NewTime(diagNow.Add(-30 * time.Second))})
		}),
		codes: []string{"unready/web"},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "unready/web")
			if f.Severity != diagInfo || !strings.HasSuffix(f.Title, ", starting 30s (grace 2m)") {
				t.Fatalf("finding = %+v", f)
			}
			if got := dgEvidence(f)[2]; got != "status.containerStatuses[app]: running, ready=false, started=false, has a startupProbe" {
				t.Fatalf("container evidence = %q", got)
			}
		},
	}, {
		name:  "a ready pod has no problem",
		pod:   rdPod(rdReady),
		codes: []string{"no-problem"},
	}, {
		name: "a completed pod has no problem",
		pod: rdPod(func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodSucceeded
			rdSetCondition(p, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "PodCompleted"})
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}
		}),
		codes: []string{"no-problem"},
	}, {
		name: "a failed pod reports the exit, not readiness",
		pod: rdPod(func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodFailed
			rdSetCondition(p, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "PodFailed"})
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}
		}),
		codes: []string{"exit/web/app"},
	}, {
		name: "a crash loop is not reported twice",
		pod: rdPod(func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].RestartCount = 4
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
			p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.NewTime(diagNow.Add(-time.Minute))}
		}),
		codes: []string{"restarts/web/app"},
	}, {
		name: "a terminating pod is expected to be unready",
		pod: rdPod(func(p *corev1.Pod) {
			p.DeletionTimestamp = new(metav1.NewTime(diagNow.Add(-5 * time.Second)))
		}),
		codes: []string{"no-problem"},
	}, {
		name: "ready containers in an unready pod",
		pod: rdPod(func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].Ready = true
			rdSetCondition(p, corev1.PodCondition{Type: corev1.ContainersReady, Status: corev1.ConditionTrue})
		}),
		codes: []string{"unready/web"},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "unready/web")
			if f.Title != "Pod web is not ready: ContainersNotReady" || !strings.Contains(f.Next, "stale") || len(f.Logs) != 0 {
				t.Fatalf("finding = %+v", f)
			}
		},
	}, {
		name: "an unready sidecar counts, a finished init container does not",
		pod: rdPod(func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].Ready = true
			p.Spec.InitContainers = []corev1.Container{{Name: "setup"}, {Name: "proxy", RestartPolicy: new(corev1.ContainerRestartPolicyAlways)}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{
				{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}},
				{Name: "proxy", Started: new(true), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
		}),
		codes: []string{"unready/web"},
		check: func(t *testing.T, d diagReport) {
			f := findingByCode(t, d, "unready/web")
			if f.Title != "Pod web is not ready: container proxy not ready (ContainersNotReady)" {
				t.Fatalf("title = %q", f.Title)
			}
			if got := dgEvidence(f)[2]; got != "status.initContainerStatuses[proxy]: running, ready=false, no readinessProbe" {
				t.Fatalf("container evidence = %q", got)
			}
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := diagWorkload(t, workloadInput{
				GVR: podGVR, Object: toUnstructured(t, &tt.pod), Now: diagNow, Grace: 2 * time.Minute, Events: tt.events,
			})
			if got := dgCodes(d); !slices.Equal(got, tt.codes) {
				t.Fatalf("findings = %v, want %v", got, tt.codes)
			}
			if tt.check != nil {
				tt.check(t, d)
			}
		})
	}
}
