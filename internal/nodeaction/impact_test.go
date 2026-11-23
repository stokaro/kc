package nodeaction_test

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/stokaro/kc/internal/nodeaction"
)

func labeled(kv ...string) podOpt {
	return func(p *corev1.Pod) {
		p.Labels = make(map[string]string)
		for i := 0; i+1 < len(kv); i += 2 {
			p.Labels[kv[i]] = kv[i+1]
		}
	}
}

func ready(ok bool) podOpt {
	return func(p *corev1.Pod) {
		s := corev1.ConditionFalse
		if ok {
			s = corev1.ConditionTrue
		}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: s}}
	}
}

func inNamespace(ns string) podOpt { return func(p *corev1.Pod) { p.Namespace = ns } }

type pdbOpt func(*policyv1.PodDisruptionBudget)

func budget(name string, selector map[string]string, allowed, healthy, desired int32, opts ...pdbOpt) *policyv1.PodDisruptionBudget {
	b := &policyv1.PodDisruptionBudget{
		Namespace: "app", Name: name, Generation: 1,
		Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, DisruptionsAllowed: allowed,
			CurrentHealthy: healthy, DesiredHealthy: desired, ExpectedPods: healthy},
	}
	if selector != nil {
		b.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// withBudgets lists budgets from the fake client and refuses the
// namespaces in denied with a Forbidden error, as the scoped lister does.
func withBudgets(env nodeaction.Env, denied ...string) nodeaction.Env {
	env.Budgets = func(ctx context.Context, ns string) ([]policyv1.PodDisruptionBudget, error) {
		if slices.Contains(denied, ns) {
			return nil, apierrors.NewForbidden(schema.GroupResource{Group: "policy", Resource: "poddisruptionbudgets"}, "", nil)
		}
		var list policyv1.PodDisruptionBudgetList
		if err := env.Client.List(ctx, &list, crclient.InNamespace(ns)); err != nil {
			return nil, err
		}
		return list.Items, nil
	}
	return env
}

func budgetByName(im nodeaction.Impact, name string) (nodeaction.BudgetImpact, bool) {
	for _, b := range im.Budgets {
		if b.Name == name {
			return b, true
		}
	}
	return nodeaction.BudgetImpact{}, false
}

func TestDrainImpact(t *testing.T) {
	t.Parallel()
	web := []podOpt{rs, labeled("app", "web")}
	env := withBudgets(newEnv(t, interceptor.Funcs{},
		node("a", false),
		pod("web-1", "a", append(web, ready(true))...),
		pod("web-2", "a", append(web, ready(true))...),
		pod("web-3", "a", append(web, ready(false))...),
		pod("web-new", "a", append(web, phase(corev1.PodPending))...),
		pod("db-0", "a", ownedBy("StatefulSet", "apps/v1", "db"), labeled("app", "db"), ready(true)),
		pod("cache-0", "a", rs, labeled("app", "cache"), ready(true)),
		pod("x", "a", rs, inNamespace("locked"), ready(true)),
		budget("web-pdb", map[string]string{"app": "web"}, 1, 3, 2),
		budget("db-pdb", map[string]string{"app": "db"}, 0, 1, 1),
		budget("nil-selector", nil, 5, 5, 0),
		budget("empty-selector", make(map[string]string), 5, 5, 0),
	), "locked")

	action, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a"}, nodeaction.DefaultDrainOptions())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	im := action.Impact()
	if !im.Read {
		t.Fatal("impact not read although the env has a budget lister")
	}
	var names []string
	for _, b := range im.Budgets {
		names = append(names, b.Name)
	}
	// The Eviction API ignores budgets with a missing or empty selector.
	if !slices.Equal(names, []string{"db-pdb", "web-pdb"}) {
		t.Fatalf("budgets = %v, want db-pdb then web-pdb (equal waits sort by name)", names)
	}
	// web-pdb covers three running pods (one not ready) and allows 1; the
	// budget is met, so the unready pod is free and one healthy pod waits.
	wb, _ := budgetByName(im, "web-pdb")
	if len(wb.Pods) != 3 || wb.NotReady != 1 || wb.Waiting() != 1 {
		t.Errorf("web-pdb = %+v waiting %d, want 3 pods, 1 not ready, 1 waiting", wb, wb.Waiting())
	}
	if db, _ := budgetByName(im, "db-pdb"); db.Waiting() != 1 {
		t.Errorf("db-pdb waiting %d, want 1", db.Waiting())
	}
	if im.Bypass != 1 {
		t.Errorf("bypass = %d, want the pending pod", im.Bypass)
	}
	if im.Uncovered != 1 {
		t.Errorf("uncovered = %d, want cache-0 only; the pod of the unreadable namespace is unknown", im.Uncovered)
	}
	if len(im.Unreadable) != 1 || im.Unreadable[0].Namespace != "locked" {
		t.Errorf("unreadable = %+v, want namespace locked", im.Unreadable)
	}
	if len(im.Workloads) != 3 || im.Workloads[0].Pods != 5 || im.Workloads[0].Kind != "ReplicaSet" {
		t.Errorf("workloads = %+v, want the ReplicaSet with 5 pods first", im.Workloads)
	}

	notes := strings.Join(action.Confirmation().Notes, "\n")
	for _, want := range []string{
		"Workloads:",
		"ReplicaSet app/web: 5 pods",
		"PodDisruptionBudgets, as read now:",
		"app/web-pdb allows 1 now (healthy 3, desired 2, expected 3), covers 3 planned pods, 1 not ready (IfHealthyBudget)",
		"1 eviction wait until replacements are healthy",
		"No budget covers 1 planned pod.",
		"1 pending, finished or terminating pod bypass budgets.",
		"! namespace locked",
		"Eviction API, which respects PodDisruptionBudgets.",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("confirmation notes miss %q:\n%s", want, notes)
		}
	}
}

