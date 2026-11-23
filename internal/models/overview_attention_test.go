package models

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
)

// reportFolder is /overview over a fixed report: the engine is closed, so
// it never recomputes and the rows are exactly those of r.
func reportFolder(t *testing.T, ns string, r *overviewReport) *OverviewFolder {
	t.Helper()
	e := newOverviewEngine(Deps{Ctx: t.Context()}, ns)
	e.close()
	e.mu.Lock()
	e.report = r
	e.mu.Unlock()
	return newOverviewFolder(Deps{Ctx: t.Context()}, ns, []string{"overview"}, e)
}

func folderRows(t *testing.T, f Folder) []table.Row {
	t.Helper()
	return slices.DeleteFunc(f.Lines(t.Context(), 0, 500), func(r table.Row) bool {
		b, ok := r.(Back)
		return ok && b.IsBack()
	})
}

// withQuietRows is rows followed by the rows of every quiet list, so a test
// can find a finding wherever the overview shows it.
func withQuietRows(t *testing.T, rows []table.Row) []table.Row {
	t.Helper()
	out := slices.Clone(rows)
	for _, r := range rows {
		id, _, _, _ := r.Columns()
		if !strings.HasPrefix(id, overviewQuietIDPrefix) {
			continue
		}
		list, err := r.(Enterable).Enter()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, folderRows(t, list)...)
		if c, ok := list.(interface{ Close() }); ok {
			c.Close()
		}
	}
	return out
}

// rowByID is the row with id in rows.
func rowByID(t *testing.T, rows []table.Row, id string) table.Row {
	t.Helper()
	for _, r := range rows {
		if rid, _, _, _ := r.Columns(); rid == id {
			return r
		}
	}
	t.Fatalf("no row %q in %v", id, overviewRowIDs(rows))
	return nil
}

// healthyCluster has one node and one pod with requests and a memory
// limit, so the rules over nodes and pods pass and the rest have nothing
// to check.
func healthyCluster() []crclient.Object {
	web := withPod(overviewRunningPod("shop", "web"), func(p *corev1.Pod) { p.Spec.Containers[0].Resources = resources("100m", "64Mi", "", "64Mi") })
	return []crclient.Object{readyNode("n1", "4", "8Gi", 110), web}
}

// pendingPod is a pod that raises exactly one warning: pods pending.
func pendingPod() *corev1.Pod {
	return withPod(overviewRunningPod("shop", "stuck"), func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodPending
		p.Spec.Containers[0].Resources = resources("100m", "64Mi", "", "64Mi")
	})
}

func measuredHealthy() *overviewMeasured {
	return &overviewMeasured{
		nodes:       map[string]usageValues{"n1": {cpu: 10, mem: 1 << 20}},
		pods:        map[string][]containerUsage{"shop/web": {{name: "app", cpu: 1, mem: 1 << 20}}},
		clusterPods: map[string][]containerUsage{"shop/web": {{name: "app", cpu: 1, mem: 1 << 20}}},
		stats:       map[string]*nodeStats{"n1": {nodeFS: fsUse{used: 1 << 30, capacity: 100 << 30}}},
	}
}

func TestOverviewAttentionSummary(t *testing.T) {
	t.Parallel()
	pending := pendingPod()
	tests := []struct {
		name      string
		ns        string
		objs      []crclient.Object
		missing   schema.GroupVersionResource
		reason    string
		value     string // the attention row's value
		state     string // its state, a substring
		attention []string
	}{
		{name: "all healthy", ns: AllNamespaces, objs: healthyCluster(), value: "nothing", state: " ok, "},
		{name: "empty namespace", ns: "empty", objs: healthyCluster(), value: "nothing", state: "no workloads in namespace empty"},
		{name: "one warning", ns: AllNamespaces, objs: append(healthyCluster(), pending), value: "1 warning", state: " ok, ",
			attention: []string{"health/pods-pending"}},
		{name: "unavailable source", ns: AllNamespaces, objs: healthyCluster(), missing: ovQuotasGVR, reason: "forbidden",
			value: "1 unavailable", state: " ok, ", attention: []string{"risks/quotas"}},
		{name: "optional API absent", ns: AllNamespaces, objs: healthyCluster(), missing: ovCertificatesGVR, reason: notServedReason,
			value: "nothing", state: " n/a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := loadOverviewInput(t.Context(), newOverviewFakeClient(tc.objs...), tc.ns, func(gvr schema.GroupVersionResource) string {
				if gvr == tc.missing {
					return tc.reason
				}
				return ""
			})
			r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, measuredHealthy())
			f := reportFolder(t, tc.ns, r)
			rows := folderRows(t, f)
			if id, _, _, _ := rows[0].Columns(); id != overviewAttentionID {
				t.Fatalf("first row = %q, want the attention row", id)
			}
			cells := rowCells(t, rows, overviewAttentionID)
			if cells[1] != tc.value || !strings.Contains(cells[2], tc.state) {
				t.Fatalf("attention row = %q, want value %q and state containing %q", cells, tc.value, tc.state)
			}
			// Every finding that needs attention is a visible row, and
			// is listed under /attention.
			ids := overviewRowIDs(rows)
			for _, id := range tc.attention {
				if !slices.Contains(ids, id) {
					t.Errorf("%s is hidden: %v", id, ids)
				}
			}
			att, err := rows[0].(Enterable).Enter()
			if err != nil {
				t.Fatal(err)
			}
			got := overviewRowIDs(folderRows(t, att))
			if len(tc.attention) == 0 {
				if !slices.Equal(got, []string{"overview/none"}) {
					t.Errorf("/attention = %v, want nothing", got)
				}
			} else if !slices.Equal(got, tc.attention) {
				t.Errorf("/attention = %v, want %v", got, tc.attention)
			}
			// An unavailable check is never counted as passed.
			c := countClasses(r.findings)
			if tc.reason == "forbidden" && c[classUnavailable] != 1 {
				t.Errorf("counts = %v, want one unavailable", c)
			}
		})
	}
}

