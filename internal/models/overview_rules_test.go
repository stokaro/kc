package models

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sttts/kc/pkg/appconfig"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var overviewTestNow = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) metav1.Time { return metav1.NewTime(overviewTestNow.Add(-d)) }

func om(ns, name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: ns, Name: name} }

func readyNode(name string, cpu, mem string, pods int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(mem),
				corev1.ResourcePods:   *resource.NewQuantity(pods, resource.DecimalSI),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func runningPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: om(ns, name),
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			QOSClass:          corev1.PodQOSBurstable,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true}},
		},
	}
}

func withPod(p *corev1.Pod, fn func(*corev1.Pod)) *corev1.Pod { fn(p); return p }

func resources(reqCPU, reqMem, limCPU, limMem string) corev1.ResourceRequirements {
	r := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	set := func(l corev1.ResourceList, n corev1.ResourceName, v string) {
		if v != "" {
			l[n] = resource.MustParse(v)
		}
	}
	set(r.Requests, corev1.ResourceCPU, reqCPU)
	set(r.Requests, corev1.ResourceMemory, reqMem)
	set(r.Limits, corev1.ResourceCPU, limCPU)
	set(r.Limits, corev1.ResourceMemory, limMem)
	return r
}

func waiting(p *corev1.Pod, reason string) *corev1.Pod {
	p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}
	return p
}

func restarted(p *corev1.Pod, count int32, reason string, finished time.Duration) *corev1.Pod {
	p.Status.ContainerStatuses[0].RestartCount = count
	p.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, FinishedAt: ago(finished)}}
	return p
}

func reportFor(t *testing.T, ns string, cfg appconfig.OverviewConfig, objs ...crclient.Object) *overviewReport {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
	in := loadOverviewInput(t.Context(), cl, ns, nil)
	if len(in.missing) > 0 {
		t.Fatalf("fake cache could not list: %v", in.missing)
	}
	return computeOverview(in, cfg, overviewTestNow, nil)
}

func targetKeys(f overviewFinding) []string {
	out := make([]string, 0, len(f.targets))
	for _, t := range f.targets {
		key := t.name
		if t.namespace != "" {
			key = t.namespace + "/" + t.name
		}
		out = append(out, t.gvr.Resource+":"+key)
	}
	return out
}

func mustFinding(t *testing.T, r *overviewReport, id string) overviewFinding {
	t.Helper()
	f, ok := r.finding(id)
	if !ok {
		t.Fatalf("no finding %q", id)
	}
	return f
}

