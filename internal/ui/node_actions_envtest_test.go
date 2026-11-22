package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/nodeaction"
)

func createEnvtestObject(t *testing.T, cl *kccluster.Cluster, obj crclient.Object) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := cl.GetClient().Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
	}
	t.Cleanup(func() {
		// t.Context() is canceled before cleanup functions run.
		delCtx, cancelDel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancelDel()
		_ = cl.GetClient().Delete(delCtx, obj, crclient.GracePeriodSeconds(0))
	})
}

func replicaSetPod(ns, name, nodeName string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		Namespace: ns, Name: name, Labels: labels,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "rs-uid", Controller: new(true)}},
		Spec:            corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
	}
}

// liveNode reads a node from the API server, not the cache.
func liveNode(t *testing.T, cl *kccluster.Cluster, name string) *corev1.Node {
	t.Helper()
	n := &corev1.Node{}
	if err := cl.GetAPIReader().Get(t.Context(), crclient.ObjectKey{Name: name}, n); err != nil {
		t.Fatalf("get node %s: %v", name, err)
	}
	return n
}

// TestNodeActionsEnvtest drives the node actions from a node list with two
// marked nodes against a real API server: the target, the cordon patch, an
// eviction that the server accepts, and one that a PodDisruptionBudget
// refuses with 429 until the drain is canceled.
func TestNodeActionsEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	const ns = "node-actions"
	t.Cleanup(createNamespace(t, cl, ns))
	createEnvtestObject(t, cl, &corev1.Node{Name: "drain-a"})
	createEnvtestObject(t, cl, &corev1.Node{Name: "drain-b"})
	createEnvtestObject(t, cl, replicaSetPod(ns, "web-1", "drain-a", nil))
	db := replicaSetPod(ns, "db-1", "drain-b", map[string]string{"app": "db"})
	createEnvtestObject(t, cl, db)
	// The eviction API ignores budgets for pending pods, and evicts a pod
	// that is not ready while its budget is healthy.
	db.Status.Phase = corev1.PodRunning
	db.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := cl.GetClient().Status().Update(t.Context(), db); err != nil {
		t.Fatalf("mark db-1 running: %v", err)
	}
	pdb := &policyv1.PodDisruptionBudget{
		Namespace: ns, Name: "db",
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: new(intstr.FromInt32(1)),
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
		},
	}
	createEnvtestObject(t, cl, pdb)
	// envtest runs no disruption controller. Write the status it would
	// write; an unprocessed budget makes the server hold the eviction
	// request instead of answering 429.
	pdb.Status = policyv1.PodDisruptionBudgetStatus{
		ObservedGeneration: pdb.Generation,
		CurrentHealthy:     1,
		DesiredHealthy:     1,
		ExpectedPods:       1,
	}
	if err := cl.GetClient().Status().Update(t.Context(), pdb); err != nil {
		t.Fatalf("write budget status: %v", err)
	}

	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns) // sets up the panels' navigation
	app.startupIntent = StartupIntent{
		Verb: KubectlVerbGet,
		Get:  &GetIntent{Tokens: []GetToken{{Value: "nodes", ExplicitResource: true}, {Value: "drain-a"}, {Value: "drain-b"}}},
	}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())
	if got := selectedItemNames(app.leftPanel.GetSelectedItems()); !equalStringSets(got, []string{"drain-a", "drain-b"}) {
		t.Fatalf("selected = %v, want both nodes (path %q)", got, app.leftPanel.GetCurrentPath())
	}

	target, reason, ok := app.nodeTargetFor(app.leftPanel)
	if !ok || reason != "" {
		t.Fatalf("node target: ok=%v reason=%q", ok, reason)
	}
	slices.Sort(target.nodes)
	if !slices.Equal(target.nodes, []string{"drain-a", "drain-b"}) {
		t.Fatalf("target nodes = %v", target.nodes)
	}

	// M-n opens the menu on the node list.
	app.activePanel = 0
	if _, cmd := app.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Mod: tea.ModAlt})); cmd != nil {
		_ = cmd
	}
	if m := app.modalManager.GetActiveModal(); m == nil || !app.modalManager.IsModalVisible() {
		t.Fatalf("M-n did not open the node actions menu")
	} else if _, isMenu := m.content.(*nodeActionMenuModel); !isMenu {
		t.Fatalf("active modal content = %T, want the node actions menu", m.content)
	}
	app.hideModalName(nodeMenuModal)

	ctx := t.Context()
	cordon, err := nodeaction.PrepareCordon(ctx, target.env, target.nodes, true)
	if err != nil {
		t.Fatalf("prepare cordon: %v", err)
	}
	if _, err := cordon.Run(ctx); err != nil {
		t.Fatalf("cordon: %v", err)
	}
	for _, n := range target.nodes {
		if !liveNode(t, cl, n).Spec.Unschedulable {
			t.Fatalf("node %s not cordoned", n)
		}
	}

	opts := nodeaction.DefaultDrainOptions()
	opts.GracePeriodSeconds = 0 // no kubelet runs here to finish a graceful stop
	drainA, err := nodeaction.PrepareDrain(ctx, target.env, []string{"drain-a"}, opts)
	if err != nil {
		t.Fatalf("prepare drain a: %v", err)
	}
	drainA.PollInterval = 50 * time.Millisecond
	if _, err := drainA.Run(ctx, nil); err != nil {
		t.Fatalf("drain a: %v", err)
	}
	if err := cl.GetAPIReader().Get(ctx, crclient.ObjectKey{Namespace: ns, Name: "web-1"}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("web-1 after drain: %v, want NotFound", err)
	}

	drainB, err := nodeaction.PrepareDrain(ctx, target.env, []string{"drain-b"}, opts)
	if err != nil {
		t.Fatalf("prepare drain b: %v", err)
	}
	drainB.RetryDelay = 50 * time.Millisecond
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var blocked []nodeaction.PodNote
	_, err = drainB.Run(runCtx, func(p nodeaction.Progress) {
		if b := p.Nodes[0].Blocked; len(b) > 0 && blocked == nil {
			blocked = b
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("drain b error = %v, want canceled after the budget refused", err)
	}
	if len(blocked) != 1 || blocked[0].Name != "db-1" || !strings.Contains(blocked[0].Message, "disruption budget") {
		t.Fatalf("blocked = %+v, want db-1 refused by its disruption budget", blocked)
	}
	if err := cl.GetAPIReader().Get(ctx, crclient.ObjectKey{Namespace: ns, Name: "db-1"}, &corev1.Pod{}); err != nil {
		t.Fatalf("db-1 must survive a refused eviction: %v", err)
	}
}
