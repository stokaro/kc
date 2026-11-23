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
			name: "partial",
			finding: &models.FindingPreview{Title: "/workloads unavailable", Scope: "namespace shop", Value: "0 (partial)", State: "ok",
				Why: "Deployments with fewer available replicas.", Partial: "statefulsets.apps: not permitted to list and watch in all namespaces or in namespace shop"},
			want: []string{" Value: 0 (partial)  State: ok\n", " Nothing matched. Checked:\n", " Partial, not counted\n   statefulsets.apps: not permitted to list and watch in all"},
			not:  []string{"Not available"},
		},
		{
			name: "not applicable",
			finding: &models.FindingPreview{Title: "/certificates expiring", Scope: "cluster", Value: "none", State: "n/a",
				Why: "Certificates that expire soon.", NotApplicable: "cert-manager is not installed"},
			want: []string{" Not applicable\n   cert-manager is not installed"},
			not:  []string{"Nothing matched", "Certificates that expire soon"},
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

// Every line of a finding wraps at the pane width, as the rest of Info
// does: no word of the title, scope, value, targets or key hints is cut.
func TestFindingLinesWrapLongLines(t *testing.T) {
	t.Parallel()
	p := models.FindingPreview{
		Title: "/pods waiting for an image pull for longer than five minutes", Scope: "namespace payments-production-europe-west",
		Value: "3", State: "warning", Health: models.RowHealthNotReady,
		Why:         "Containers waiting in ImagePullBackOff or ErrImagePull.",
		TargetCount: 4, Inputs: 2,
		Partial: "statefulsets.apps: not permitted to list",
		Targets: []models.FindingPreviewTarget{{
			Resource: "pods", Namespace: "payments-production-europe-west", Name: "checkout-7d9c8b6f5-x2x4q",
			Value: "ImagePullBackOff: registry.example.com/checkout:v2 not found", Health: models.RowHealthNotReady,
		}},
	}
	for _, width := range []int{24, 40} {
		lines := findingLines(p, width)
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: line %q is %d cells wide", width, ansi.Strip(l), w)
			}
		}
		text := noSpaces(ansi.Strip(strings.Join(lines, " ")))
		for _, want := range []string{
			p.Title, "Scope: " + p.Scope, "Value: 3", "State: warning",
			"pods payments-production-europe-west/checkout-7d9c8b6f5-x2x4q", p.Targets[0].Value,
			"and 3 more", "In the list: Enter lists the objects, F3 shows the calculation", "M-c copies the 2 input commands",
		} {
			if !strings.Contains(text, noSpaces(want)) {
				t.Errorf("width %d: view misses %q:\n%s", width, want, ansi.Strip(strings.Join(lines, "\n")))
			}
		}
	}
}

// noSpaces drops the spaces, so a word broken across lines by the wrap
// compares equal to the unbroken text.
func noSpaces(s string) string { return strings.ReplaceAll(s, " ", "") }
