package models

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// oomPod is a pod whose container "app" was OOMKilled ago before now and
// is in state now; limit sets limits.memory when not empty.
func oomPod(ago time.Duration, state corev1.ContainerState, limit string) corev1.Pod {
	return dgPod("web-1-a", "pod-a", "rs-1", func(p *corev1.Pod) {
		if limit != "" {
			p.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}
		}
		s := &p.Status.ContainerStatuses[0]
		s.RestartCount = 1
		s.State = state
		s.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(diagNow.Add(-ago)),
		}
	})
}

var (
	oomRunning = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	oomLooping = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
)

func TestDiagnoseWorkloadOOMAge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		pod       corev1.Pod
		severity  diagSeverity
		title     string
		next      string // a substring of Next
		notNext   string // must not be in Next
		hintOwner bool   // the Deployment's warning gets the "pod problems below" hint
	}{{
		name:     "an OOM a week ago of a container that runs now is history",
		pod:      oomPod(7*24*time.Hour, oomRunning, ""),
		severity: diagInfo,
		title:    "Container app of pod web-1-a was OOMKilled 7d ago and has run since (1 restarts) (OOMKilled)",
		next:     "History, not a current failure",
		notNext:  "more memory than its limit",
	}, {
		name:      "a recent recovered OOM is a warning",
		pod:       oomPod(10*time.Minute, oomRunning, "64Mi"),
		severity:  diagWarning,
		title:     "Container app of pod web-1-a was OOMKilled 10m ago and runs again (1 restarts) (OOMKilled)",
		next:      "limits.memory 64Mi",
		hintOwner: true,
	}, {
		name:      "an OOM crash loop is an error, however old the last kill",
		pod:       oomPod(3*time.Hour, oomLooping, "64Mi"),
		severity:  diagError,
		title:     "Container app of pod web-1-a was OOMKilled (1 restarts) (OOMKilled)",
		next:      "limits.memory 64Mi",
		hintOwner: true,
	}, {
		name: "a container terminated by an OOM now is an error",
		pod: dgPod("web-1-a", "pod-a", "rs-1", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(diagNow.Add(-5 * 24 * time.Hour)),
			}}
		}),
		severity:  diagError,
		title:     "Container app of pod web-1-a was OOMKilled (0 restarts) (OOMKilled)",
		next:      "no limits.memory",
		notNext:   "reached its memory limit",
		hintOwner: true,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dep := dgDeployment("dep-1", func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 0 })
			d := diagWorkload(t, deploymentInput(t, dep, tt.pod))
			f := findingByCode(t, d, "oom/web-1-a/app")
			if f.Severity != tt.severity || f.Title != tt.title {
				t.Fatalf("finding = %s %q, want %s %q", f.Severity, f.Title, tt.severity, tt.title)
			}
			if !strings.Contains(f.Next, tt.next) || (tt.notNext != "" && strings.Contains(f.Next, tt.notNext)) {
				t.Fatalf("next = %q, want %q and not %q", f.Next, tt.next, tt.notNext)
			}
			// The kill keeps its own time and evidence, whatever its age.
			if t0 := tt.pod.Status.ContainerStatuses[0]; t0.State.Terminated == nil && !f.Observed.Equal(t0.LastTerminationState.Terminated.FinishedAt.Time) {
				t.Fatalf("observed = %v, want the finish of the kill", f.Observed)
			}
			if got := dgEvidence(f)[0]; !strings.Contains(got, "exit code 137, OOMKilled") {
				t.Fatalf("evidence = %q", got)
			}
			if !slices.ContainsFunc(f.Logs, func(l diagLog) bool { return l.Spec.Previous }) && tt.pod.Status.ContainerStatuses[0].RestartCount > 0 {
				t.Fatalf("logs = %+v, want the previous log", f.Logs)
			}
			if hinted := findingByCode(t, d, "unavailable").Hint != ""; hinted != tt.hintOwner {
				t.Fatalf("owner hint = %v, want %v", hinted, tt.hintOwner)
			}
		})
	}
}

// An old OOM kill is history; it does not hide that the container is not
// ready now.
func TestDiagnoseOldOOMDoesNotHideReadiness(t *testing.T) {
	t.Parallel()
	pod := oomPod(7*24*time.Hour, oomRunning, "")
	pod.Status.ContainerStatuses[0].Ready = false
	pod.Status.Conditions[1] = corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"}
	d := diagWorkload(t, deploymentInput(t, dgDeployment("dep-1", nil), pod))
	if got, want := dgCodes(d), []string{"unready/web-1-a", "oom/web-1-a/app"}; !slices.Equal(got, want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
}