// TestOverviewRules checks every Health, Risk and Activity rule: the objects
// it must list (in order), and at least one look-alike it must not list.
func TestOverviewRules(t *testing.T) {
	t.Parallel()
	cronUID := types.UID("cron-uid")
	ownedJob := func(name string, created time.Duration, failed bool) *batchv1.Job {
		j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Namespace: "a", Name: name, CreationTimestamp: ago(created),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: "nightly", UID: cronUID, Controller: ptr.To(true)}},
		}}
		if failed {
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}
		}
		return j
	}
	event := func(name, typ string, seen time.Duration) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "a", Name: name, CreationTimestamp: ago(3 * time.Hour)},
			Type:           typ,
			Reason:         "BackOff",
			Count:          4,
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web"},
			LastTimestamp:  ago(seen),
		}
	}

	tests := []struct {
		name    string
		finding string
		objs    []crclient.Object
		want    []string
		sev     overviewSeverity
		value   string // substring of the first target's value
	}{
		{
			name: "nodes not ready", finding: "health/nodes-not-ready",
			objs: []crclient.Object{
				readyNode("good", "4", "8Gi", 110),
				func() *corev1.Node {
					n := readyNode("bad", "4", "8Gi", 110)
					n.Status.Conditions[0] = corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionUnknown, Reason: "NodeStatusUnknown"}
					return n
				}(),
			},
			want: []string{"nodes:bad"}, sev: severityFailed, value: "Ready=Unknown NodeStatusUnknown",
		},
		{
			name: "nodes under pressure", finding: "health/nodes-pressure",
			objs: []crclient.Object{
				readyNode("good", "4", "8Gi", 110),
				func() *corev1.Node {
					n := readyNode("hot", "4", "8Gi", 110)
					n.Status.Conditions = append(n.Status.Conditions,
						corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
						corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse})
					return n
				}(),
			},
			want: []string{"nodes:hot"}, sev: severityWarning, value: "MemoryPressure",
		},
		{
			name: "nodes cordoned", finding: "health/nodes-cordoned",
			objs: []crclient.Object{
				readyNode("good", "4", "8Gi", 110),
				func() *corev1.Node { n := readyNode("off", "4", "8Gi", 110); n.Spec.Unschedulable = true; return n }(),
			},
			want: []string{"nodes:off"}, sev: severityWarning,
		},
		{
			name: "pods not ready", finding: "health/pods-not-ready",
			objs: []crclient.Object{
				runningPod("a", "ready"),
				withPod(runningPod("a", "unready"), func(p *corev1.Pod) {
					p.Status.Conditions[0].Status = corev1.ConditionFalse
					p.Status.ContainerStatuses[0].Ready = false
				}),
				withPod(runningPod("a", "done"), func(p *corev1.Pod) {
					p.Status.Phase = corev1.PodSucceeded
					p.Status.Conditions[0].Status = corev1.ConditionFalse
				}),
			},
			want: []string{"pods:a/unready"}, sev: severityWarning, value: "0/1 ready",
		},
		{
			name: "pods pending, unschedulable fails", finding: "health/pods-pending",
			objs: []crclient.Object{
				runningPod("a", "running"),
				withPod(runningPod("a", "stuck"), func(p *corev1.Pod) {
					p.Status.Phase = corev1.PodPending
					p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable}}
				}),
				withPod(runningPod("a", "pulling"), func(p *corev1.Pod) {
					p.Status.Phase = corev1.PodPending
					p.Status.Conditions = nil
					p.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}
				}),
			},
			want: []string{"pods:a/stuck", "pods:a/pulling"}, sev: severityFailed, value: "Unschedulable",
		},
		{
			name: "pods crash looping", finding: "health/pods-crashloop",
			objs: []crclient.Object{
				waiting(runningPod("a", "loop"), "CrashLoopBackOff"),
				waiting(runningPod("a", "creating"), "ContainerCreating"),
			},
			want: []string{"pods:a/loop"}, sev: severityFailed, value: "CrashLoopBackOff (app)",
		},
		{
			name: "pods image pull errors", finding: "health/pods-image",
			objs: []crclient.Object{
				waiting(runningPod("a", "pull"), "ImagePullBackOff"),
				waiting(runningPod("a", "err"), "ErrImagePull"),
				waiting(runningPod("a", "loop"), "CrashLoopBackOff"),
			},
			want: []string{"pods:a/err", "pods:a/pull"}, sev: severityFailed,
		},
		{
			name: "pods config errors", finding: "health/pods-config",
			objs: []crclient.Object{
				waiting(runningPod("a", "cfg"), "CreateContainerConfigError"),
				runningPod("a", "fine"),
			},
			want: []string{"pods:a/cfg"}, sev: severityFailed,
		},
		{
			name: "pods OOMKilled inside the window only", finding: "health/pods-oomkilled",
			objs: []crclient.Object{
				restarted(runningPod("a", "recent"), 1, "OOMKilled", 10*time.Minute),
				restarted(runningPod("a", "old"), 1, "OOMKilled", 2*time.Hour),
				restarted(runningPod("a", "error"), 1, "Error", 10*time.Minute),
			},
			want: []string{"pods:a/recent"}, sev: severityWarning, value: "OOMKilled (app) 10m ago",
		},
		{
			name: "pods restarting above the threshold", finding: "health/pods-restarting",
			objs: []crclient.Object{
				restarted(runningPod("a", "flappy"), 7, "Error", 5*time.Minute),
				restarted(runningPod("a", "once"), 2, "Error", 5*time.Minute),
				restarted(runningPod("a", "settled"), 9, "Error", 3*time.Hour),
			},
			want: []string{"pods:a/flappy"}, sev: severityWarning, value: "7 restarts (app), last 5m ago",
		},
		{
			name: "workloads unavailable", finding: "health/workloads-unavailable",
			objs: []crclient.Object{
				&appsv1.Deployment{ObjectMeta: om("a", "down"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](2)}, Status: appsv1.DeploymentStatus{AvailableReplicas: 0}},
				&appsv1.Deployment{ObjectMeta: om("a", "half"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](2)}, Status: appsv1.DeploymentStatus{AvailableReplicas: 1}},
				&appsv1.Deployment{ObjectMeta: om("a", "up"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](2)}, Status: appsv1.DeploymentStatus{AvailableReplicas: 2}},
				&appsv1.Deployment{ObjectMeta: om("a", "scaled-down"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](0)}},
				&appsv1.StatefulSet{ObjectMeta: om("a", "db"), Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3)}, Status: appsv1.StatefulSetStatus{AvailableReplicas: 2}},
				&appsv1.DaemonSet{ObjectMeta: om("a", "agent"), Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, NumberAvailable: 3}},
			},
			want: []string{"deployments:a/down", "statefulsets:a/db", "deployments:a/half"}, sev: severityFailed, value: "available 0/2",
		},
		{
			name: "workloads rolling out or stuck", finding: "health/workloads-rollout",
			objs: []crclient.Object{
				&appsv1.Deployment{ObjectMeta: om("a", "stuck"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)}, Status: appsv1.DeploymentStatus{
					UpdatedReplicas: 1,
					Conditions:      []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}},
				}},
				&appsv1.Deployment{ObjectMeta: om("a", "done"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)}, Status: appsv1.DeploymentStatus{UpdatedReplicas: 1}},
				&appsv1.DaemonSet{ObjectMeta: om("a", "agent"), Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, UpdatedNumberScheduled: 1}},
				&appsv1.StatefulSet{ObjectMeta: om("a", "manual"), Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](2), UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}}},
			},
			want: []string{"deployments:a/stuck", "daemonsets:a/agent"}, sev: severityFailed,
		},
		{
			name: "jobs failed", finding: "health/jobs-failed",
			objs: []crclient.Object{
				&batchv1.Job{ObjectMeta: om("a", "bad"), Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"}}}},
				&batchv1.Job{ObjectMeta: om("a", "good"), Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}},
			},
			want: []string{"jobs:a/bad"}, sev: severityFailed, value: "DeadlineExceeded",
		},
		{
			name: "cronjob newest run failed", finding: "health/cronjobs-failed",
			objs: []crclient.Object{
				&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "nightly", UID: cronUID}},
				&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "recovered", UID: "other"}},
				ownedJob("nightly-1", 2*time.Hour, false),
				ownedJob("nightly-2", time.Hour, true),
				func() *batchv1.Job {
					j := ownedJob("recovered-1", 2*time.Hour, true)
					j.OwnerReferences[0].UID = "other"
					return j
				}(),
				func() *batchv1.Job {
					j := ownedJob("recovered-2", time.Hour, false)
					j.OwnerReferences[0].UID = "other"
					return j
				}(),
			},
			want: []string{"cronjobs:a/nightly"}, sev: severityFailed, value: "nightly-2: BackoffLimitExceeded",
		},
		{
			name: "cronjobs suspended", finding: "health/cronjobs-suspended",
			objs: []crclient.Object{
				&batchv1.CronJob{ObjectMeta: om("a", "paused"), Spec: batchv1.CronJobSpec{Suspend: ptr.To(true)}},
				&batchv1.CronJob{ObjectMeta: om("a", "active"), Spec: batchv1.CronJobSpec{Suspend: ptr.To(false)}},
			},
			want: []string{"cronjobs:a/paused"}, sev: severityWarning,
		},
		{
			name: "pvcs pending or lost", finding: "health/pvcs",
			objs: []crclient.Object{
				&corev1.PersistentVolumeClaim{ObjectMeta: om("a", "lost"), Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimLost}},
				&corev1.PersistentVolumeClaim{ObjectMeta: om("a", "wait"), Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}},
				&corev1.PersistentVolumeClaim{ObjectMeta: om("a", "bound"), Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}},
			},
			want: []string{"persistentvolumeclaims:a/lost", "persistentvolumeclaims:a/wait"}, sev: severityFailed,
		},
		{
			name: "pvs released or failed", finding: "health/pvs",
			objs: []crclient.Object{
				&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "old"}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}},
				&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "used"}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}},
			},
			want: []string{"persistentvolumes:old"}, sev: severityWarning,
		},
		{
			name: "pdbs blocking eviction", finding: "risks/pdbs-blocking",
			objs: []crclient.Object{
				&policyv1.PodDisruptionBudget{ObjectMeta: om("a", "strict"), Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0, ExpectedPods: 1, CurrentHealthy: 1, DesiredHealthy: 1}},
				&policyv1.PodDisruptionBudget{ObjectMeta: om("a", "loose"), Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 1, ExpectedPods: 2}},
				&policyv1.PodDisruptionBudget{ObjectMeta: om("a", "empty"), Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0, ExpectedPods: 0}},
			},
			want: []string{"poddisruptionbudgets:a/strict"}, sev: severityWarning, value: "0 allowed, 1/1 healthy",
		},
		{
			name: "pods without requests", finding: "risks/pods-besteffort",
			objs: []crclient.Object{
				withPod(runningPod("a", "free"), func(p *corev1.Pod) { p.Status.QOSClass = corev1.PodQOSBestEffort }),
				withPod(runningPod("a", "done"), func(p *corev1.Pod) {
					p.Status.QOSClass = corev1.PodQOSBestEffort
					p.Status.Phase = corev1.PodSucceeded
				}),
				runningPod("a", "burstable"),
			},
			want: []string{"pods:a/free"}, sev: severityWarning,
		},
		{
			name: "pods without memory limit", finding: "risks/pods-no-memory-limit",
			objs: []crclient.Object{
				withPod(runningPod("a", "open"), func(p *corev1.Pod) { p.Spec.Containers[0].Resources = resources("100m", "64Mi", "", "") }),
				withPod(runningPod("a", "capped"), func(p *corev1.Pod) { p.Spec.Containers[0].Resources = resources("", "", "", "128Mi") }),
				withPod(runningPod("a", "pod-level"), func(p *corev1.Pod) {
					p.Spec.Resources = &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}}
				}),
			},
			want: []string{"pods:a/open"}, sev: severityWarning, value: "no limit: app",
		},
		{
			name: "single replica without PDB", finding: "risks/single-replica",
			objs: []crclient.Object{
				&appsv1.Deployment{ObjectMeta: om("a", "lonely"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "lonely"}}}}},
				&appsv1.Deployment{ObjectMeta: om("a", "guarded"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "guarded"}}}}},
				&appsv1.Deployment{ObjectMeta: om("a", "many"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)}},
				&appsv1.Deployment{ObjectMeta: om("b", "elsewhere"), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "guarded"}}}}},
				&policyv1.PodDisruptionBudget{ObjectMeta: om("a", "guard"), Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "guarded"}}}},
			},
			// The PDB in namespace a does not cover the look-alike in b.
			want: []string{"deployments:a/lonely", "deployments:b/elsewhere"}, sev: severityWarning,
		},
		{
			name: "quotas near limit", finding: "risks/quotas",
			objs: []crclient.Object{
				quota("full", "10", "10"),
				quota("near", "8", "10"),
				quota("roomy", "2", "10"),
			},
			want: []string{"resourcequotas:a/full", "resourcequotas:a/near"}, sev: severityFailed, value: "pods 10/10 (100%)",
		},
		{
			name: "hpas at max or without metrics", finding: "risks/hpas",
			objs: []crclient.Object{
				&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: om("a", "maxed"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 5}, Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 5}},
				&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: om("a", "blind"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 5}, Status: autoscalingv2.HorizontalPodAutoscalerStatus{
					CurrentReplicas: 1,
					Conditions:      []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric"}},
				}},
				&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: om("a", "fine"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 5}, Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 2}},
			},
			want: []string{"horizontalpodautoscalers:a/blind", "horizontalpodautoscalers:a/maxed"}, sev: severityWarning,
		},
		{
			name: "warning events in the window, newest first", finding: "activity/events-warning",
			objs: []crclient.Object{
				event("older", corev1.EventTypeWarning, 30*time.Minute),
				event("newer", corev1.EventTypeWarning, 2*time.Minute),
				event("stale", corev1.EventTypeWarning, 2*time.Hour),
				event("normal", corev1.EventTypeNormal, time.Minute),
			},
			want: []string{"events:a/newer", "events:a/older"}, sev: severityWarning, value: "BackOff Pod/web x4, 2m ago",
		},
		{
			name: "pods restarted in the window, latest first", finding: "activity/pods-restarted",
			objs: []crclient.Object{
				restarted(runningPod("a", "early"), 1, "Error", 40*time.Minute),
				restarted(runningPod("a", "late"), 1, "Completed", 5*time.Minute),
				restarted(runningPod("a", "long-ago"), 4, "Error", 5*time.Hour),
				runningPod("a", "never"),
			},
			want: []string{"pods:a/late", "pods:a/early"}, sev: severityWarning, value: "app: Completed, 5m ago",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := mustFinding(t, reportFor(t, AllNamespaces, appconfig.DefaultOverview(), tc.objs...), tc.finding)
			if got := targetKeys(f); !slices.Equal(got, tc.want) {
				t.Fatalf("targets = %v, want %v", got, tc.want)
			}
			if f.severity != tc.sev {
				t.Fatalf("severity = %v, want %v", f.severity, tc.sev)
			}
			if f.value != strconv.Itoa(len(tc.want)) {
				t.Fatalf("value = %q, want the count %d", f.value, len(tc.want))
			}
			if tc.value != "" && !strings.Contains(f.targets[0].value, tc.value) {
				t.Fatalf("first target value = %q, want it to contain %q", f.targets[0].value, tc.value)
			}
			if f.explain == "" || len(f.kubectl) == 0 {
				t.Fatalf("finding %s has no explanation or kubectl command", f.id)
			}
		})
	}
}

