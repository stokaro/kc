package models

import (
	"slices"
	"strings"
	"testing"

	"github.com/sttts/kc/pkg/appconfig"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func controllerRef(apiVersion, kind, name string, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid, Controller: ptr.To(true)}}
}

func ownedMeta(ns, name string, uid types.UID, owners []metav1.OwnerReference) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: ns, Name: name, UID: uid, OwnerReferences: owners}
}

// ownedPod is a running pod owned by owners whose app container has the
// given requests and limits.
func ownedPod(ns, name string, owners []metav1.OwnerReference, res corev1.ResourceRequirements, ready bool) *corev1.Pod {
	p := overviewRunningPod(ns, name)
	p.OwnerReferences = owners
	p.Spec.Containers[0].Resources = res
	if !ready {
		p.Status.Conditions[0].Status = corev1.ConditionFalse
	}
	return p
}

var (
	wlDeployment  = InfoWorkload{GVR: ovDeploymentsGVR, Namespace: "a", Name: "web"}
	wlReplicaSet  = InfoWorkload{GVR: ovReplicaSetsGVR, Namespace: "a", Name: "web-abc"}
	wlCronJob     = InfoWorkload{GVR: ovCronJobsGVR, Namespace: "a", Name: "nightly"}
	wlJob         = InfoWorkload{GVR: ovJobsGVR, Namespace: "a", Name: "nightly-1"}
	wlStatefulSet = InfoWorkload{GVR: ovStatefulSetsGVR, Namespace: "a", Name: "db"}
)

// workloadObjects is a namespace with a Deployment (two pods through its
// ReplicaSet, one ready), a CronJob with a Job and a pod, a StatefulSet
// with a pod without limits, and pods that must not be grouped: one whose
// ReplicaSet is gone, one whose ReplicaSet was recreated (other UID), one
// owned by an unknown kind, and a terminated pod of the Deployment.
func workloadObjects() []crclient.Object {
	rsRef := controllerRef("apps/v1", "ReplicaSet", "web-abc", "rs-uid")
	limited := resources("100m", "64Mi", "200m", "128Mi")
	succeeded := ownedPod("a", "web-done", rsRef, limited, false)
	succeeded.Status.Phase = corev1.PodSucceeded
	return []crclient.Object{
		&appsv1.Deployment{ObjectMeta: ownedMeta("a", "web", "dep-uid", nil), Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](2)}},
		&appsv1.ReplicaSet{ObjectMeta: ownedMeta("a", "web-abc", "rs-uid", controllerRef("apps/v1", "Deployment", "web", "dep-uid"))},
		ownedPod("a", "web-1", rsRef, limited, true),
		ownedPod("a", "web-2", rsRef, limited, false),
		succeeded,
		ownedPod("a", "orphan", controllerRef("apps/v1", "ReplicaSet", "gone", "gone-uid"), limited, true),
		ownedPod("a", "stale", controllerRef("apps/v1", "ReplicaSet", "web-abc", "old-uid"), limited, true),
		ownedPod("a", "rollout", controllerRef("argoproj.io/v1alpha1", "Rollout", "web", "ro-uid"), limited, true),
		&batchv1.CronJob{ObjectMeta: ownedMeta("a", "nightly", "cron-uid", nil)},
		&batchv1.Job{ObjectMeta: ownedMeta("a", "nightly-1", "job-uid", controllerRef("batch/v1", "CronJob", "nightly", "cron-uid"))},
		ownedPod("a", "nightly-1-x", controllerRef("batch/v1", "Job", "nightly-1", "job-uid"), limited, true),
		&appsv1.StatefulSet{ObjectMeta: ownedMeta("a", "db", "sts-uid", nil), Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1)}},
		ownedPod("a", "db-0", controllerRef("apps/v1", "StatefulSet", "db", "sts-uid"), resources("100m", "64Mi", "", ""), true),
	}
}

func workloadMeasured() *overviewMeasured {
	use := func(cpu int64, memMi int64) []containerUsage {
		return []containerUsage{{name: "app", cpu: cpu, mem: memMi << 20}}
	}
	return &overviewMeasured{
		usageAt: overviewTestNow,
		pods: map[string][]containerUsage{
			"a/web-1": use(150, 120), "a/web-2": use(50, 20), "a/orphan": use(1, 1),
			"a/nightly-1-x": use(10, 10), "a/db-0": use(300, 30),
		},
	}
}

