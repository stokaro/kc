package models

// White-box like usage_metrics_test.go: the poller and its fetch hook are
// unexported.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// TestUsagePollerBacksOffWhenForbidden: a PodMetrics list the user may not
// read is asked again after usageForbiddenInterval, not every interval,
// while a reader keeps reading. The snapshot keeps the error, so the cells
// and the Info text still say why.
func TestUsagePollerBacksOffWhenForbidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var polls atomic.Int32
		m := &UsageMetrics{interval: usagePollInterval, now: time.Now, fetch: func(context.Context, Deps, usageKind, string, bool) *usageSnapshot {
			polls.Add(1)
			err := apierrors.NewForbidden(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "", errors.New("no"))
			return &usageSnapshot{served: true, err: err}
		}}
		deps := Deps{Ctx: ctx, ClusterKey: kccluster.Key{ContextName: "c"}}
		watcher := new(int)
		read := func() *usageSnapshot {
			snap, _ := m.metricsSnapshot(deps, usagePods, AllNamespaces, watcher, nil)
			return snap
		}
		read()
		synctest.Wait()
		for elapsed := time.Duration(0); elapsed < usageForbiddenInterval-usagePollInterval; elapsed += usagePollInterval {
			time.Sleep(usagePollInterval)
			synctest.Wait()
			if snap := read(); snap == nil || !apierrors.IsForbidden(snap.err) {
				t.Fatalf("snapshot = %+v, want the forbidden error", snap)
			}
		}
		if got := polls.Load(); got != 1 {
			t.Fatalf("polls before the back-off ends = %d, want 1", got)
		}
		time.Sleep(2 * usagePollInterval)
		synctest.Wait()
		if got := polls.Load(); got != 2 {
			t.Fatalf("polls after the back-off = %d, want 2", got)
		}
	})
}