func TestDrainImpactUnhealthyPolicy(t *testing.T) {
	t.Parallel()
	always := policyv1.AlwaysAllow
	alwaysAllow := func(b *policyv1.PodDisruptionBudget) { b.Spec.UnhealthyPodEvictionPolicy = &always }
	sel := map[string]string{"app": "web"}
	for _, tc := range []struct {
		name    string
		budget  *policyv1.PodDisruptionBudget
		waiting int
	}{
		{"if healthy budget, met: the unready pod is free", budget("p", sel, 0, 2, 2), 1},
		{"if healthy budget, not met: the unready pod needs the allowance", budget("p", sel, 0, 1, 2), 2},
		{"always allow: the unready pod is free", budget("p", sel, 0, 1, 2, alwaysAllow), 1},
		{"stale status: every pod that needs the budget waits", budget("p", sel, 5, 2, 2, func(b *policyv1.PodDisruptionBudget) { b.Generation = 2 }), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := withBudgets(newEnv(t, interceptor.Funcs{}, node("a", false), tc.budget,
				pod("a-1", "a", rs, labeled("app", "web"), ready(true)),
				pod("a-2", "a", rs, labeled("app", "web"), ready(false)),
			))
			action, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a"}, nodeaction.DefaultDrainOptions())
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			b, ok := budgetByName(action.Impact(), "p")
			if !ok || b.Waiting() != tc.waiting {
				t.Errorf("budget %+v waiting %d, want %d", b, b.Waiting(), tc.waiting)
			}
			if tc.budget.Generation == 2 && !strings.Contains(b.String(), "status is stale") {
				t.Errorf("stale budget reads %q", b.String())
			}
		})
	}
}

func TestDrainImpactMultipleBudgets(t *testing.T) {
	t.Parallel()
	env := withBudgets(newEnv(t, interceptor.Funcs{}, node("a", false),
		pod("web-1", "a", rs, labeled("app", "web", "tier", "front"), ready(true)),
		budget("by-app", map[string]string{"app": "web"}, 1, 1, 0),
		budget("by-tier", map[string]string{"tier": "front"}, 1, 1, 0),
	))
	action, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a"}, nodeaction.DefaultDrainOptions())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	im := action.Impact()
	if len(im.MultiBudget) != 1 || !strings.Contains(im.MultiBudget[0].Message, "by-app, by-tier") {
		t.Fatalf("multi-budget pods = %+v", im.MultiBudget)
	}
	if notes := strings.Join(action.Confirmation().Notes, "\n"); !strings.Contains(notes, "! app/web-1") {
		t.Errorf("confirmation does not warn about the pod:\n%s", notes)
	}
}

func TestDrainImpactWithoutLister(t *testing.T) {
	t.Parallel()
	env := newEnv(t, interceptor.Funcs{}, node("a", false), pod("web-1", "a", rs), budget("p", nil, 0, 0, 0))
	action, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a"}, nodeaction.DefaultDrainOptions())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	im := action.Impact()
	if im.Read || len(im.Budgets) != 0 || im.Uncovered != 0 {
		t.Errorf("impact without a lister = %+v, want nothing read", im)
	}
	if notes := strings.Join(action.Confirmation().Notes, "\n"); strings.Contains(notes, "No budget covers") {
		t.Errorf("confirmation claims no budget without reading any:\n%s", notes)
	}
}

// A 429 is a budget refusal only with the DisruptionBudget cause. Both
// keep the API's message; the budget refusal carries the planned budget
// as evidence.
func TestDrainRefusalNotes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		refusal      error
		wantMessage  string
		wantEvidence string
	}{
		{"budget", pdbRefusal("web-1"), "The disruption budget web-pdb needs 2 healthy pods", "when planned: app/web-pdb allows 0 now"},
		{"load shedding", apierrors.NewTooManyRequests("too many requests, please try again later", 1),
			"429 without a disruption budget cause: too many requests, please try again later", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				funcs := interceptor.Funcs{SubResourceCreate: func(ctx context.Context, c crclient.Client, sub string, obj crclient.Object, subObj crclient.Object, opts ...crclient.SubResourceCreateOption) error {
					if sub == "eviction" && calls.Add(1) == 1 {
						return tc.refusal
					}
					return c.SubResource(sub).Create(ctx, obj, subObj, opts...)
				}}
				env := withBudgets(newEnv(t, funcs, node("a", false),
					pod("web-1", "a", rs, labeled("app", "web"), ready(true)),
					budget("web-pdb", map[string]string{"app": "web"}, 0, 2, 2),
				))
				action, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a"}, nodeaction.DefaultDrainOptions())
				if err != nil {
					t.Fatalf("prepare: %v", err)
				}
				var notes []nodeaction.PodNote
				if _, err := action.Run(t.Context(), func(p nodeaction.Progress) { notes = append(notes, p.Nodes[0].Blocked...) }); err != nil {
					t.Fatalf("run: %v", err)
				}
				if len(notes) == 0 {
					t.Fatal("no progress report showed the refused pod")
				}
				n := notes[0]
				if !strings.Contains(n.Message, tc.wantMessage) {
					t.Errorf("message = %q, want %q", n.Message, tc.wantMessage)
				}
				if tc.wantEvidence == "" && n.Evidence != "" || !strings.Contains(n.Evidence, tc.wantEvidence) {
					t.Errorf("evidence = %q, want %q", n.Evidence, tc.wantEvidence)
				}
			})
		})
	}
}
