package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/metricsources"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
	"github.com/stokaro/kc/pkg/appconfig"
)

// The empty fields show examples that say "e.g." in grey, so they do not
// read as entered values; an entered value is drawn in the input color.
func TestMetricSourcesPlaceholdersAreExamples(t *testing.T) {
	t.Parallel()
	d := newMetricSourcesDialog(nil, "prod", "", appconfig.MetricsBackendConfig{}, false)
	for _, in := range []string{d.service.Placeholder, d.prefix.Placeholder, d.labels.Placeholder} {
		if !strings.HasPrefix(in, "e.g. ") {
			t.Errorf("placeholder %q does not start with e.g.", in)
		}
	}
	d.SetDimensions(94, 30)
	view := d.View().Content
	// 8 is the ANSI dark grey the examples use; 90 is its SGR code, here on
	// the cyan (46) of the input line.
	grey := "\x1b[90;46m"
	if uistyles.ColorDarkGrey != "8" || !strings.Contains(view, grey) {
		t.Fatalf("the examples are not drawn in grey:\n%q", view)
	}
	if !strings.Contains(ansi.Strip(view), "e.g. monitoring") {
		t.Errorf("the Service example is not shown:\n%s", ansi.Strip(view))
	}

	d.service.SetValue("monitoring/prometheus-k8s:9090")
	d.prefix.SetValue("/x")
	d.labels.SetValue("cluster=a")
	if view := d.View().Content; strings.Contains(view, grey) || strings.Contains(ansi.Strip(view), "e.g.") {
		t.Errorf("entered values are drawn as examples:\n%q", view)
	}
}

// A report line wider than the dialog wraps under the detail column
// instead of being cut, and every row of a failed check keeps its color.
func TestMetricSourcesReportWraps(t *testing.T) {
	t.Parallel()
	d := newMetricSourcesDialog(nil, "kind-kc-shots", "", appconfig.MetricsBackendConfig{}, false)
	d.showReport(metricSourcesReportMsg{checks: []metricsources.Check{
		{Name: "metrics API", State: metricsources.Available, Detail: "metrics.k8s.io/v1beta1 is served"},
		metricsources.NoBackendCheck("kind-kc-shots"),
	}})
	d.SetDimensions(94, 30)
	rows, errRows := d.reportRows()
	col := metricsources.DetailColumn([]metricsources.Check{{Name: "metrics API", State: metricsources.Available}, metricsources.NoBackendCheck("x")})
	if len(rows) < 3 || len(errRows) != len(rows) {
		t.Fatalf("rows = %q, want the no-backend line wrapped", rows)
	}
	joined := ""
	for i, r := range rows {
		if w := ansi.StringWidth(r); w > 92 {
			t.Errorf("row %d is %d wide: %q", i, w, r)
		}
		if i >= 2 {
			if strings.TrimSpace(r[:col]) != "" {
				t.Errorf("continuation row %q does not start under the detail", r)
			}
			joined += " " + strings.TrimSpace(r)
		}
		if want := i >= 1; errRows[i] != want {
			t.Errorf("row %d error = %v, want %v", i, errRows[i], want)
		}
	}
	whole := strings.TrimSpace(rows[1][col:]) + joined
	if want := metricsources.NoBackendCheck("kind-kc-shots").Detail; whole != want {
		t.Errorf("wrapped detail = %q, want %q", whole, want)
	}
	view := ansi.Strip(d.View().Content)
	if !strings.Contains(view, "Prometheus-compatible server") {
		t.Errorf("the end of the hint is not shown:\n%s", view)
	}
}