func TestOverviewQuietRowsFoldAndList(t *testing.T) {
	t.Parallel()
	r := measuredReport(t, AllNamespaces, measuredHealthy(), healthyCluster()...)
	f := reportFolder(t, AllNamespaces, r)
	rows := folderRows(t, f)
	ids := overviewRowIDs(rows)

	// The capacity rows carry figures and stay; a zero ok check folds.
	for _, id := range []string{"capacity/cpu", "capacity/memory"} {
		if !slices.Contains(ids, id) {
			t.Errorf("%s folded: %v", id, ids)
		}
	}
	if slices.Contains(ids, "health/pods-pending") {
		t.Errorf("a zero ok row is shown: %v", ids)
	}

	// Every finding is either shown or counted in its section's quiet
	// row, and that row lists exactly the hidden ones.
	for _, section := range overviewSections {
		var want []string
		for _, x := range r.findings {
			if x.section != section {
				continue
			}
			if quietFinding(x) {
				want = append(want, x.id)
			} else if !slices.Contains(ids, x.id) {
				t.Errorf("%s is neither shown nor folded", x.id)
			}
		}
		quietID := overviewQuietIDPrefix + strings.ToLower(section)
		if len(want) == 0 {
			if slices.Contains(ids, quietID) {
				t.Errorf("%s: a quiet row with nothing in it", section)
			}
			continue
		}
		cells := rowCells(t, rows, quietID)
		if !strings.HasPrefix(cells[0], fmt.Sprintf("/%d quiet checks", len(want))) || cells[2] != "hidden" {
			t.Errorf("%s quiet row = %q, want %d", section, cells, len(want))
		}
		var quiet Enterable
		for _, row := range rows {
			if id, _, _, _ := row.Columns(); id == quietID {
				quiet = row.(Enterable)
			}
		}
		list, err := quiet.Enter()
		if err != nil {
			t.Fatal(err)
		}
		got := folderRows(t, list)
		if !slices.Equal(overviewRowIDs(got), want) {
			t.Errorf("%s quiet list = %v, want %v", section, overviewRowIDs(got), want)
		}
		// Rows keep the finding: F3 explains it, Enter lists its objects.
		item := got[0].(*OverviewFindingItem)
		if _, body, _, _, _, err := item.ViewContent(); err != nil || !strings.Contains(body, "How it is computed") {
			t.Errorf("%s: F3 = %v %q", want[0], err, body)
		}
		if next, err := item.Enter(); err != nil || next == nil {
			t.Errorf("%s: Enter = %v %v", want[0], next, err)
		}
	}
}

func TestOverviewNotApplicableReadsAsNothingChecked(t *testing.T) {
	t.Parallel()
	r := measuredReport(t, "empty", measuredHealthy(), healthyCluster()...)
	f := mustFinding(t, r, "health/pods-not-ready")
	if f.notApplicable != "no pods in namespace empty" || f.value != "none" || f.state != "n/a" {
		t.Fatalf("pods not ready in an empty namespace = %q %q %q", f.value, f.state, f.notApplicable)
	}
	folder := reportFolder(t, "empty", r)
	if body := folder.explainFinding(f); !strings.Contains(body, "Not applicable: no pods in namespace empty; nothing was checked, which is not a pass.") {
		t.Fatalf("F3:\n%s", body)
	}
	// The node checks are cluster-wide and still apply.
	if n := mustFinding(t, r, "health/nodes-not-ready"); n.notApplicable != "" || n.value != "0" {
		t.Fatalf("nodes not ready = %q %q", n.value, n.notApplicable)
	}
}

func TestOverviewRowIDsStableAcrossSeverityChanges(t *testing.T) {
	t.Parallel()
	pending := pendingPod()
	before := overviewRowIDs(folderRows(t, reportFolder(t, AllNamespaces, measuredReport(t, AllNamespaces, measuredHealthy(), append(healthyCluster(), pending)...))))
	after := overviewRowIDs(folderRows(t, reportFolder(t, AllNamespaces, measuredReport(t, AllNamespaces, measuredHealthy(), healthyCluster()...))))
	// The warning row leaves for the quiet row; every other ID stays and
	// keeps its order, so the cursor on another row does not move to a
	// different finding.
	if !slices.Contains(before, "health/pods-pending") || slices.Contains(after, "health/pods-pending") {
		t.Fatalf("before %v\nafter %v", before, after)
	}
	kept := slices.DeleteFunc(slices.Clone(before), func(id string) bool { return id == "health/pods-pending" })
	if !slices.Equal(kept, after) {
		t.Fatalf("IDs moved:\nbefore %v\nafter  %v", kept, after)
	}
}
