package models

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/pkg/appconfig"
)

// lifeCompute computes one report at now from in, through life when set,
// the way the engine does.
func lifeCompute(in *overviewInput, now time.Time, m *overviewMeasured, life *overviewLifecycle) *overviewReport {
	r := computeOverviewWith(in, appconfig.DefaultOverview(), now, m, life)
	if life != nil {
		life.apply(r, now)
	}
	return r
}

// notReadyPod is a running pod created at created whose Ready condition
// became False at readySince.
func notReadyPod(name string, uid types.UID, created, readySince time.Time) corev1.Pod {
	return corev1.Pod{
		Namespace: "a", Name: name, UID: uid, CreationTimestamp: metav1.NewTime(created),
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(readySince)},
		}},
	}
}

func onlyTarget(t *testing.T, f overviewFinding) overviewTarget {
	t.Helper()
	if len(f.targets) != 1 {
		t.Fatalf("%s targets = %v, want one", f.id, targetKeys(f))
	}
	return f.targets[0]
}

func TestOverviewStartupGrace(t *testing.T) {
	t.Parallel()
	now := overviewTestNow
	t.Run("a new pod that is not ready is starting, then persistent", func(t *testing.T) {
		t.Parallel()
		created := now.Add(-time.Minute)
		in := &overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{notReadyPod("web", "u1", created, created)}}
		f := mustFinding(t, computeOverview(in, appconfig.DefaultOverview(), now, nil), "health/pods-not-ready")
		tg := onlyTarget(t, f)
		if f.severity != severityOK || f.state != "starting" || !tg.starting || tg.value != "0/1 ready, starting 60s (grace 5m)" {
			t.Fatalf("inside the grace: row %v %q, target %+v", f.severity, f.state, tg)
		}
		if !strings.Contains(f.explain, "startupGrace, 5m") {
			t.Fatalf("explain does not name the grace: %q", f.explain)
		}
		// Ten minutes later the same pod is still not ready.
		f = mustFinding(t, computeOverview(in, appconfig.DefaultOverview(), now.Add(9*time.Minute), nil), "health/pods-not-ready")
		tg = onlyTarget(t, f)
		if f.severity != severityWarning || f.state != "warning" || tg.starting || tg.value != "0/1 ready, for 10m" {
			t.Fatalf("after the grace: row %v %q, target %+v", f.severity, f.state, tg)
		}
	})
	t.Run("an old pod that lost readiness is not starting", func(t *testing.T) {
		t.Parallel()
		in := &overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{notReadyPod("web", "u1", now.Add(-24*time.Hour), now.Add(-30*time.Second))}}
		f := mustFinding(t, computeOverview(in, appconfig.DefaultOverview(), now, nil), "health/pods-not-ready")
		if tg := onlyTarget(t, f); f.severity != severityWarning || tg.value != "0/1 ready, for 30s" {
			t.Fatalf("row %v, target %+v", f.severity, tg)
		}
	})
	t.Run("explicit pod failures bypass the grace", func(t *testing.T) {
		t.Parallel()
		created := metav1.NewTime(now.Add(-time.Minute))
		pending := func(name string, mut func(*corev1.Pod)) corev1.Pod {
			p := corev1.Pod{Namespace: "a", Name: name, CreationTimestamp: created, Status: corev1.PodStatus{Phase: corev1.PodPending}}
			mut(&p)
			return p
		}
		in := &overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{
			pending("creating", func(p *corev1.Pod) {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}}
			}),
			pending("image", func(p *corev1.Pod) {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}}}}
			}),
			pending("unschedulable", func(p *corev1.Pod) {
				p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable}}
			}),
		}}
		f := mustFinding(t, computeOverview(in, appconfig.DefaultOverview(), now, nil), "health/pods-pending")
		got := make(map[string]overviewTarget)
		for _, tg := range f.targets {
			got[tg.name] = tg
		}
		if tg := got["creating"]; !tg.starting || tg.severity != severityOK {
			t.Fatalf("creating = %+v, want starting", tg)
		}
		if tg := got["image"]; tg.starting || tg.severity != severityWarning || tg.value != "ErrImagePull (app), for 60s" {
			t.Fatalf("image = %+v, want a warning inside the grace", tg)
		}
		if tg := got["unschedulable"]; tg.starting || tg.severity != severityFailed {
			t.Fatalf("unschedulable = %+v, want failed inside the grace", tg)
		}
		if f.severity != severityFailed {
			t.Fatalf("row severity = %v", f.severity)
		}
	})
	t.Run("a failed rollout is visible inside the grace", func(t *testing.T) {
		t.Parallel()
		dep := func(name string, uid types.UID, conds ...appsv1.DeploymentCondition) appsv1.Deployment {
			return appsv1.Deployment{
				Namespace: "a", Name: name, UID: uid,
				Spec:   appsv1.DeploymentSpec{Replicas: new(int32(2))},
				Status: appsv1.DeploymentStatus{UpdatedReplicas: 1, Conditions: conds},
			}
		}
		minuteAgo := metav1.NewTime(now.Add(-time.Minute))
		unavailable := appsv1.DeploymentCondition{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, LastTransitionTime: minuteAgo}
		failed := appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", LastTransitionTime: minuteAgo}
		rs := metav1.PartialObjectMetadata{
			Namespace: "a", Name: "rolling-2", CreationTimestamp: minuteAgo,
			OwnerReferences: appsControllerRef("Deployment", "rolling", "d1")}
		in := &overviewInput{
			namespace:   AllNamespaces,
			deployments: []appsv1.Deployment{dep("rolling", "d1", unavailable), dep("stuck", "d2", unavailable, failed)},
			replicaSets: []metav1.PartialObjectMetadata{rs},
		}
		r := computeOverview(in, appconfig.DefaultOverview(), now, nil)
		byName := func(f overviewFinding) map[string]overviewTarget {
			out := make(map[string]overviewTarget)
			for _, tg := range f.targets {
				out[tg.name] = tg
			}
			return out
		}
		un := byName(mustFinding(t, r, "health/workloads-unavailable"))
		if tg := un["rolling"]; !tg.starting || tg.value != "available 0/2, starting 60s (grace 5m)" {
			t.Fatalf("rolling unavailable = %+v", tg)
		}
		if tg := un["stuck"]; tg.starting || tg.severity != severityFailed {
			t.Fatalf("stuck unavailable = %+v, want failed", tg)
		}
		ro := mustFinding(t, r, "health/workloads-rollout")
		if tg := byName(ro)["rolling"]; !tg.starting || tg.value != "updated 1/2, starting 60s (grace 5m)" {
			t.Fatalf("rolling rollout = %+v", tg)
		}
		if tg := byName(ro)["stuck"]; tg.severity != severityFailed || ro.severity != severityFailed {
			t.Fatalf("stuck rollout = %+v, row %v", tg, ro.severity)
		}
	})
}