// TestOverviewRulesWithNothingToReport checks that an empty cluster reports
// every rule as ok with a count of zero.
func TestOverviewRulesWithNothingToReport(t *testing.T) {
	t.Parallel()
	web := withPod(runningPod("a", "web"), func(p *corev1.Pod) { p.Spec.Containers[0].Resources = resources("100m", "64Mi", "", "64Mi") })
	r := reportFor(t, AllNamespaces, appconfig.DefaultOverview(), readyNode("n1", "4", "8Gi", 110), web)
	if len(r.findings) != len(overviewRules) {
		t.Fatalf("findings = %d, want one per rule (%d)", len(r.findings), len(overviewRules))
	}
	for _, f := range r.findings {
		if f.section == overviewCapacity {
			continue
		}
		if f.value != "0" || f.severity != severityOK || f.state != "ok" {
			t.Errorf("%s = %q %v %q, want 0 ok", f.id, f.value, f.severity, f.state)
		}
	}
}

func TestOverviewNamespaceScope(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{
		waiting(runningPod("a", "loop"), "CrashLoopBackOff"),
		waiting(runningPod("b", "loop"), "CrashLoopBackOff"),
		&batchv1.Job{ObjectMeta: om("b", "bad"), Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-a"}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Namespace: "a"}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-b"}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Namespace: "b"}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}},
	}
	r := reportFor(t, "a", appconfig.DefaultOverview(), objs...)
	if got := targetKeys(mustFinding(t, r, "health/pods-crashloop")); !slices.Equal(got, []string{"pods:a/loop"}) {
		t.Fatalf("crash looping in a = %v", got)
	}
	if got := targetKeys(mustFinding(t, r, "health/jobs-failed")); len(got) != 0 {
		t.Fatalf("failed jobs in a = %v, want none", got)
	}
	if got := targetKeys(mustFinding(t, r, "health/pvs")); !slices.Equal(got, []string{"persistentvolumes:pv-a"}) {
		t.Fatalf("pvs in a = %v", got)
	}
	all := reportFor(t, AllNamespaces, appconfig.DefaultOverview(), objs...)
	if got := targetKeys(mustFinding(t, all, "health/pods-crashloop")); len(got) != 2 {
		t.Fatalf("crash looping across namespaces = %v, want both", got)
	}
}

