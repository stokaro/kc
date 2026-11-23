package models

import (
	"context"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// TestOverviewEngineOnARealCache runs the engine on a kc cluster's cache
// against envtest: typed informers sync, a new object reaches the report
// through the informer handler, and leaving the overview removes the
// handlers.
func TestOverviewEngineOnARealCache(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	c1, c2, open := kctesting.UniqueName("overview-cordoned-1"), kctesting.UniqueName("overview-cordoned-2"), kctesting.UniqueName("overview-open")
	// t.Context() is canceled before cleanups run.
	cleanupCtx := context.WithoutCancel(ctx)
	cordoned := func(name string) *corev1.Node {
		return &corev1.Node{Name: name, Spec: corev1.NodeSpec{Unschedulable: true}}
	}
	for _, n := range []*corev1.Node{cordoned(c1), {Name: open}} {
		if err := cli.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cli.Delete(cleanupCtx, n) })
	}

	e := newOverviewEngine(Deps{Cl: cl, Ctx: ctx}, AllNamespaces)
	e.idleAfter = 3 * time.Second
	defer e.close()

	waitFor := func(what string, cond func(overviewFinding) bool) overviewFinding {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		var last overviewFinding
		for time.Now().Before(deadline) {
			r, err := e.snapshot()
			if err != nil {
				t.Fatalf("engine: %v", err)
			}
			if f, ok := r.finding("health/nodes-cordoned"); ok {
				last = f
				if cond(f) {
					return f
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("waited 30s for %s: last nodes-cordoned finding value=%q unavailable=%q targets=%v", what, last.value, last.unavailable, targetKeys(last))
		return last
	}
	has := func(names ...string) func(overviewFinding) bool {
		return func(f overviewFinding) bool {
			got := targetKeys(f)
			for _, n := range names {
				if !slices.Contains(got, "nodes:"+n) {
					return false
				}
			}
			return f.unavailable == ""
		}
	}
	f := waitFor("the first cordoned node", has(c1))
	if slices.Contains(targetKeys(f), "nodes:"+open) {
		t.Fatalf("a schedulable node is listed as cordoned: %v", targetKeys(f))
	}

	before := e.computeCount()
	second := cordoned(c2)
	if err := cli.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Delete(cleanupCtx, second) })
	waitFor("the node created after start", has(c1, c2))
	if e.computeCount() <= before {
		t.Fatal("the report changed without a recompute")
	}

	// Stop reading: the engine stops after idleAfter.
	deadline := time.Now().Add(15 * time.Second)
	for e.isRunning() && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if e.isRunning() {
		t.Fatalf("engine still runs %v after the last read", e.idleAfter)
	}
}
