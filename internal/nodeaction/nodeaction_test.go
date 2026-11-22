package nodeaction_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/stokaro/kc/internal/nodeaction"
)

func node(name string, unschedulable bool) *corev1.Node {
	return &corev1.Node{Name: name, Spec: corev1.NodeSpec{Unschedulable: unschedulable}}
}

type podOpt func(*corev1.Pod)

func ownedBy(kind, apiVersion, name string) podOpt {
	return func(p *corev1.Pod) {
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(name + "-uid"), Controller: new(true)}}
	}
}

func withEmptyDir(p *corev1.Pod) {
	p.Spec.Volumes = []corev1.Volume{{Name: "scratch", EmptyDir: &corev1.EmptyDirVolumeSource{}}}
}

func mirror(p *corev1.Pod) {
	p.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "hash"}
}

func phase(ph corev1.PodPhase) podOpt { return func(p *corev1.Pod) { p.Status.Phase = ph } }

func pod(name, nodeName string, opts ...podOpt) *corev1.Pod {
	p := &corev1.Pod{
		Namespace: "app", Name: name, UID: types.UID(name + "-uid"),
		Spec:   corev1.PodSpec{NodeName: nodeName},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func rs(p *corev1.Pod) { ownedBy("ReplicaSet", "apps/v1", "web")(p) }
func ds(p *corev1.Pod) { ownedBy("DaemonSet", "apps/v1", "agent")(p) }

func daemonSet() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{Namespace: "app", Name: "agent"}
}

func newEnv(t *testing.T, funcs interceptor.Funcs, objs ...crclient.Object) nodeaction.Env {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).WithInterceptorFuncs(funcs).Build()
	return nodeaction.Env{
		Client:  c,
		Context: "dev",
		Kubectl: func(args ...string) string { return "kubectl " + strings.Join(args, " ") },
		Pods: func(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
			var list corev1.PodList
			if err := c.List(ctx, &list); err != nil {
				return nil, err
			}
			var out []corev1.Pod
			for _, p := range list.Items {
				if p.Spec.NodeName == nodeName {
					out = append(out, p)
				}
			}
			return out, nil
		},
	}
}

func unschedulable(ctx context.Context, t *testing.T, env nodeaction.Env, name string) bool {
	t.Helper()
	n := &corev1.Node{}
	if err := env.Client.Get(ctx, crclient.ObjectKey{Name: name}, n); err != nil {
		t.Fatalf("get node %s: %v", name, err)
	}
	return n.Spec.Unschedulable
}

func podNames(pods []corev1.Pod) []string {
	var out []string
	for _, p := range pods {
		out = append(out, p.Name)
	}
	return out
}

func noteNames(notes []nodeaction.PodNote) []string {
	var out []string
	for _, n := range notes {
		out = append(out, n.Name)
	}
	return out
}

func TestCordonAndUncordon(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cordon      bool
		nodes       []*corev1.Node
		wantPatches int
		wantTitle   string
		wantCommand string
		wantState   bool
	}{
		{name: "cordon", cordon: true, nodes: []*corev1.Node{node("a", false)}, wantPatches: 1,
			wantTitle: "Cordon node?", wantCommand: "kubectl cordon a", wantState: true},
		{name: "uncordon", cordon: false, nodes: []*corev1.Node{node("a", true)}, wantPatches: 1,
			wantTitle: "Uncordon node?", wantCommand: "kubectl uncordon a", wantState: false},
		{name: "already cordoned is not patched", cordon: true, nodes: []*corev1.Node{node("a", true)}, wantPatches: 0,
			wantTitle: "Nothing to do: already cordoned", wantCommand: "kubectl cordon a", wantState: true},
		{name: "several nodes, one already cordoned", cordon: true, nodes: []*corev1.Node{node("a", false), node("b", true), node("c", false)},
			wantPatches: 2, wantTitle: "Cordon 2 nodes?", wantCommand: "kubectl cordon a c", wantState: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var patches []string
			var mu sync.Mutex
			funcs := interceptor.Funcs{Patch: func(ctx context.Context, c crclient.WithWatch, obj crclient.Object, patch crclient.Patch, opts ...crclient.PatchOption) error {
				data, _ := patch.Data(obj)
				mu.Lock()
				patches = append(patches, string(patch.Type())+" "+string(data))
				mu.Unlock()
				return c.Patch(ctx, obj, patch, opts...)
			}}
			var objs []crclient.Object
			var names []string
			for _, n := range tt.nodes {
				objs = append(objs, n)
				names = append(names, n.Name)
			}
			env := newEnv(t, funcs, objs...)
			ctx := t.Context()

			action, err := nodeaction.PrepareCordon(ctx, env, names, tt.cordon)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if got := action.Confirmation().Title; got != tt.wantTitle {
				t.Errorf("title = %q, want %q", got, tt.wantTitle)
			}
			if got := action.Command(); got != tt.wantCommand {
				t.Errorf("command = %q, want %q", got, tt.wantCommand)
			}
			// Twice: the patch names the value, so a repeat keeps the state.
			for i := range 2 {
				if _, err := action.Run(ctx); err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
				for _, n := range names {
					if got := unschedulable(ctx, t, env, n); got != tt.wantState {
						t.Fatalf("run %d: node %s unschedulable = %v, want %v", i, n, got, tt.wantState)
					}
				}
			}
			if len(patches) != 2*tt.wantPatches {
				t.Fatalf("patches = %v, want %d per run", patches, tt.wantPatches)
			}
			want := `application/merge-patch+json {"spec":{"unschedulable":null}}`
			if tt.cordon {
				want = `application/merge-patch+json {"spec":{"unschedulable":true}}`
			}
			for _, p := range patches {
				if p != want {
					t.Errorf("patch = %s, want %s", p, want)
				}
			}
		})
	}
}

