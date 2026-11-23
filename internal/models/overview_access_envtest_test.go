package models

import (
	"context"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// These tests are white-box: the overview's report and pool leases are
// internal, and the acceptance criteria of #182 are about exactly them
// (which findings a namespace-only user gets, and which caches run).

// grantOverviewReads lets user read the overview's namespaced workload
// kinds in namespace, besides the pods newNamespaceUserEnv grants.
func grantOverviewReads(t *testing.T, cl *kccluster.Cluster, namespace, user string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	role := &rbacv1.Role{
		Name: "overview-reader", Namespace: namespace,
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"apps"}, Resources: []string{"deployments", "replicasets"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	if err := cl.GetClient().Create(ctx, role); err != nil {
		t.Fatalf("create role: %v", err)
	}
	binding := &rbacv1.RoleBinding{
		Name: "overview-reader", Namespace: namespace,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
	}
	if err := cl.GetClient().Create(ctx, binding); err != nil {
		t.Fatalf("create rolebinding: %v", err)
	}
	waitGranted(t, cl, user, namespace, "", "events")
	waitGranted(t, cl, user, namespace, "apps", "deployments")
}

// waitGranted waits until the API server's authorizer has seen a new grant,
// asked with a SubjectAccessReview so the user's own cached answers stay
// untouched.
func waitGranted(t *testing.T, cl *kccluster.Cluster, user, namespace, group, resource string) {
	t.Helper()
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
			User:               user,
			ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: namespace, Verb: "watch", Group: group, Resource: resource},
		}}
		if err := cl.GetClient().Create(ctx, review); err != nil {
			return false
		}
		return review.Status.Allowed
	}, "grant of "+resource+" to "+user+" never took effect")
}

// grantNodeReads lets user list and watch nodes, cluster-wide.
func grantNodeReads(t *testing.T, cl *kccluster.Cluster, user string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	role := &rbacv1.ClusterRole{
		Name:  "node-reader-" + user,
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "watch"}}},
	}
	if err := cl.GetClient().Create(ctx, role); err != nil {
		t.Fatalf("create clusterrole: %v", err)
	}
	binding := &rbacv1.ClusterRoleBinding{
		Name:     role.Name,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
	}
	if err := cl.GetClient().Create(ctx, binding); err != nil {
		t.Fatalf("create clusterrolebinding: %v", err)
	}
	t.Cleanup(func() {
		delCtx, cancelDel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancelDel()
		_ = cl.GetClient().Delete(delCtx, binding)
		_ = cl.GetClient().Delete(delCtx, role)
	})
	waitGranted(t, cl, user, "", "", "nodes")
}

func createWarningEvent(t *testing.T, cl *kccluster.Cluster, namespace, name, pod string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	now := metav1.Now()
	ev := &corev1.Event{
		Name: name, Namespace: namespace,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: namespace, Name: pod, APIVersion: "v1"},
		Type:           corev1.EventTypeWarning,
		Reason:         "BackOff",
		Message:        "Back-off restarting failed container",
		Count:          3,
		FirstTimestamp: now,
		LastTimestamp:  now,
		Source:         corev1.EventSource{Component: "kubelet"},
	}
	if err := cl.GetClient().Create(ctx, ev); err != nil {
		t.Fatalf("create event %s/%s: %v", namespace, name, err)
	}
}

