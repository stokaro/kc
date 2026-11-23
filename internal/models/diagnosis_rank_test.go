package models

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// dgWaiting makes the app container wait with reason and message.
func dgWaiting(reason, msg string) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		dgStatus(p, "app", corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg}}})
	}
}

// dgNotReady makes the pod run without being ready: a warning.
func dgNotReady(p *corev1.Pod) {
	dgStatus(p, "app", corev1.ContainerStatus{Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})
}

// dgRestarted makes the app container run after a recent exit with code:
// a warning whose title names the code.
func dgRestarted(code int32) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		dgStatus(p, "app", corev1.ContainerStatus{Ready: true, RestartCount: 1,
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: "Error", FinishedAt: metav1.NewTime(diagNow.Add(-time.Minute))}}})
		p.Status.Conditions[1].Status = corev1.ConditionTrue
	}
}

// dgExited makes the app container exit with code: an error whose title
// names the code.
func dgExited(code int32) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		dgStatus(p, "app", corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: "Error", FinishedAt: metav1.NewTime(diagNow.Add(-time.Minute))}}})
	}
}

// podLinks counts the distinct pods a finding cites.
func podLinks(f diagFinding) int {
	seen := make(map[string]bool)
	for _, l := range f.Links {
		if l.GVR == podGVR {
			seen[l.Name] = true
		}
	}
	return len(seen)
}

func rankPods(n int, prefix string, mut func(*corev1.Pod)) []corev1.Pod {
	pods := make([]corev1.Pod, 0, n)
	for i := range n {
		name := fmt.Sprintf("%s-%02d", prefix, i)
		pods = append(pods, dgPod(name, types.UID(name), "rs-1", mut))
	}
	return pods
}

func severityOf(t *testing.T, d diagReport, code string) diagSeverity {
	t.Helper()
	return findingByCode(t, d, code).Severity
}