func TestPrepareCordonReportsMissingNode(t *testing.T) {
	t.Parallel()
	env := newEnv(t, interceptor.Funcs{})
	if _, err := nodeaction.PrepareCordon(t.Context(), env, []string{"gone"}, true); !apierrors.IsNotFound(err) {
		t.Fatalf("error = %v, want NotFound", err)
	}
}

func TestSelectPods(t *testing.T) {
	t.Parallel()
	exists := func(ns, name string) (bool, error) { return name == "agent", nil }
	defaults := nodeaction.DrainOptions{GracePeriodSeconds: -1}
	tests := []struct {
		name        string
		pod         *corev1.Pod
		opts        nodeaction.DrainOptions
		dsExists    nodeaction.DaemonSetExists
		want        string // evict, skip, block
		wantWarning bool
		wantMessage string
	}{
		{name: "replicaset pod is evicted", pod: pod("p", "n", rs), opts: defaults, want: "evict"},
		{name: "daemonset pod refused by default", pod: pod("p", "n", ds), opts: defaults, want: "block", wantMessage: "--ignore-daemonsets"},
		{name: "daemonset pod skipped with ignore", pod: pod("p", "n", ds), opts: nodeaction.DrainOptions{IgnoreDaemonSets: true}, want: "skip"},
		{name: "finished daemonset pod is evicted", pod: pod("p", "n", ds, phase(corev1.PodSucceeded)), opts: defaults, want: "evict"},
		{name: "orphaned daemonset pod refused", pod: pod("p", "n", ownedBy("DaemonSet", "apps/v1", "gone")), opts: nodeaction.DrainOptions{IgnoreDaemonSets: true}, want: "block", wantMessage: "not found"},
		{name: "orphaned daemonset pod evicted with force", pod: pod("p", "n", ownedBy("DaemonSet", "apps/v1", "gone")), opts: nodeaction.DrainOptions{Force: true}, want: "evict", wantWarning: true},
		{name: "daemonset lookup error refuses", pod: pod("p", "n", ds), opts: nodeaction.DrainOptions{IgnoreDaemonSets: true},
			dsExists: func(string, string) (bool, error) { return false, errors.New("forbidden") }, want: "block", wantMessage: "forbidden"},
		{name: "other group DaemonSet kind is a controller", pod: pod("p", "n", ownedBy("DaemonSet", "example.com/v1", "x")), opts: defaults, want: "evict"},
		{name: "mirror pod skipped", pod: pod("p", "n", mirror), opts: nodeaction.DrainOptions{Force: true}, want: "skip"},
		{name: "mirror pod skipped without force", pod: pod("p", "n", mirror), opts: defaults, want: "skip"},
		{name: "emptyDir refused by default", pod: pod("p", "n", rs, withEmptyDir), opts: defaults, want: "block", wantMessage: "--delete-emptydir-data"},
		{name: "emptyDir evicted with delete-emptydir-data", pod: pod("p", "n", rs, withEmptyDir), opts: nodeaction.DrainOptions{DeleteEmptyDirData: true}, want: "evict", wantWarning: true},
		{name: "finished emptyDir pod is evicted", pod: pod("p", "n", rs, withEmptyDir, phase(corev1.PodFailed)), opts: defaults, want: "evict"},
		{name: "unmanaged refused by default", pod: pod("p", "n"), opts: defaults, want: "block", wantMessage: "--force"},
		{name: "unmanaged evicted with force", pod: pod("p", "n"), opts: nodeaction.DrainOptions{Force: true}, want: "evict", wantWarning: true},
		{name: "finished unmanaged pod is evicted", pod: pod("p", "n", phase(corev1.PodSucceeded)), opts: defaults, want: "evict"},
		{name: "unmanaged emptyDir needs both flags", pod: pod("p", "n", withEmptyDir), opts: nodeaction.DrainOptions{DeleteEmptyDirData: true}, want: "block", wantMessage: "--force"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dsExists := tt.dsExists
			if dsExists == nil {
				dsExists = exists
			}
			s := nodeaction.SelectPods([]corev1.Pod{*tt.pod}, tt.opts, dsExists)
			got := map[string]int{"evict": len(s.Evict), "skip": len(s.Skipped), "block": len(s.Blocked)}
			for k, v := range got {
				want := 0
				if k == tt.want {
					want = 1
				}
				if v != want {
					t.Fatalf("%s = %d, want %d (selection %+v)", k, v, want, s)
				}
			}
			if gotWarn := len(s.Warnings) > 0; gotWarn != tt.wantWarning {
				t.Errorf("warnings = %v, want warning %v", s.Warnings, tt.wantWarning)
			}
			if tt.wantMessage != "" && !strings.Contains(s.Blocked[0].Message, tt.wantMessage) {
				t.Errorf("message = %q, want it to mention %q", s.Blocked[0].Message, tt.wantMessage)
			}
		})
	}
}

func TestDrainOptionsKubectlArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts nodeaction.DrainOptions
		want string
	}{
		{name: "kubectl defaults", opts: nodeaction.DrainOptions{GracePeriodSeconds: -1}, want: "drain a b"},
		{name: "dialog defaults", opts: nodeaction.DefaultDrainOptions(), want: "drain a b --ignore-daemonsets --timeout=5m0s"},
		{name: "everything", opts: nodeaction.DrainOptions{GracePeriodSeconds: 0, Timeout: 90 * time.Second, IgnoreDaemonSets: true, DeleteEmptyDirData: true, Force: true},
			want: "drain a b --ignore-daemonsets --delete-emptydir-data --force --grace-period=0 --timeout=1m30s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := strings.Join(tt.opts.KubectlArgs([]string{"a", "b"}), " "); got != tt.want {
				t.Errorf("args = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrepareDrainRefusesBeforeCordoning(t *testing.T) {
	t.Parallel()
	env := newEnv(t, interceptor.Funcs{}, node("a", false), pod("loose", "a"), pod("web", "a", rs))
	_, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a"}, nodeaction.DefaultDrainOptions())
	if _, ok := errors.AsType[*nodeaction.RefusedError](err); !ok {
		t.Fatalf("error = %v, want RefusedError", err)
	}
	if !strings.Contains(err.Error(), "app/loose") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("error %q does not name the pod and the flag", err)
	}
	if unschedulable(t.Context(), t, env, "a") {
		t.Errorf("a refused drain must not cordon the node")
	}
}

// pdbRefusal is the 429 the API server returns when an eviction would
// violate a PodDisruptionBudget.
func pdbRefusal(name string) error {
	err := apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)
	err.ErrStatus.Details = &metav1.StatusDetails{Name: name, Kind: "pods", Causes: []metav1.StatusCause{{
		Type:    policyv1.DisruptionBudgetCause,
		Message: "The disruption budget web-pdb needs 2 healthy pods and has 2 currently",
	}}}
	return err
}

// evictions refuses the first refusals evictions of pod name with a PDB
// 429, then evicts. It records the grace period of every request.
type evictions struct {
	name     string
	refusals int32
	calls    atomic.Int32
	mu       sync.Mutex
	graces   []*int64
}

func (e *evictions) funcs() interceptor.Funcs {
	return interceptor.Funcs{SubResourceCreate: func(ctx context.Context, c crclient.Client, sub string, obj crclient.Object, subObj crclient.Object, opts ...crclient.SubResourceCreateOption) error {
		if sub == "eviction" {
			ev := subObj.(*policyv1.Eviction)
			e.mu.Lock()
			if ev.DeleteOptions != nil {
				e.graces = append(e.graces, ev.DeleteOptions.GracePeriodSeconds)
			} else {
				e.graces = append(e.graces, nil)
			}
			e.mu.Unlock()
			if obj.GetName() == e.name {
				if n := e.calls.Add(1); e.refusals < 0 || n <= e.refusals {
					return pdbRefusal(obj.GetName())
				}
			}
		}
		return c.SubResource(sub).Create(ctx, obj, subObj, opts...)
	}}
}