// waitOverview reads the engine until ready accepts its report.
func waitOverview(t *testing.T, e *overviewEngine, what string, ready func(*overviewReport) bool) *overviewReport {
	t.Helper()
	var last *overviewReport
	ok := func() bool {
		r, _ := e.snapshot()
		last = r
		return r != nil && ready(r)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	var b strings.Builder
	if last != nil {
		for _, f := range last.findings {
			b.WriteString("\n  " + f.id + " = " + f.value + " " + f.unavailable)
		}
	}
	t.Fatalf("overview never %s; last report:%s", what, b.String())
	return nil
}

func targetNames(f overviewFinding) []string {
	out := make([]string, 0, len(f.targets))
	for _, tg := range f.targets {
		out = append(out, tg.namespace+"/"+tg.name)
	}
	return out
}

func TestOverviewNamespaceOnlyUser(t *testing.T) {
	user, ns := kctesting.UniqueName("overview-user"), kctesting.UniqueName("ov-team")
	e := newNamespaceUserEnv(t, user, ns)
	grantOverviewReads(t, e.admin, ns, user)
	other := kctesting.UniqueName("ov-other")
	createNamespace(t, e.admin, other) // not granted
	createPod(t, e.admin, ns, "web-1")
	createPod(t, e.admin, other, "hidden-1")
	createWarningEvent(t, e.admin, ns, "web-1.backoff", "web-1")
	createWarningEvent(t, e.admin, other, "hidden-1.backoff", "hidden-1")

	eng := newOverviewEngine(e.deps, ns)
	t.Cleanup(eng.close)
	r := waitOverview(t, eng, "showed the namespace's pod and warning event", func(r *overviewReport) bool {
		ev, _ := r.finding("activity/events-warning")
		return r.info.scopedPods == 1 && len(ev.targets) == 1
	})

	ev, _ := r.finding("activity/events-warning")
	if got := targetNames(ev); got[0] != ns+"/web-1.backoff" {
		t.Fatalf("warning events = %v, want only %s's", got, ns)
	}
	for _, id := range []string{"health/pods-pending", "health/workloads-unavailable", "activity/events-warning"} {
		if f, _ := r.finding(id); f.unavailable != "" {
			t.Errorf("%s unavailable for a user who may read it in %s: %s", id, ns, f.unavailable)
		}
	}
	// Deployments are granted, StatefulSets and DaemonSets are not: the row
	// counts the Deployments and says what it leaves out.
	wl, _ := r.finding("health/workloads-unavailable")
	wantPartial := "statefulsets.apps: not permitted to list and watch in all namespaces or in namespace " + ns +
		"; daemonsets.apps: not permitted to list and watch in all namespaces or in namespace " + ns
	if wl.value != "0 (partial)" || wl.partial != wantPartial {
		t.Errorf("workloads-unavailable = %q, partial %q; want \"0 (partial)\", partial %q", wl.value, wl.partial, wantPartial)
	}
	// Cluster-wide findings say which source is missing; capacity is never
	// summed from the one namespace's pods.
	for id, want := range map[string]string{
		"capacity/cpu":           "nodes: not permitted to list and watch",
		"capacity/pods":          "nodes: not permitted to list and watch",
		"health/nodes-not-ready": "nodes: not permitted to list and watch",
		"health/pvs":             "persistentvolumes: not permitted to list and watch",
		"health/jobs-failed":     "jobs.batch: not permitted to list and watch in all namespaces or in namespace " + ns,
	} {
		f, ok := r.finding(id)
		if !ok || f.unavailable != want || f.value != "n/a" {
			t.Errorf("%s = %q (%q), want n/a because %q", id, f.value, f.unavailable, want)
		}
	}

	// Only the overview's namespace cache runs: no cluster-wide informer
	// retries forbidden lists.
	wantKey := overviewNamespaceKey(e.deps.ClusterKey, ns)
	holds := e.openHolds()
	if len(holds) != 1 || holds[wantKey] != 1 {
		t.Fatalf("open holds = %v, want one on %+v", holds, wantKey)
	}
	if errs := e.errors(); len(errs) > 0 {
		t.Fatalf("reported read errors: %v", errs)
	}
	eng.close()
	kctesting.Eventually(t, 5*time.Second, 50*time.Millisecond, func() bool { return len(e.openHolds()) == 0 },
		"closing the overview left its caches held")
}

func TestOverviewNamespaceOnlyUserAllNamespaces(t *testing.T) {
	user := kctesting.UniqueName("overview-all-user")
	e := newNamespaceUserEnv(t, user, kctesting.UniqueName("ov-all-a"))
	eng := newOverviewEngine(e.deps, AllNamespaces)
	t.Cleanup(eng.close)
	r := waitOverview(t, eng, "computed", func(*overviewReport) bool { return true })
	f, _ := r.finding("health/pods-pending")
	if want := "pods: not permitted to list and watch in all namespaces"; f.unavailable != want {
		t.Fatalf("pods-pending unavailable = %q, want %q", f.unavailable, want)
	}
	if got := e.openHolds(); len(got) != 0 {
		t.Fatalf("open holds = %v, want none: no kind is readable in all namespaces", got)
	}
}

func TestOverviewMixedAccess(t *testing.T) {
	user, ns := kctesting.UniqueName("overview-mixed-user"), kctesting.UniqueName("ov-mixed")
	e := newNamespaceUserEnv(t, user, ns)
	grantNodeReads(t, e.admin, user)
	createPod(t, e.admin, ns, "api-1")

	eng := newOverviewEngine(e.deps, ns)
	t.Cleanup(eng.close)
	r := waitOverview(t, eng, "showed the namespace's pod", func(r *overviewReport) bool { return r.info.scopedPods == 1 })

	// Nodes are readable cluster-wide, pods in the namespace only: the
	// capacity rows must not add up the one namespace's pods.
	want := "pods: readable only in namespace " + ns + "; this needs every one in all namespaces"
	for _, id := range []string{"capacity/cpu", "capacity/memory", "capacity/pods"} {
		if f, _ := r.finding(id); f.unavailable != want {
			t.Errorf("%s unavailable = %q, want %q", id, f.unavailable, want)
		}
	}
	if f, _ := r.finding("health/nodes-not-ready"); f.unavailable != "" {
		t.Errorf("nodes-not-ready unavailable = %q, want it computed from the readable nodes", f.unavailable)
	}
	if f, _ := r.finding("activity/events-warning"); !strings.HasPrefix(f.unavailable, "events: not permitted") {
		t.Errorf("events-warning unavailable = %q, want the missing events grant", f.unavailable)
	}

	holds := e.openHolds()
	for _, k := range []kccluster.Key{overviewClusterKey(e.deps.ClusterKey), overviewNamespaceKey(e.deps.ClusterKey, ns)} {
		if holds[k] != 1 {
			t.Errorf("hold on %+v = %d, want 1 (holds %v)", k, holds[k], holds)
		}
	}
}
