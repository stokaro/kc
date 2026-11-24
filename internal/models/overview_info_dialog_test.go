package models

// White-box like overview_stats_denied_test.go: the engine's recomputes and
// its stats fetcher are unexported, and the dialog pause is a property of
// the engine loop, not of the summary it returns.

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

// TestInfoEngineRecomputesBehindADialog: the Info panel stays in view
// beside a dialog, so its engine recomputes from the cache while one is
// open, and the first kubelet stats round is not held back. /overview
// keeps waiting for the dialog to close.
func TestInfoEngineRecomputesBehindADialog(t *testing.T) {
	for _, tc := range []struct {
		name          string
		info          bool
		wantComputes  int
		wantDiskKnown bool
	}{
		{name: "info", info: true, wantComputes: 3, wantDiskKnown: true},
		{name: "overview", info: false, wantComputes: 1, wantDiskKnown: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				activity := &Activity{}
				e, conn := newTestOverviewEngine(t, Deps{Activity: activity}, AllNamespaces, readyNode("n1", "4", "8Gi", 10))
				e.computeBehindDialogs = tc.info
				e.statsFetch = func() (statsFetcher, error) {
					return func(context.Context, string) (*nodeStats, error) {
						return &nodeStats{nodeFS: fsUse{used: 40 << 30, capacity: 100 << 30}}, nil
					}, nil
				}
				defer e.close()
				_, _ = e.snapshot()
				synctest.Wait()
				// A dialog opens before the first stats round is due.
				activity.Pause()
				time.Sleep(3 * time.Second)
				synctest.Wait()
				conn.fire()
				time.Sleep(3 * time.Second)
				synctest.Wait()
				r, _ := e.snapshot()
				if got := e.computeCount(); got != tc.wantComputes {
					t.Errorf("recomputes with a dialog open = %d, want %d", got, tc.wantComputes)
				}
				disk := mustFinding(t, r, "capacity/disk")
				if known := disk.unavailable == ""; known != tc.wantDiskKnown {
					t.Errorf("disk known = %v (unavailable %q), want %v", known, disk.unavailable, tc.wantDiskKnown)
				}
			})
		})
	}
}

// TestInfoTopLeavesOutPodsBeingDeleted: right after a drain the evicted
// pods still run out their grace period on the node. They are on their
// way out, so the node's top list leaves them out; the pod count and the
// capacity still hold them until they end.
func TestInfoTopLeavesOutPodsBeingDeleted(t *testing.T) {
	t.Parallel()
	evicted := onNode(withPod(overviewRunningPod("shop", "web"), func(p *corev1.Pod) {
		p.DeletionTimestamp = &metav1.Time{Time: overviewTestNow}
		p.Finalizers = []string{"test/keep"} // the fake client keeps no deleted object without one
	}), "n1")
	objs := []crclient.Object{readyNode("n1", "4", "8Gi", 100), evicted, onNode(overviewRunningPod("kube-system", "proxy"), "n1")}
	podUse := map[string][]containerUsage{
		"shop/web":          {{name: "app", cpu: 100, mem: 900 << 20}},
		"kube-system/proxy": {{name: "app", cpu: 10, mem: 20 << 20}},
	}
	m := &overviewMeasured{usageAt: overviewTestNow, pods: podUse, clusterPods: podUse}
	s := infoSummary(measuredReport(t, AllNamespaces, m, objs...), nil, appconfig.DefaultOverview(), "n1")
	if len(s.Top) != 1 || s.Top[0].Name != "proxy" {
		t.Fatalf("top = %+v, want only the pod that stays", s.Top)
	}
	if pods := infoBar(t, s, "Pods"); pods.Detail != "2/100" {
		t.Fatalf("pods = %+v, want the terminating pod counted", pods)
	}
}
