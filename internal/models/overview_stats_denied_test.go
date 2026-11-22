package models

// White-box like overview_measured_test.go: the engine's stats rounds and
// its fetcher are unexported.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestOverviewEngineStopsStatsWhenForbidden: once every node answers
// forbidden for nodes/proxy, the engine stops asking, so an identity
// without that grant does not send a 403 per node every minute. The reason
// stays in the report.
func TestOverviewEngineStopsStatsWhenForbidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		e, _ := newTestOverviewEngine(t, Deps{Ctx: ctx}, AllNamespaces, readyNode("n1", "4", "8Gi", 10), readyNode("n2", "4", "8Gi", 10))
		var calls atomic.Int32
		e.statsFetch = func() (statsFetcher, error) {
			return func(_ context.Context, node string) (*nodeStats, error) {
				calls.Add(1)
				return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes/proxy"}, node, errors.New("no"))
			}, nil
		}
		defer e.close()

		deadline := time.Now().Add(3*overviewStatsEvery + time.Second)
		var r *overviewReport
		for time.Now().Before(deadline) {
			r, _ = e.snapshot()
			time.Sleep(time.Second)
			synctest.Wait()
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("stats requests in three minutes = %d, want 2 (one round over two nodes)", got)
		}
		disk := mustFinding(t, r, "capacity/disk")
		if disk.unavailable == "" {
			t.Fatalf("disk finding = %+v, want the forbidden reason", disk)
		}
	})
}

func TestPollNodeStatsDenied(t *testing.T) {
	t.Parallel()
	forbidden := func(_ context.Context, node string) (*nodeStats, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes/proxy"}, node, errors.New("no"))
	}
	if res := pollNodeStats(t.Context(), forbidden, []string{"a", "b"}, 10); !res.denied {
		t.Fatalf("all forbidden: denied = false, reason %q", res.reason)
	}
	mixed := func(ctx context.Context, node string) (*nodeStats, error) {
		if node == "a" {
			return forbidden(ctx, node)
		}
		return nil, errors.New("dial tcp: timeout")
	}
	if res := pollNodeStats(t.Context(), mixed, []string{"a", "b"}, 10); res.denied {
		t.Fatal("one forbidden node and one timeout must not stop the stats")
	}
}