func TestDrainRetriesWhileAPDBRefuses(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ev := &evictions{name: "web-1", refusals: 2}
		env := newEnv(t, ev.funcs(),
			node("a", false), daemonSet(),
			pod("web-1", "a", rs), pod("web-2", "a", rs), pod("agent-x", "a", ds), pod("static", "a", mirror),
			pod("elsewhere", "b", rs),
		)
		ctx := t.Context()
		opts := nodeaction.DefaultDrainOptions()
		opts.GracePeriodSeconds = 10
		action, err := nodeaction.PrepareDrain(ctx, env, []string{"a"}, opts)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if got := podNames(action.Plan()[0].Evict); !slices.Equal(got, []string{"web-1", "web-2"}) {
			t.Fatalf("plan evicts %v, want web-1 and web-2", got)
		}
		if got := noteNames(action.Plan()[0].Skipped); !slices.Equal(got, []string{"agent-x", "static"}) {
			t.Fatalf("plan skips %v, want agent-x and static", got)
		}
		if want := "kubectl drain a --ignore-daemonsets --grace-period=10 --timeout=5m0s"; action.Command() != want {
			t.Errorf("command = %q, want %q", action.Command(), want)
		}

		start := time.Now()
		var reports []nodeaction.Progress
		res, err := action.Run(ctx, func(p nodeaction.Progress) { reports = append(reports, p) })
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if res.Message != "Drained node a: evicted 2 pods" {
			t.Errorf("message = %q", res.Message)
		}
		// Two refusals, each followed by the retry delay.
		if elapsed := time.Since(start); elapsed < 2*nodeaction.DefaultRetryDelay {
			t.Errorf("drain took %v, want at least two retry delays", elapsed)
		}
		if got := ev.calls.Load(); got != 3 {
			t.Errorf("web-1 eviction requests = %d, want 3 (two refused, one accepted)", got)
		}
		for _, g := range ev.graces {
			if g == nil || *g != 10 {
				t.Errorf("eviction grace period = %v, want 10", g)
			}
		}

		sawBlocked := false
		for _, p := range reports {
			for _, b := range p.Nodes[0].Blocked {
				sawBlocked = true
				if b.Name != "web-1" || !strings.Contains(b.Message, "web-pdb") {
					t.Errorf("blocked note = %+v, want web-1 with the budget's message", b)
				}
			}
		}
		if !sawBlocked {
			t.Errorf("no progress report showed the pod blocked by its PDB")
		}
		last := reports[len(reports)-1].Nodes[0]
		if last.Phase != nodeaction.PhaseDrained || last.Evicted != 2 || last.Remaining != 0 || len(last.Blocked) != 0 {
			t.Errorf("last progress = %+v, want drained with 2 evicted", last)
		}

		if !unschedulable(ctx, t, env, "a") {
			t.Errorf("node a is not cordoned")
		}
		left, err := env.Pods(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		if got := podNames(left); !slices.Equal(got, []string{"agent-x", "static"}) {
			t.Errorf("pods left on a = %v, want the DaemonSet and mirror pods", got)
		}
		other, _ := env.Pods(ctx, "b")
		if len(other) != 1 {
			t.Errorf("pods on node b = %v, want untouched", podNames(other))
		}
	})
}

