package models

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

// TestOverviewClusterScopesExist keeps the list of cluster-wide findings in
// step with the rules: every listed ID is a finding, so a renamed rule
// cannot silently lose its cluster label.
func TestOverviewClusterScopesExist(t *testing.T) {
	t.Parallel()
	r := measuredReport(t, "a", nil, readyNode("n1", "4", "8Gi", 110))
	ids := make(map[string]bool, len(r.findings))
	for _, f := range r.findings {
		ids[f.id] = true
	}
	for id := range overviewClusterScopes {
		if !ids[id] {
			t.Errorf("cluster scope listed for %q, which is no finding", id)
		}
	}
	if len(overviewClusterScopes) == len(r.findings) {
		t.Fatal("every finding is cluster-wide; the namespace label would never show")
	}
}

// TestOverviewFolderScopeLabels opens the overview of an empty namespace
// while pods run elsewhere: capacity is labeled cluster-wide, the namespace
// sections name the namespace, node and volume checks in them say cluster,
// and F3 reports each finding's own scope.
func TestOverviewFolderScopeLabels(t *testing.T) {
	objs := []crclient.Object{readyNode("n1", "4", "8Gi", 110), onNode(overviewRunningPod("a", "web"), "n1")}
	tests := []struct {
		namespace string
		headers   map[string]string // section row: its label
		titles    map[string]string // finding row: its title cell
		f3        map[string]string // finding: its F3 scope line
	}{
		{
			namespace: "empty",
			headers: map[string]string{
				"section/capacity": " Capacity · cluster", "section/health": " Health · namespace empty",
				"section/risks": " Risks · namespace empty", "section/activity": " Activity · namespace empty",
			},
			titles: map[string]string{
				"capacity/cpu": "/nodes cpu", "capacity/pods": "/nodes pods",
				"health/nodes-not-ready": "/nodes not ready (cluster)", "health/pvs": "/pvs released or failed (cluster)",
				"health/pods-not-ready": "/pods not ready", "health/pvcs": "/pvcs pending or lost",
			},
			f3: map[string]string{
				"capacity/cpu":          "Scope: cluster: every node and every pod on it, in all namespaces, whatever the namespace scope (namespace empty)",
				"capacity/memory":       "Scope: cluster: every node and every pod on it, in all namespaces, whatever the namespace scope (namespace empty)",
				"capacity/pods":         "Scope: cluster: every node and every pod on it, in all namespaces, whatever the namespace scope (namespace empty)",
				"capacity/disk":         "Scope: cluster: every node, whatever the namespace scope (namespace empty)",
				"health/pods-not-ready": "Scope: namespace empty\n",
			},
		},
		{
			namespace: AllNamespaces,
			headers:   map[string]string{"section/capacity": " Capacity · cluster", "section/health": " Health · all namespaces"},
			titles:    map[string]string{"health/nodes-not-ready": "/nodes not ready (cluster)", "health/pods-not-ready": "/pods not ready"},
			f3: map[string]string{
				"capacity/cpu":          "Scope: cluster: every node and every pod on it, in all namespaces\n",
				"health/pods-not-ready": "Scope: all namespaces\n",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.namespace, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				deps := Deps{CtxName: "prod"}
				e, _ := newTestOverviewEngine(t, deps, tc.namespace, objs...)
				e.now = func() time.Time { return overviewTestNow }
				folder := newOverviewFolder(deps, tc.namespace, []string{"overview"}, e)
				defer folder.Close()
				ctx := t.Context()
				_ = folder.Lines(ctx, 0, 100)
				synctest.Wait()
				rows := folder.Lines(ctx, 0, 100)
				for id, want := range tc.headers {
					if got := rowCells(t, rows, id)[0]; got != want {
						t.Errorf("%s = %q, want %q", id, got, want)
					}
				}
				for id, want := range tc.titles {
					if got := rowCells(t, rows, id)[0]; got != want {
						t.Errorf("%s title = %q, want %q", id, got, want)
					}
				}
				// Capacity counts the pod in a while the namespace has none.
				if got := rowCells(t, rows, "capacity/pods")[1]; got != "1/110" {
					t.Errorf("capacity/pods = %q, want the cluster's pod", got)
				}
				for id, want := range tc.f3 {
					item, ok := folder.ItemByID(ctx, id)
					if !ok {
						t.Fatalf("no row %s", id)
					}
					_, body, _, _, _, err := item.(Viewable).ViewContent()
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(body, want) || !strings.Contains(body, overviewSwitchHint) {
						t.Errorf("F3 of %s lacks %q or the switch hint:\n%s", id, want, body)
					}
				}
			})
		})
	}
}

// TestInfoSummaryTitles checks the scope labels of the bars and the top
// pods for a namespace, all namespaces, a node and a workload.
func TestInfoSummaryTitles(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{readyNode("n1", "4", "8Gi", 110), onNode(overviewRunningPod("a", "web"), "n1")}
	cfg := appconfig.DefaultOverview()
	tests := []struct {
		name, namespace, node string
		workload              *InfoWorkload
		bars, top             string
	}{
		{name: "namespace", namespace: "empty", bars: "Cluster capacity (all nodes, all namespaces)", top: "Top pods by memory in namespace empty"},
		{name: "all namespaces", namespace: AllNamespaces, bars: "Cluster capacity (all nodes, all namespaces)", top: "Top pods by memory in all namespaces"},
		{name: "node", namespace: "a", node: "n1", bars: "Node n1 (all its pods, all namespaces)", top: "Top pods by memory in namespace a on node n1"},
		{
			name: "workload", namespace: "a", workload: &InfoWorkload{GVR: ovDeploymentsGVR, Namespace: "a", Name: "web"},
			bars: "Pods of deployments.apps a/web", top: "Top pods of deployments.apps a/web by memory",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := measuredReport(t, tc.namespace, nil, objs...)
			s := infoSummary(r, nil, cfg, tc.node)
			if tc.workload != nil {
				s = workloadInfoSummary(r, nil, cfg, *tc.workload)
			}
			if s.BarsTitle != tc.bars || s.TopTitle != tc.top {
				t.Fatalf("titles = %q / %q, want %q / %q", s.BarsTitle, s.TopTitle, tc.bars, tc.top)
			}
		})
	}
	if s := infoSummary(nil, nil, cfg, ""); s.BarsTitle != "" || s.TopTitle != "" {
		t.Fatalf("loading summary carries titles %q / %q", s.BarsTitle, s.TopTitle)
	}
}