func TestOverviewUsageHysteresis(t *testing.T) {
	t.Parallel()
	pod := *withPod(overviewRunningPod("a", "near"), func(p *corev1.Pod) {
		p.UID = "u1"
		p.Spec.Containers = []corev1.Container{{Name: "app", Resources: resources("", "", "", "100Mi")}}
	})
	in := &overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{pod}}
	at := func(mib int64) *overviewMeasured {
		return &overviewMeasured{pods: map[string][]containerUsage{"a/near": {{name: "app", mem: mib << 20}}}}
	}
	values := func(r *overviewReport) []string {
		var out []string
		for _, tg := range mustFinding(t, r, "risks/pods-memory-limit").targets {
			out = append(out, tg.value)
		}
		return out
	}
	now := overviewTestNow
	life := newOverviewLifecycle()
	steps := []struct {
		mib  int64
		want []string
	}{
		{92, []string{"app 92% of 100Mi"}},
		// Below 90% but not below 85%: held.
		{88, []string{"app 88% of 100Mi, held until below 85%"}},
		{86, []string{"app 86% of 100Mi, held until below 85%"}},
		// Below 85%: released, and listed as recovered.
		{84, []string{"recovered 0s ago (was: app 86% of 100Mi, held until below 85%)"}},
		// Back between 85% and 90% after a release: not held.
		{88, []string{"recovered 1s ago (was: app 86% of 100Mi, held until below 85%)"}},
	}
	for i, s := range steps {
		r := lifeCompute(in, now.Add(time.Duration(i)*time.Second), at(s.mib), life)
		if got := values(r); !slices.Equal(got, s.want) {
			t.Fatalf("step %d (%d MiB): values = %q, want %q", i, s.mib, got, s.want)
		}
	}
	// Without the session (one report), 88% is simply below the threshold.
	if got := values(lifeCompute(in, now, at(88), nil)); len(got) != 0 {
		t.Fatalf("88%% without a previous report = %q", got)
	}
}

// a pod whose image cannot be pulled; the waiting rules have no server date.
func imagePod(name string, uid types.UID) corev1.Pod {
	return corev1.Pod{
		Namespace: "a", Name: name, UID: uid,
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
		}},
	}
}