func TestDrainCancel(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ev := &evictions{name: "web-1", refusals: -1} // always refused
		env := newEnv(t, ev.funcs(), node("a", false), node("b", false), pod("web-1", "a", rs), pod("web-9", "b", rs))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		action, err := nodeaction.PrepareDrain(ctx, env, []string{"a", "b"}, nodeaction.DefaultDrainOptions())
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		var last nodeaction.Progress
		start := time.Now()
		_, err = action.Run(ctx, func(p nodeaction.Progress) {
			last = p
			if len(p.Nodes[0].Blocked) > 0 {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed >= nodeaction.DefaultRetryDelay {
			t.Errorf("cancel took %v; it must not wait out the retry delay", elapsed)
		}
		if got := ev.calls.Load(); got != 1 {
			t.Errorf("eviction requests = %d, want 1", got)
		}
		if last.Nodes[0].Phase != nodeaction.PhaseCanceled || last.Nodes[0].Remaining != 1 {
			t.Errorf("node a progress = %+v, want canceled with 1 left", last.Nodes[0])
		}
		if last.Nodes[1].Phase != nodeaction.PhaseWaiting {
			t.Errorf("node b progress = %+v, want not started", last.Nodes[1])
		}
		// Both nodes were cordoned before the first eviction, and stay so.
		for _, n := range []string{"a", "b"} {
			if !unschedulable(t.Context(), t, env, n) {
				t.Errorf("node %s is not cordoned", n)
			}
		}
		if left, _ := env.Pods(t.Context(), "b"); len(left) != 1 {
			t.Errorf("node b pods = %v; a canceled drain must not go on", podNames(left))
		}
	})
}

func TestDrainTimeout(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ev := &evictions{name: "web-1", refusals: -1}
		env := newEnv(t, ev.funcs(), node("a", false), pod("web-1", "a", rs))
		opts := nodeaction.DefaultDrainOptions()
		opts.Timeout = 30 * time.Second
		action, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a"}, opts)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		start := time.Now()
		_, err = action.Run(t.Context(), nil)
		if err == nil || !strings.Contains(err.Error(), "timed out after 30s with 1 pod left") {
			t.Fatalf("error = %v, want a timeout naming the pod left", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Errorf("a timeout is not a cancel: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 30*time.Second {
			t.Errorf("drain gave up after %v, want 30s", elapsed)
		}
	})
}

func TestDrainEvictsPodsThatAppearLater(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var env nodeaction.Env
		var once sync.Once
		funcs := interceptor.Funcs{SubResourceCreate: func(ctx context.Context, c crclient.Client, sub string, obj crclient.Object, subObj crclient.Object, opts ...crclient.SubResourceCreateOption) error {
			// A pod the cache had not shown when the drain started.
			once.Do(func() {
				if err := c.Create(ctx, pod("late", "a", rs)); err != nil {
					t.Errorf("create late pod: %v", err)
				}
			})
			return c.SubResource(sub).Create(ctx, obj, subObj, opts...)
		}}
		env = newEnv(t, funcs, node("a", false), pod("web-1", "a", rs))
		action, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a"}, nodeaction.DefaultDrainOptions())
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		res, err := action.Run(t.Context(), nil)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if res.Message != "Drained node a: evicted 2 pods" {
			t.Errorf("message = %q, want 2 evicted", res.Message)
		}
		if left, _ := env.Pods(t.Context(), "a"); len(left) != 0 {
			t.Errorf("pods left = %v", podNames(left))
		}
	})
}

func TestDrainConfirmation(t *testing.T) {
	t.Parallel()
	opts := nodeaction.DefaultDrainOptions()
	opts.Force = true
	env := newEnv(t, interceptor.Funcs{}, node("a", false), node("b", false), daemonSet(),
		pod("web-1", "a", rs), pod("loose", "b"), pod("agent-x", "b", ds))
	action, err := nodeaction.PrepareDrain(t.Context(), env, []string{"a", "b"}, opts)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	c := action.Confirmation()
	if c.Title != "Drain 2 nodes?" || c.Action != "Drain" {
		t.Errorf("title, action = %q, %q", c.Title, c.Action)
	}
	notes := strings.Join(c.Notes, "\n")
	for _, want := range []string{"evicts 2 pods", "PodDisruptionBudgets", "1 DaemonSet or mirror pod", "! app/loose\n  deleting Pods that declare no controller"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if !slices.Contains(c.Details, [2]string{"Nodes", "a, b"}) || !slices.Contains(c.Details, [2]string{"Context", "dev"}) {
		t.Errorf("details = %v", c.Details)
	}
}

// A cancel that lands while the drain reads a DaemonSet ends the drain as
// canceled, not failed: the lookup error is a result of the cancel.
func TestDrainCancelDuringDaemonSetLookup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var armed atomic.Bool
	funcs := interceptor.Funcs{
		Get: func(ctx context.Context, c crclient.WithWatch, key crclient.ObjectKey, obj crclient.Object, opts ...crclient.GetOption) error {
			if u, ok := obj.(*unstructured.Unstructured); ok && u.GetKind() == "DaemonSet" && armed.Load() {
				cancel()
				return ctx.Err()
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}
	env := newEnv(t, funcs, node("a", false), daemonSet(), pod("agent-1", "a", ds), pod("web-1", "a", rs))
	action, err := nodeaction.PrepareDrain(ctx, env, []string{"a"}, nodeaction.DefaultDrainOptions())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	armed.Store(true)
	var last nodeaction.Progress
	_, err = action.Run(ctx, func(p nodeaction.Progress) { last = p })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if last.Nodes[0].Phase != nodeaction.PhaseCanceled {
		t.Fatalf("node a progress = %+v, want canceled", last.Nodes[0])
	}
}
