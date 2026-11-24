package models

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func rowSummary(r UsageRow) string {
	kind := "pod"
	if r.Workload != nil {
		kind = r.Workload.GVR.Resource
	}
	return fmt.Sprintf("%s %s %d/%d %q", kind, r.Name, r.Ready, r.Pods, r.Problem)
}

func TestUsageAmountsRows(t *testing.T) {
	t.Parallel()
	const mi = 1 << 20
	report := measuredReport(t, "a", workloadMeasured(), amountObjects()...)
	tests := []struct {
		name  string
		focus UsageFocus
		want  []string
	}{
		{
			name: "the scope groups pods by their top workload, most memory first; pods without one stay pods",
			want: []string{
				`deployments web 2/3 "not ready"`,
				`statefulsets db 1/1 ""`,
				`cronjobs nightly 1/1 ""`,
				`pod orphan 1/1 ""`,
				`pod rollout 1/1 ""`,
				`pod stale 1/1 ""`,
			},
		},
		{
			name:  "a workload lists its pods",
			focus: UsageFocus{Workload: &wlDeployment},
			want:  []string{`pod web-1 1/1 ""`, `pod web-2 0/1 "not ready"`, `pod web-3 1/1 "Pending"`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := usageAmounts(report, nil, tc.focus, 3)
			var rows []string
			for _, r := range got.Rows {
				rows = append(rows, rowSummary(r))
			}
			if !slices.Equal(rows, tc.want) {
				t.Errorf("rows =\n%q\nwant\n%q", rows, tc.want)
			}
		})
	}
	t.Run("a workload row sums its pods and names its top controller", func(t *testing.T) {
		t.Parallel()
		r := usageAmounts(report, nil, UsageFocus{}, 3).Rows[0]
		if *r.Workload != wlDeployment || r.Measured != 2 || r.CPU.Used != 0.2 || r.Memory.Used != 140*mi || r.Memory.Requests != 192*mi {
			t.Errorf("web row = %+v", r)
		}
	})
	t.Run("the scope also has a row per pod; a workload has none", func(t *testing.T) {
		t.Parallel()
		var deployment int
		for _, r := range usageAmounts(report, nil, UsageFocus{}, 3).PodRows {
			if r.Owner != nil && *r.Owner == wlDeployment {
				deployment++
			}
		}
		if deployment != 3 {
			t.Errorf("%d pod rows of the Deployment, want 3", deployment)
		}
		if p := usageAmounts(report, nil, UsageFocus{Workload: &wlDeployment}, 3).PodRows; p != nil {
			t.Errorf("a workload has pod rows %+v; its Rows are pods", p)
		}
	})
	t.Run("a pod row names its owner", func(t *testing.T) {
		t.Parallel()
		r := usageAmounts(report, nil, UsageFocus{PodNamespace: "a", Pod: "web-1"}, 3).Rows
		if len(r) != 1 || r[0].Owner == nil || *r[0].Owner != wlDeployment {
			t.Errorf("pod rows = %+v", r)
		}
	})
}

func TestHealthOf(t *testing.T) {
	t.Parallel()
	pending := overviewRunningPod("a", "p")
	pending.Status.Phase = corev1.PodPending
	pending.Status.Conditions = nil
	tests := []struct {
		name string
		pod  *corev1.Pod
		want usagePodHealth
	}{
		{name: "running and ready", pod: overviewRunningPod("a", "p"), want: usagePodHealth{ready: true}},
		{name: "a waiting reason is the problem", pod: unready(waiting(overviewRunningPod("a", "p"), "ImagePullBackOff")), want: usagePodHealth{problem: "ImagePullBackOff"}},
		{name: "pending without a reason", pod: pending, want: usagePodHealth{problem: "Pending"}},
		{name: "not ready", pod: unready(overviewRunningPod("a", "p")), want: usagePodHealth{problem: "not ready"}},
		{
			name: "restarts and the last OOM kill count",
			pod:  restarted(overviewRunningPod("a", "p"), 4, "OOMKilled", time.Minute),
			want: usagePodHealth{ready: true, restarts: 4, oomKilled: 1},
		},
		{
			name: "another termination reason is no OOM kill",
			pod:  restarted(overviewRunningPod("a", "p"), 2, "Error", time.Minute),
			want: usagePodHealth{ready: true, restarts: 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := healthOf(tc.pod)
			got.containers = nil // TestContainersOfAPodRow
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("health = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestContainersOfAPodRow: a pod row lists its containers with what
// they do now and why they last stopped; a workload row lists none.
func TestContainersOfAPodRow(t *testing.T) {
	t.Parallel()
	p := restarted(overviewRunningPod("a", "p"), 3, "OOMKilled", time.Minute)
	cs := healthOf(p).containers
	if len(cs) == 0 {
		t.Fatal("no containers")
	}
	c := cs[len(cs)-1]
	if c.Init || c.Restarts != 3 || c.LastReason != "OOMKilled" || c.LastAt.IsZero() {
		t.Errorf("container = %+v", c)
	}
	w := healthOf(waiting(overviewRunningPod("a", "p"), "CrashLoopBackOff")).containers
	if c := w[len(w)-1]; c.State != "waiting" || c.Reason != "CrashLoopBackOff" {
		t.Errorf("waiting container = %+v", c)
	}
	rows := usageRows([]overviewPodAmount{{name: "p", namespace: "a", health: healthOf(p)}}, func(*overviewPodAmount) bool { return true }, false)
	if len(rows) != 1 || len(rows[0].Containers) != len(cs) {
		t.Errorf("pod rows = %+v", rows)
	}
	wl := InfoWorkload{Namespace: "a", Name: "web"}
	rows = usageRows([]overviewPodAmount{{name: "p", namespace: "a", owners: []InfoWorkload{wl}, health: healthOf(p)}}, func(*overviewPodAmount) bool { return true }, true)
	if len(rows) != 1 || rows[0].Workload == nil || rows[0].Containers != nil {
		t.Errorf("workload rows = %+v", rows)
	}
}