func TestSummarizeWorkloadsGroupsPodsByController(t *testing.T) {
	t.Parallel()
	r := measuredReport(t, AllNamespaces, workloadMeasured(), workloadObjects()...)
	tests := []struct {
		w              InfoWorkload
		pods, ready    int64
		desired        int64
		hasDesired     bool
		cpuReq, cpuLim int64
		cpuUse         int64
		memUnlimited   bool
		cpuUnlimited   bool
		wantTop        []string
	}{
		{w: wlDeployment, pods: 2, ready: 1, desired: 2, hasDesired: true, cpuReq: 200, cpuLim: 400, cpuUse: 200, wantTop: []string{"web-1", "web-2"}},
		{w: wlReplicaSet, pods: 2, ready: 1, cpuReq: 200, cpuLim: 400, cpuUse: 200, wantTop: []string{"web-1", "web-2"}},
		{w: wlCronJob, pods: 1, ready: 1, cpuReq: 100, cpuLim: 200, cpuUse: 10, wantTop: []string{"nightly-1-x"}},
		{w: wlJob, pods: 1, ready: 1, cpuReq: 100, cpuLim: 200, cpuUse: 10, wantTop: []string{"nightly-1-x"}},
		{w: wlStatefulSet, pods: 1, ready: 1, desired: 1, hasDesired: true, cpuReq: 100, cpuUse: 300, memUnlimited: true, cpuUnlimited: true, wantTop: []string{"db-0"}},
	}
	for _, tt := range tests {
		t.Run(tt.w.String(), func(t *testing.T) {
			t.Parallel()
			s := r.workloads[tt.w]
			if s == nil {
				t.Fatalf("no summary; have %v", workloadKeys(r))
			}
			if s.pods != tt.pods || s.ready != tt.ready || s.measured != tt.pods || s.desired != tt.desired || s.hasDesired != tt.hasDesired {
				t.Fatalf("pods %d ready %d measured %d desired %d/%v, want %d %d %d %d/%v", s.pods, s.ready, s.measured, s.desired, s.hasDesired, tt.pods, tt.ready, tt.pods, tt.desired, tt.hasDesired)
			}
			if s.cpuReq != tt.cpuReq || s.cpuLim != tt.cpuLim || s.cpuUse != tt.cpuUse || s.cpuUnlimited != tt.cpuUnlimited || s.memUnlimited != tt.memUnlimited {
				t.Fatalf("cpu req %d lim %d use %d unlimited %v/%v, want %d %d %d %v/%v", s.cpuReq, s.cpuLim, s.cpuUse, s.cpuUnlimited, s.memUnlimited, tt.cpuReq, tt.cpuLim, tt.cpuUse, tt.cpuUnlimited, tt.memUnlimited)
			}
			sum := workloadInfoSummary(r, nil, appconfig.DefaultOverview(), tt.w)
			var top []string
			for _, c := range sum.Top {
				top = append(top, c.Name)
			}
			if !slices.Equal(top, tt.wantTop) {
				t.Fatalf("top = %v, want %v", top, tt.wantTop)
			}
		})
	}
	// Pods that must not be grouped: no workload holds them.
	for w, s := range r.workloads {
		if w.Name == "gone" || w.GVR.Group == "argoproj.io" {
			t.Fatalf("unexpected workload %v: %+v", w, s)
		}
	}
	for _, p := range r.pods {
		if (p.name == "orphan" || p.name == "stale" || p.name == "rollout") && len(p.owners) > 0 {
			t.Fatalf("pod %s has owners %v, want none", p.name, p.owners)
		}
	}
}

func workloadKeys(r *overviewReport) []string {
	var out []string
	for w := range r.workloads {
		out = append(out, w.String())
	}
	slices.Sort(out)
	return out
}