// TestOverviewCapacity checks the sums over nodes, the init container rule
// and that terminated pods and pods of other namespaces count the same in
// a namespace scope (the capacity of a node depends on all its pods).
func TestOverviewCapacity(t *testing.T) {
	t.Parallel()
	pod := func(ns, name, node string, r corev1.ResourceRequirements) *corev1.Pod {
		return withPod(runningPod(ns, name), func(p *corev1.Pod) {
			p.Spec.NodeName = node
			p.Spec.Containers[0].Resources = r
		})
	}
	objs := []crclient.Object{
		readyNode("n1", "4", "8Gi", 10),
		readyNode("n2", "4", "8Gi", 10),
		pod("a", "web", "n1", resources("1", "1Gi", "2", "4Gi")),
		pod("b", "db", "n1", resources("2", "1Gi", "4", "6Gi")),
		// The init container asks for more than the app: it counts.
		withPod(pod("b", "migrate", "n2", resources("500m", "512Mi", "", "")), func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "init", Resources: resources("2", "1Gi", "", "")}}
		}),
		withPod(pod("a", "done", "n2", resources("4", "8Gi", "", "")), func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }),
		pod("a", "unscheduled", "", resources("4", "8Gi", "", "")),
	}
	r := reportFor(t, "a", appconfig.DefaultOverview(), objs...)

	cpu := mustFinding(t, r, "capacity/cpu")
	// requests 1+2+2 = 5 of 8 cores; limits 2+4 = 6 of 8.
	if cpu.value != "use n/a req 62% lim 75%" {
		t.Fatalf("cpu value = %q", cpu.value)
	}
	// n1: requests 3/4 = 75%, limits 6/4 = 150%: overcommitted.
	if cpu.state != "overcommitted" || cpu.severity != severityWarning {
		t.Fatalf("cpu state = %q %v, want overcommitted warning", cpu.state, cpu.severity)
	}
	if got := targetKeys(cpu); !slices.Equal(got, []string{"nodes:n1", "nodes:n2"}) {
		t.Fatalf("cpu targets = %v, the most loaded first", got)
	}
	if !strings.Contains(cpu.targets[0].value, "req 75% lim 150% overcommitted") {
		t.Fatalf("n1 value = %q", cpu.targets[0].value)
	}

	mem := mustFinding(t, r, "capacity/memory")
	// requests 1+1+1 Gi = 3 of 16 Gi; limits 4+6 = 10 of 16, but all on
	// n1 with 8 Gi: the row is overcommitted while the sum looks fine.
	if mem.value != "use n/a req 19% lim 62%" || mem.state != "overcommitted" {
		t.Fatalf("memory = %q %q", mem.value, mem.state)
	}

	pods := mustFinding(t, r, "capacity/pods")
	if pods.value != "3/20" || pods.state != "ok" {
		t.Fatalf("pods = %q %q, want 3/20 ok", pods.value, pods.state)
	}
}

