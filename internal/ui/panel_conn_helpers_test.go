package ui

import (
	"testing"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// useTestCluster gives panels idxs (both when none are named) a connection
// to kctx served by cl, a cluster the test runs itself; cl may be nil for
// a context without a cluster. It returns the connection of the last
// panel.
func useTestCluster(app *App, kctx *kubeconfig.Context, cl *kccluster.Cluster, idxs ...int) *panelConn {
	if len(idxs) == 0 {
		idxs = []int{0, 1}
	}
	var c *panelConn
	for _, idx := range idxs {
		c = app.connFor(kctx, app.clusterKeyFor(kctx), cl, nil)
		app.setPanelConn(idx, c)
	}
	return c
}

// usePoolContext connects panels idxs (both when none are named) to kctx
// through app.clPool as kc's startup does: one lease per panel. The
// connections are released when the test ends.
func usePoolContext(t *testing.T, app *App, kctx *kubeconfig.Context, idxs ...int) *panelConn {
	t.Helper()
	if len(idxs) == 0 {
		idxs = []int{0, 1}
	}
	key := app.clusterKeyFor(kctx)
	var c *panelConn
	for _, idx := range idxs {
		lease, err := app.clPool.Acquire(t.Context(), key, nil)
		if err != nil {
			t.Fatalf("acquire %s for the %s panel: %v", kctx.Name, panelName(idx), err)
		}
		c = app.newPanelConn(kctx, key, lease)
		app.setPanelConn(idx, c)
	}
	t.Cleanup(app.releasePanelConns)
	return c
}