func TestPodProblemsRanking(t *testing.T) {
	t.Parallel()

	t.Run("a severe finding of a late pod is not crowded out", func(t *testing.T) {
		t.Parallel()
		// 12 distinct warnings (restarts with different exit codes) on
		// pods that sort before zz-last by name.
		var pods []corev1.Pod
		for i := range maxSymptomGroups + 2 {
			name := fmt.Sprintf("aa-%02d", i)
			pods = append(pods, dgPod(name, types.UID(name), "rs-1", dgRestarted(int32(i+1))))
		}
		pods = append(pods, dgPod("zz-last", "zz-last", "rs-1", dgUnschedulable))
		d, err := diagnoseWorkload(deploymentInput(t, dgDeployment("dep-1", nil), pods...))
		if err != nil {
			t.Fatal(err)
		}
		if severityOf(t, d, "unschedulable/zz-last") != diagError {
			t.Fatal("the unschedulable pod's finding is not an error")
		}
		more := findingByCode(t, d, "more-symptoms")
		if !strings.HasPrefix(more.Title, "3 more symptoms") {
			t.Errorf("more = %q, want the three lowest-ranked symptoms hidden", more.Title)
		}
		if len(more.Links) != 3 {
			t.Errorf("hidden symptoms link %d pods, want one per hidden symptom", len(more.Links))
		}
	})

	t.Run("repeated warnings group with a count and every pod", func(t *testing.T) {
		t.Parallel()
		pods := append(rankPods(15, "web", dgNotReady), rankPods(1, "db", dgWaiting("ImagePullBackOff", "pull access denied"))...)
		d, err := diagnoseWorkload(deploymentInput(t, dgDeployment("dep-1", nil), pods...))
		if err != nil {
			t.Fatal(err)
		}
		codes := dgCodes(d)
		var group diagFinding
		for _, f := range d.Findings {
			if strings.HasPrefix(f.Code, "group/") {
				group = f
			}
		}
		if group.Code == "" || !strings.HasSuffix(group.Title, "(15 pods)") {
			t.Fatalf("no group of 15 pods in %v", codes)
		}
		if cited := podLinks(group); cited != 15 || !strings.Contains(group.Detail, "evidence is from pod web-00") {
			t.Errorf("group cites %d pods, detail %q", podLinks(group), group.Detail)
		}
		// The error of the single db pod ranks first among pod problems.
		ie, ig := slices.IndexFunc(d.Findings, func(f diagFinding) bool {
			return strings.HasPrefix(f.Code, "image/db-00") || strings.Contains(f.Title, "ImagePullBackOff")
		}),
			slices.IndexFunc(d.Findings, func(f diagFinding) bool { return f.Code == group.Code })
		if ie < 0 || ie > ig {
			t.Errorf("the image pull error (%d) does not come before the warning group (%d): %v", ie, ig, codes)
		}
	})

	t.Run("different reasons are not merged", func(t *testing.T) {
		t.Parallel()
		pods := []corev1.Pod{
			dgPod("web-a", "web-a", "rs-1", dgWaiting("ImagePullBackOff", "")),
			dgPod("web-b", "web-b", "rs-1", dgWaiting("ErrImagePull", "")),
		}
		d, err := diagnoseWorkload(deploymentInput(t, dgDeployment("dep-1", nil), pods...))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range dgCodes(d) {
			if strings.HasPrefix(c, "group/") {
				t.Fatalf("pods with different reasons were grouped: %v", dgCodes(d))
			}
		}
	})

	t.Run("equal titles with different messages say so", func(t *testing.T) {
		t.Parallel()
		pods := []corev1.Pod{
			dgPod("web-a", "web-a", "rs-1", dgWaiting("ImagePullBackOff", "registry timeout")),
			dgPod("web-b", "web-b", "rs-1", dgWaiting("ImagePullBackOff", "manifest unknown")),
		}
		d, err := diagnoseWorkload(deploymentInput(t, dgDeployment("dep-1", nil), pods...))
		if err != nil {
			t.Fatal(err)
		}
		var group diagFinding
		for _, f := range d.Findings {
			if strings.HasPrefix(f.Code, "group/") {
				group = f
			}
		}
		if !strings.Contains(group.Detail, "registry timeout") || !strings.Contains(group.Detail, "Their messages differ") {
			t.Errorf("group detail = %q, want the first pod's message and a note that they differ", group.Detail)
		}
	})

	t.Run("identical reads give identical order", func(t *testing.T) {
		t.Parallel()
		pods := append(rankPods(3, "b", dgNotReady), rankPods(3, "a", dgWaiting("CrashLoopBackOff", ""))...)
		pods = append(pods, rankPods(3, "c", dgNotReady)...)
		var first []string
		for i := range 5 {
			// Pods arrive in a different order each time; the cache order
			// is not part of the result.
			shuffled := slices.Clone(pods)
			slices.Reverse(shuffled[:i+1])
			d, err := diagnoseWorkload(deploymentInput(t, dgDeployment("dep-1", nil), shuffled...))
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				first = dgCodes(d)
				continue
			}
			if got := dgCodes(d); !slices.Equal(got, first) {
				t.Fatalf("read %d: %v, want %v", i, got, first)
			}
		}
	})

	t.Run("source-unavailable findings stay visible", func(t *testing.T) {
		t.Parallel()
		in := deploymentInput(t, dgDeployment("dep-1", nil), rankPods(maxSymptomGroups+5, "web", dgWaiting("CrashLoopBackOff", ""))...)
		in.EventsErr = fmt.Errorf("forbidden")
		in.Events = nil
		d, err := diagnoseWorkload(in)
		if err != nil {
			t.Fatal(err)
		}
		if severityOf(t, d, "events-unknown") != diagIncomplete {
			t.Error("events-unknown is not incomplete")
		}
	})
}

func TestPodProblemsLargeWorkload(t *testing.T) {
	t.Parallel()
	// 900 pods, 30 distinct exit codes.
	var pods []corev1.Pod
	for i := range 900 {
		name := fmt.Sprintf("ds-%03d", i)
		pods = append(pods, dgPod(name, types.UID(name), "rs-1", dgExited(int32(i%30+1))))
	}
	d, err := diagnoseWorkload(deploymentInput(t, dgDeployment("dep-1", nil), pods...))
	if err != nil {
		t.Fatal(err)
	}
	groups := 0
	for _, f := range d.Findings {
		if strings.HasPrefix(f.Code, "group/") {
			groups++
			if n := podLinks(f); n != maxGroupPods {
				t.Errorf("group %s cites %d pods, want %d", f.Code, n, maxGroupPods)
			}
			if !strings.Contains(f.Detail, "more pods are not cited") {
				t.Errorf("group %s does not say pods are left out: %q", f.Code, f.Detail)
			}
		}
	}
	if groups != maxSymptomGroups {
		t.Errorf("expanded %d groups, want %d", groups, maxSymptomGroups)
	}
	more := findingByCode(t, d, "more-symptoms")
	if !strings.HasPrefix(more.Title, "20 more symptoms") || len(more.Links) != 20 {
		t.Errorf("more = %q with %d links", more.Title, len(more.Links))
	}
}