func TestOverviewCapacityUsagePlugsIn(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(readyNode("n1", "4", "8Gi", 10)).Build()
	in := loadOverviewInput(t.Context(), cl, AllNamespaces, nil)
	usage := func() (map[string]nodeUsage, bool) {
		return map[string]nodeUsage{"n1": {cpuMilli: 1000, memBytes: 2 << 30}}, true
	}
	r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, usage)
	if got := mustFinding(t, r, "capacity/cpu").value; got != "use 25% req 0% lim 0%" {
		t.Fatalf("cpu with usage = %q", got)
	}
	if got := mustFinding(t, r, "capacity/memory").value; got != "use 25% req 0% lim 0%" {
		t.Fatalf("memory with usage = %q", got)
	}
}

// TestOverviewThresholdsFromConfig checks that the overview: thresholds move
// the verdicts.
func TestOverviewThresholdsFromConfig(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{
		readyNode("n1", "4", "8Gi", 10),
		withPod(runningPod("a", "web"), func(p *corev1.Pod) {
			p.Spec.NodeName = "n1"
			p.Spec.Containers[0].Resources = resources("3", "1Gi", "", "")
		}),
		restarted(runningPod("a", "flappy"), 2, "Error", 50*time.Minute),
		quota("q", "6", "10"),
	}
	def := reportFor(t, AllNamespaces, appconfig.DefaultOverview(), objs...)
	if got := mustFinding(t, def, "activity/events-warning").title; got != "/events warnings (1h)" {
		t.Fatalf("title with the default window = %q", got)
	}
	if got := mustFinding(t, def, "capacity/cpu").state; got != "ok" {
		t.Fatalf("cpu 75%% with defaults = %q, want ok", got)
	}
	if got := mustFinding(t, def, "health/pods-restarting").value; got != "0" {
		t.Fatalf("2 restarts with default threshold 3 = %q, want 0", got)
	}
	if got := mustFinding(t, def, "risks/quotas").value; got != "0" {
		t.Fatalf("quota 60%% with defaults = %q, want 0", got)
	}

	cfg := appconfig.OverviewConfig{RequestsWarnPercent: 70, RestartThreshold: 2, QuotaWarnPercent: 50, Window: metav1.Duration{Duration: 30 * time.Minute}}
	strict := reportFor(t, AllNamespaces, cfg, objs...)
	if got := mustFinding(t, strict, "capacity/cpu").state; got != "high" {
		t.Fatalf("cpu 75%% with warn 70 = %q, want high", got)
	}
	// Threshold 2 matches, but the 30m window excludes the restart 50m ago.
	if got := mustFinding(t, strict, "health/pods-restarting").value; got != "0" {
		t.Fatalf("restart outside a 30m window = %q, want 0", got)
	}
	if got := mustFinding(t, strict, "health/pods-oomkilled").title; got != "/pods OOMKilled (30m)" {
		t.Fatalf("title follows the window: %q", got)
	}
	if got := mustFinding(t, strict, "risks/quotas").value; got != "1" {
		t.Fatalf("quota 60%% with warn 50 = %q, want 1", got)
	}
	cfg.Window = metav1.Duration{Duration: time.Hour}
	if got := mustFinding(t, reportFor(t, AllNamespaces, cfg, objs...), "health/pods-restarting").value; got != "1" {
		t.Fatalf("2 restarts with threshold 2 in 1h = %q, want 1", got)
	}
}

// quota builds a pods quota in namespace a.
func quota(name, used, hard string) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{ObjectMeta: om("a", name), Status: corev1.ResourceQuotaStatus{
		Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse(hard)},
		Used: corev1.ResourceList{corev1.ResourcePods: resource.MustParse(used)},
	}}
}

func TestOverviewUnavailableSourceDimsRows(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	in := loadOverviewInput(t.Context(), cl, AllNamespaces, func(gvr schema.GroupVersionResource) string {
		if gvr == ovPDBsGVR {
			return "forbidden"
		}
		return ""
	})
	r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil)
	for _, id := range []string{"risks/pdbs-blocking", "risks/single-replica"} {
		f := mustFinding(t, r, id)
		if f.severity != severityUnknown || f.value != "n/a" || !strings.Contains(f.unavailable, "poddisruptionbudgets.policy: forbidden") {
			t.Fatalf("%s = %q %v %q, want n/a with the reason", id, f.value, f.severity, f.unavailable)
		}
	}
	if f := mustFinding(t, r, "health/pods-crashloop"); f.severity != severityOK {
		t.Fatalf("a row that does not need PDBs is %v, want ok", f.severity)
	}
}
