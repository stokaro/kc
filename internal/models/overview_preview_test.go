package models

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

// previewFolder is /overview over the fixed report r: its engine is closed,
// so the rows and the previews read exactly r.
func previewFolder(t *testing.T, ns string, r *overviewReport) *OverviewFolder {
	t.Helper()
	deps := Deps{Ctx: t.Context(), CtxName: "dev", KubectlContext: "dev"}
	e := newOverviewEngine(deps, ns)
	e.close()
	e.mu.Lock()
	e.report = r
	e.mu.Unlock()
	return newOverviewFolder(deps, ns, []string{"overview"}, e)
}

func previewOf(t *testing.T, f *OverviewFolder, id string) FindingPreview {
	t.Helper()
	item, ok := f.ItemByID(t.Context(), id)
	if !ok {
		t.Fatalf("no row %s", id)
	}
	p, ok := item.(*OverviewFindingItem).FindingPreview()
	if !ok {
		t.Fatalf("%s: no preview", id)
	}
	return p
}

func TestFindingPreviewMatchesTheRowAndTheDrillDown(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{readyNode("n1", "4", "8Gi", 110)}
	for i := range 10 {
		objs = append(objs, waiting(overviewRunningPod("shop", fmt.Sprintf("loop-%02d", i)), "CrashLoopBackOff"))
	}
	r := measuredReport(t, "shop", &overviewMeasured{}, objs...)
	f := previewFolder(t, "shop", r)
	p := previewOf(t, f, "health/pods-crashloop")
	row := rowCells(t, f.Lines(t.Context(), 0, 200), "health/pods-crashloop")
	if p.Title != row[0] || p.Value != row[1] || p.State != row[2] {
		t.Fatalf("preview %q %q %q, row %v", p.Title, p.Value, p.State, row)
	}
	if p.Scope != "namespace shop" || p.TargetCount != 10 || len(p.Targets) != findingPreviewTargets || p.Health != RowHealthFailed {
		t.Fatalf("preview = %+v", p)
	}
	if !strings.Contains(p.Why, "CrashLoopBackOff") || strings.Contains(p.Why, "\n") {
		t.Fatalf("why = %q", p.Why)
	}
	// The objects are those Enter lists, in the same order.
	item, _ := f.ItemByID(t.Context(), "health/pods-crashloop")
	next, err := item.(*OverviewFindingItem).Enter()
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, id := range overviewRowIDs(next.Lines(t.Context(), 0, 100)) {
		if id != "__back__" {
			listed = append(listed, id)
		}
	}
	var shown []string
	for _, tg := range p.Targets {
		shown = append(shown, "v1/pods/"+tg.Namespace+"/"+tg.Name)
	}
	if !slices.Equal(shown, listed[:len(shown)]) {
		t.Fatalf("preview objects %v, Enter lists %v", shown, listed)
	}
	// A cluster-wide finding names its own scope.
	if c := previewOf(t, f, "capacity/cpu"); !strings.HasPrefix(c.Scope, "cluster") || c.Inputs != 5 {
		t.Fatalf("cpu preview scope %q inputs %d", c.Scope, c.Inputs)
	}
}

func TestFindingPreviewUnavailableNamesTheSourceAndACheck(t *testing.T) {
	t.Parallel()
	in := loadOverviewInput(t.Context(), newOverviewFakeClient(readyNode("n1", "4", "8Gi", 110)), "shop", func(gvr schema.GroupVersionResource) string {
		if gvr == ovQuotasGVR {
			return "resourcequotas is forbidden: User cannot list"
		}
		return ""
	})
	r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil)
	f := previewFolder(t, "shop", r)
	p := previewOf(t, f, "risks/quotas")
	if p.Unavailable != "resourcequotas: resourcequotas is forbidden: User cannot list" {
		t.Fatalf("unavailable = %q", p.Unavailable)
	}
	want := []string{"kubectl auth can-i list resourcequotas -n shop", "kubectl describe resourcequotas -n shop"}
	if !slices.Equal(p.Verify, want) {
		t.Fatalf("verify = %q, want %q", p.Verify, want)
	}
	// Metrics that are not served get no permission check, only the input.
	cpu := previewOf(t, f, "capacity/cpu")
	if cpu.Unavailable == "" && cpu.Verify != nil {
		t.Fatalf("cpu verify without a reason: %q", cpu.Verify)
	}
	for _, c := range cpu.Verify {
		if strings.Contains(c, "auth can-i") {
			t.Fatalf("permission check for a source that is not forbidden: %q", cpu.Verify)
		}
	}
}

func TestFindingPreviewZeroResult(t *testing.T) {
	t.Parallel()
	pod := withPod(overviewRunningPod("shop", "web"), func(p *corev1.Pod) { p.Spec.Containers[0].Resources = resources("100m", "64Mi", "", "64Mi") })
	r := measuredReport(t, "shop", &overviewMeasured{}, readyNode("n1", "4", "8Gi", 110), pod)
	p := previewOf(t, previewFolder(t, "shop", r), "health/pods-crashloop")
	if p.TargetCount != 0 || p.Targets != nil || p.Value != "0" || p.Why == "" || p.Unavailable != "" {
		t.Fatalf("zero preview = %+v", p)
	}
}
