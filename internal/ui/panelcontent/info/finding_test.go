package info

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

func TestWidgetFollowsTheSelectedFinding(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.summary = models.InfoSummary{UsageAt: h.now, Bars: bars(25, 55), TopNote: "no pods"}
	h.w.SetFocus(ctx, true)
	_ = h.w.Init(ctx)
	view := func() string { return ansi.Strip(strings.Join(h.w.lines(70, 60), "\n")) }

	steps := []struct {
		name    string
		finding *models.FindingPreview
		want    []string
		not     []string
	}{
		{
			name: "pending pods",
			finding: &models.FindingPreview{
				ID: "health/pods-pending", Title: "/pods pending", Scope: "namespace shop", Value: "12", State: "warning",
				Why:         "Pods in phase Pending.",
				TargetCount: 12, Inputs: 1,
				Targets: []models.FindingPreviewTarget{{Resource: "pods", Namespace: "shop", Name: "web-1", Value: "Unschedulable"}},
			},
			want: []string{" /pods pending\n", " Scope: namespace shop\n", " Value: 12  State: warning\n", " 12 objects, as Enter lists them\n",
				"   pods shop/web-1  Unschedulable\n", "   and 11 more\n", "Enter lists the objects, F3 shows the calculation", "M-c copies the 1 input command"},
			not: []string{"CPU ", "Top pods"},
		},
		{
			name:    "nothing matched",
			finding: &models.FindingPreview{Title: "/pods crash looping", Scope: "namespace shop", Value: "0", State: "ok", Why: "Containers waiting in CrashLoopBackOff."},
			want:    []string{" Nothing matched. Checked:\n   Containers waiting in CrashLoopBackOff."},
			not:     []string{"objects, as Enter", "pods shop/web-1"},
		},
		{
			name: "unavailable",
			finding: &models.FindingPreview{Title: "/nodes cpu", Scope: "cluster", Value: "n/a", State: "unknown",
				Unavailable: "metrics.k8s.io is not served by this cluster", Verify: []string{"kubectl top nodes"}},
			want: []string{" Not available\n   metrics.k8s.io is not served by this cluster", " Check with\n   kubectl top nodes"},
			not:  []string{"Nothing matched"},
		},
		{
			name: "back to a list",
			want: []string{" CPU ", "pods 1.0/4.0 cores allocatable"},
			not:  []string{"Scope:", "/nodes cpu"},
		},
	}
	for _, s := range steps {
		h.target.Finding = s.finding
		h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
		got := view()
		for _, w := range s.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: view misses %q:\n%s", s.name, w, got)
			}
		}
		for _, w := range s.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: view still shows %q:\n%s", s.name, w, got)
			}
		}
	}
	// The scope did not change, so no second summarizer was opened.
	if len(h.opened) != 1 {
		t.Fatalf("opened %d summarizers, want 1", len(h.opened))
	}
}

func TestWrapWords(t *testing.T) {
	t.Parallel()
	got := wrapWords("aa bb cc dd", 5)
	if strings.Join(got, "|") != "aa bb|cc dd" {
		t.Fatalf("wrap = %q", got)
	}
}
