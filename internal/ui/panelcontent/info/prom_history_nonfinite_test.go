package info

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/stokaro/kc/internal/promhistory"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	"github.com/stokaro/kc/pkg/appconfig"
)

// promMatrix is a successful range result with one series of values.
func promMatrix(values ...string) string {
	return `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` + strings.Join(values, ",") + `]}]}}`
}

// fakePrometheus answers the CPU and memory range queries with cpu and
// mem, and the pod count with 3.
func fakePrometheus(t *testing.T, cpu, mem string) *rest.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case strings.Contains(q, "container_cpu_usage_seconds_total"):
			_, _ = w.Write([]byte(cpu))
		case strings.Contains(q, "container_memory_working_set_bytes"):
			_, _ = w.Write([]byte(mem))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"3"]}]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return &rest.Config{Host: srv.URL}
}

// TestPromHistoryNonFiniteSeries reads a Prometheus answer through the
// real client and shows it in Info: a series without a finite sample is
// missing data, never zero usage, and the other metric still renders.
func TestPromHistoryNonFiniteSeries(t *testing.T) {
	t.Parallel()
	h0 := newHarness(t)
	end := h0.now.Truncate(time.Minute)
	sample := func(minutesBefore int, v string) string {
		return fmt.Sprintf(`[%d,"%s"]`, end.Add(-time.Duration(minutesBefore)*time.Minute).Unix(), v)
	}
	mem := promMatrix(sample(30, "536870912"), sample(0, "268435456"))
	for _, tc := range []struct {
		name    string
		cpu     string
		cpuLine string // what the CPU line shows
		gaps    string
	}{
		{name: "all NaN", cpu: promMatrix(sample(10, "NaN")), cpuLine: " CPU    no finite samples in this window", gaps: "61 of 61 steps without samples"},
		{name: "all infinite", cpu: promMatrix(sample(10, "+Inf"), sample(5, "-Inf")), cpuLine: " CPU    no finite samples in this window", gaps: "61 of 61 steps without samples"},
		{name: "no values", cpu: promMatrix(), cpuLine: " CPU    no finite samples in this window", gaps: "61 of 61 steps without samples"},
		{name: "mixed finite and NaN", cpu: promMatrix(sample(10, "NaN"), sample(5, "0.5"), sample(0, "Inf")), cpuLine: "max 500m", gaps: "60 of 61 steps without samples"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := promhistory.NewServiceProxyClient(fakePrometheus(t, tc.cpu, mem),
				appconfig.MetricsBackendConfig{Namespace: "monitoring", Service: "prom", Port: "9090"})
			if err != nil {
				t.Fatalf("client: %v", err)
			}
			hist, err := c.Fetch(t.Context(), promhistory.Target{Kind: "Deployment", Namespace: "shop", Name: "web"},
				promhistory.Options{Window: time.Hour, Now: end})
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			h, f := promHarness(t)
			f.h = hist
			runPromRead(t, h.w, h.w.OnSelectionChanged(t.Context(), panelcontent.Selection{}))
			got := promScreen(h.w)
			for _, want := range []string{tc.cpuLine, "max 512Mi, end 256Mi", tc.gaps} {
				if !strings.Contains(got, want) {
					t.Errorf("view lacks %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "max 0m") || strings.Contains(got, "end 0m") {
				t.Errorf("missing CPU shown as zero:\n%s", got)
			}
		})
	}
}

// TestPromSeriesLineEmpty guards the renderer itself: an empty series,
// however it was produced, is missing data and does not index past it.
func TestPromSeriesLineEmpty(t *testing.T) {
	t.Parallel()
	end := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	h := promhistory.History{Start: end.Add(-time.Hour), End: end, Step: time.Minute, CPU: make([]promhistory.Point, 0)}
	if got := promSeriesLine("CPU", h, h.CPU, 80, promCores); got != " CPU    no finite samples in this window" {
		t.Fatalf("line = %q", got)
	}
	if got := promSeriesLine("CPU", h, nil, 80, promCores); got != " CPU    no series" {
		t.Fatalf("line = %q", got)
	}
}