func TestOverviewLifecycleSession(t *testing.T) {
	t.Parallel()
	const id = "health/pods-image"
	now := overviewTestNow
	value := func(t *testing.T, r *overviewReport) []string {
		t.Helper()
		var out []string
		for _, tg := range mustFinding(t, r, id).targets {
			out = append(out, string(tg.uid)+" "+tg.severity.String()+" "+tg.value)
		}
		return out
	}
	t.Run("undated state is aged by the session, then recovers and expires", func(t *testing.T) {
		t.Parallel()
		life := newOverviewLifecycle()
		failing := &overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{imagePod("web", "u1")}}
		fixed := &overviewInput{namespace: AllNamespaces}
		steps := []struct {
			in    *overviewInput
			after time.Duration
			want  []string
		}{
			{failing, 0, []string{"u1 failed ImagePullBackOff (app)"}},
			{failing, 3 * time.Minute, []string{"u1 failed ImagePullBackOff (app), seen 3m this session"}},
			{fixed, 4 * time.Minute, []string{"u1 ok recovered 0s ago (was: ImagePullBackOff (app), seen 3m this session)"}},
			{fixed, 4*time.Minute + recoveredRetention, []string{"u1 ok recovered 10m ago (was: ImagePullBackOff (app), seen 3m this session)"}},
			{fixed, 5*time.Minute + recoveredRetention, nil},
		}
		for i, s := range steps {
			r := lifeCompute(s.in, now.Add(s.after), nil, life)
			if got := value(t, r); !slices.Equal(got, s.want) {
				t.Fatalf("step %d: targets = %q, want %q", i, got, s.want)
			}
			if f := mustFinding(t, r, id); s.in == fixed && (f.value != "0" || f.severity != severityOK) {
				t.Fatalf("step %d: a recovered target changed the row: %q %v", i, f.value, f.severity)
			}
		}
	})
	t.Run("a denied source does not resolve an active finding", func(t *testing.T) {
		t.Parallel()
		life := newOverviewLifecycle()
		failing := &overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{imagePod("web", "u1")}}
		denied := &overviewInput{namespace: AllNamespaces, missing: map[schema.GroupVersionResource]string{ovPodsGVR: "forbidden"}}
		lifeCompute(failing, now, nil, life)
		r := lifeCompute(denied, now.Add(time.Minute), nil, life)
		f := mustFinding(t, r, id)
		if f.value != "n/a" || f.unavailable != "pods: forbidden" {
			t.Fatalf("row = %q %q", f.value, f.unavailable)
		}
		if got, want := value(t, r), []string{"u1 n/a unknown (pods: forbidden); last seen ImagePullBackOff (app)"}; !slices.Equal(got, want) {
			t.Fatalf("targets = %q, want %q", got, want)
		}
		// The source comes back and the pod still fails: the session
		// duration runs on from the first sighting, not from the gap.
		r = lifeCompute(failing, now.Add(2*time.Minute), nil, life)
		if got, want := value(t, r), []string{"u1 failed ImagePullBackOff (app), seen 2m this session"}; !slices.Equal(got, want) {
			t.Fatalf("after the gap: targets = %q, want %q", got, want)
		}
	})
	t.Run("a recreated object starts a new lifecycle", func(t *testing.T) {
		t.Parallel()
		life := newOverviewLifecycle()
		lifeCompute(&overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{imagePod("web", "u1")}}, now, nil, life)
		recreated := &overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{imagePod("web", "u2")}}
		r := lifeCompute(recreated, now.Add(5*time.Minute), nil, life)
		// The old UID recovered, but its row would share the name: only
		// the new object is listed, with no inherited duration.
		if got, want := value(t, r), []string{"u2 failed ImagePullBackOff (app)"}; !slices.Equal(got, want) {
			t.Fatalf("targets = %q, want %q", got, want)
		}
		r = lifeCompute(recreated, now.Add(6*time.Minute), nil, life)
		if got, want := value(t, r), []string{"u2 failed ImagePullBackOff (app), seen 60s this session"}; !slices.Equal(got, want) {
			t.Fatalf("targets = %q, want %q", got, want)
		}
	})
	t.Run("a dated state is not aged by the session", func(t *testing.T) {
		t.Parallel()
		life := newOverviewLifecycle()
		created := now.Add(-time.Hour)
		in := &overviewInput{namespace: AllNamespaces, pods: []corev1.Pod{notReadyPod("web", "u1", created, created)}}
		lifeCompute(in, now, nil, life)
		r := lifeCompute(in, now.Add(2*time.Minute), nil, life)
		if tg := onlyTarget(t, mustFinding(t, r, "health/pods-not-ready")); tg.value != "0/1 ready, for 62m" {
			t.Fatalf("value = %q", tg.value)
		}
	})
}