func TestWorkloadInfoSummaryBars(t *testing.T) {
	t.Parallel()
	r := measuredReport(t, AllNamespaces, workloadMeasured(), workloadObjects()...)
	bar := func(s InfoSummary, label string) InfoBar {
		t.Helper()
		for _, b := range s.Bars {
			if b.Label == label {
				return b
			}
		}
		t.Fatalf("no %s bar in %+v", label, s.Bars)
		return InfoBar{}
	}
	cfg := appconfig.DefaultOverview()

	web := workloadInfoSummary(r, nil, cfg, wlDeployment)
	if b := bar(web, "CPU"); !b.Known || b.Percent != 50 || b.Flag != "ok" || !strings.HasPrefix(b.Detail, "use 200m of lim 400m") {
		t.Fatalf("web CPU = %+v, want 50%% of the limits", b)
	}
	// 140Mi of 256Mi is 55%; below memoryLimitWarnPercent.
	if b := bar(web, "Memory"); !b.Known || b.Flag != "ok" || !strings.HasPrefix(b.Detail, "use 140Mi of lim 256Mi") {
		t.Fatalf("web memory = %+v", b)
	}
	if b := bar(web, "Pods"); b.Percent != 50 || b.Flag != "not ready" || b.Detail != "1/2 ready, 2 pods" {
		t.Fatalf("web pods = %+v", b)
	}

	// db-0 has no limits: the bars compare with the requests; 300m is three
	// times the 100m request, so it is under-requested.
	db := workloadInfoSummary(r, nil, cfg, wlStatefulSet)
	if b := bar(db, "CPU"); !b.Known || b.Percent != 300 || b.Flag != flagUnderRequested || !strings.Contains(b.Detail, "no limit") {
		t.Fatalf("db CPU = %+v, want 300%% of requests, under-req", b)
	}
	if b := bar(db, "Pods"); b.Flag != "ok" || b.Detail != "1/1 ready, 1 pods" {
		t.Fatalf("db pods = %+v", b)
	}

	// A memory use near the limit is flagged.
	hot := workloadMeasured()
	hot.pods["a/web-1"] = []containerUsage{{name: "app", cpu: 1, mem: 125 << 20}}
	hot.pods["a/web-2"] = []containerUsage{{name: "app", cpu: 1, mem: 120 << 20}}
	hr := measuredReport(t, AllNamespaces, hot, workloadObjects()...)
	if b := bar(workloadInfoSummary(hr, nil, cfg, wlDeployment), "Memory"); b.Flag != flagNearLimit || b.Health != RowHealthNotReady {
		t.Fatalf("hot web memory = %+v, want near-limit", b)
	}

	// Without usage the bars show the requests of the limits and say why.
	none := measuredReport(t, AllNamespaces, &overviewMeasured{podsReason: "metrics.k8s.io is not served by this cluster", nodesReason: "x", statsReason: "x"}, workloadObjects()...)
	ns := workloadInfoSummary(none, nil, cfg, wlDeployment)
	if b := bar(ns, "CPU"); !b.Known || b.Percent != 50 || !strings.Contains(b.Detail, "use n/a: metrics.k8s.io is not served") {
		t.Fatalf("CPU without usage = %+v", b)
	}
	if !strings.Contains(ns.TopNote, "metrics.k8s.io is not served") {
		t.Fatalf("top note = %q", ns.TopNote)
	}

	// A workload with no pods says so.
	gone := workloadInfoSummary(r, nil, cfg, InfoWorkload{GVR: ovDeploymentsGVR, Namespace: "a", Name: "missing"})
	if b := bar(gone, "CPU"); b.Known || !strings.Contains(b.Detail, "has no pods") {
		t.Fatalf("missing workload CPU = %+v", b)
	}
}

// TestWorkloadInfoWithoutReplicaSets checks that a Deployment says why it
// cannot be shown when ReplicaSets cannot be read, while the ReplicaSet the
// pods name still groups them.
func TestWorkloadInfoWithoutReplicaSets(t *testing.T) {
	t.Parallel()
	in := loadOverviewInput(t.Context(), newOverviewFakeClient(workloadObjects()...), AllNamespaces, func(gvr schema.GroupVersionResource) string {
		if gvr == ovReplicaSetsGVR {
			return "forbidden"
		}
		return ""
	})
	r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, workloadMeasured())
	dep := workloadInfoSummary(r, nil, appconfig.DefaultOverview(), wlDeployment)
	if len(dep.Bars) == 0 || dep.Bars[0].Known || !strings.Contains(dep.Bars[0].Detail, "replicasets.apps: forbidden") {
		t.Fatalf("deployment bars = %+v, want the ReplicaSet reason", dep.Bars)
	}
	// web-1, web-2 and stale all name web-abc; without the list its UID
	// cannot be checked, so all three count.
	if s := r.workloads[wlReplicaSet]; s == nil || s.pods != 3 {
		t.Fatalf("replicaset summary = %+v, want 3 pods", s)
	}
}

func TestIsInfoWorkload(t *testing.T) {
	t.Parallel()
	for _, gvr := range []schema.GroupVersionResource{ovDeploymentsGVR, ovReplicaSetsGVR, ovStatefulSetsGVR, ovDaemonSetsGVR, ovJobsGVR, ovCronJobsGVR} {
		if !IsInfoWorkload(gvr) {
			t.Errorf("%v is not an Info workload", gvr)
		}
	}
	for _, gvr := range []schema.GroupVersionResource{ovPodsGVR, ovNodesGVR, {Group: "extensions", Version: "v1beta1", Resource: "deployments"}} {
		if IsInfoWorkload(gvr) {
			t.Errorf("%v is an Info workload", gvr)
		}
	}
}
