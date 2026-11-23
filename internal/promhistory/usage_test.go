package promhistory_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/stokaro/kc/internal/promhistory"
)

// rangeBackend answers range queries by a rule per query: the first rule
// whose substring the query contains wins.
type rangeBackend struct {
	t     *testing.T
	mu    sync.Mutex
	rules []rangeRule
	asked []map[string]string
	hits  atomic.Int32
	block chan struct{}
}

type rangeRule struct {
	contains string
	body     string
}

func (b *rangeBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.hits.Add(1)
	q := r.URL.Query()
	p := map[string]string{"path": r.URL.Path}
	for k := range q {
		p[k] = q.Get(k)
	}
	b.mu.Lock()
	b.asked = append(b.asked, p)
	rules := b.rules
	b.mu.Unlock()
	if b.block != nil {
		select {
		case <-b.block:
		case <-r.Context().Done():
			return
		}
	}
	for _, rule := range rules {
		if strings.Contains(q.Get("query"), rule.contains) {
			_, _ = w.Write([]byte(rule.body))
			return
		}
	}
	b.t.Errorf("unexpected query %q", q.Get("query"))
	_, _ = w.Write([]byte(emptyMatrix))
}

func (b *rangeBackend) queries() []map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]map[string]string(nil), b.asked...)
}

func newRangeClient(t *testing.T, b *rangeBackend, labels map[string]string) *promhistory.Client {
	t.Helper()
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	cfg := promBackend
	cfg.Labels = labels
	c, err := promhistory.NewServiceProxyClient(&rest.Config{Host: srv.URL, BearerToken: token}, cfg)
	if err != nil {
		t.Fatalf("NewServiceProxyClient: %v", err)
	}
	return c
}

// steady is a matrix with value v at every step of r.
func steady(r promhistory.Range, v string) string {
	var vals []string
	for at := r.Start; !at.After(r.End); at = at.Add(r.Step) {
		vals = append(vals, fmt.Sprintf(`[%d,"%s"]`, at.Unix(), v))
	}
	return matrix(vals...)
}

func TestRangeFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		span       time.Duration
		cells      int
		offset     time.Duration
		wantStep   time.Duration
		wantPoints int
	}{
		{name: "an hour in 60 cells", span: time.Hour, cells: 60, wantStep: time.Minute, wantPoints: 61},
		{name: "ten minutes never below the scrape step", span: 10 * time.Minute, cells: 200, wantStep: 15 * time.Second, wantPoints: 41},
		{name: "seven days on a wide screen is bounded", span: 7 * 24 * time.Hour, cells: 5000, wantStep: 21 * time.Minute, wantPoints: 481},
		{name: "an uneven step rounds up to a second", span: time.Hour, cells: 70, wantStep: 52 * time.Second},
		{name: "an end inside a step is aligned down and the start still covered", span: time.Hour, offset: 7 * time.Second, cells: 60, wantStep: 61 * time.Second, wantPoints: 61},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := promhistory.RangeFor(now.Add(-tc.span), now.Add(tc.offset), tc.cells)
			if r.Step != tc.wantStep {
				t.Errorf("step = %v, want %v", r.Step, tc.wantStep)
			}
			if !r.End.Equal(r.End.Truncate(r.Step)) || r.End.After(now.Add(tc.offset)) || r.Start.After(now.Add(-tc.span)) {
				t.Errorf("end %v is not aligned to the step or lies after the asked end", r.End)
			}
			if r.Steps() > min(tc.cells, promhistory.MaxPoints)+1 {
				t.Errorf("steps = %d, above the bound", r.Steps())
			}
			if tc.wantPoints > 0 && r.Steps() != tc.wantPoints {
				t.Errorf("steps = %d, want %d", r.Steps(), tc.wantPoints)
			}
		})
	}
}

func TestUsageQueriesAndCoverage(t *testing.T) {
	t.Parallel()
	r := promhistory.RangeFor(now.Add(-time.Hour), now, 60)
	b := &rangeBackend{t: t}
	b.rules = []rangeRule{
		{contains: "count(sum by (namespace, pod)", body: steady(r, "1")},
		{contains: "count((", body: steady(r, "2")},
		{contains: "max_over_time((sum(container_memory", body: steady(r, "300")},
		{contains: "container_cpu_usage_seconds_total", body: steady(r, "0.5")},
		{contains: "container_memory_working_set_bytes", body: steady(r, "200")},
	}
	c := newRangeClient(t, b, map[string]string{"cluster": "eu"})
	scope := promhistory.Scope{Kind: promhistory.ScopeWorkload, Workload: web}
	u, err := c.Usage(t.Context(), scope, r)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(u.CPU) != r.Steps() || u.CPU[0].Value != 0.5 || u.MemoryPeak[0].Value != 300 {
		t.Fatalf("usage = %+v", u)
	}
	if u.RateWindow != 5*time.Minute || u.PeakResolution != 15*time.Second {
		t.Errorf("rate window %v, peak resolution %v; want 5m and 15s", u.RateWindow, u.PeakResolution)
	}
	cov := u.Coverage()
	if cov.Complete() || cov.Partial != r.Steps() || !cov.Known {
		t.Errorf("coverage = %+v: one measured pod of two expected must be partial at every step", cov)
	}
	qs := b.queries()
	if len(qs) != 5 {
		t.Fatalf("%d queries, want 5", len(qs))
	}
	for _, q := range qs {
		if !strings.Contains(q["query"], `cluster="eu"`) {
			t.Errorf("query %s lacks the configured label", q["query"])
		}
		if q["step"] != "60" || q["start"] != formatUnix(r.Start) || q["end"] != formatUnix(r.End) {
			t.Errorf("query range %s..%s step %s, want the asked range", q["start"], q["end"], q["step"])
		}
		if strings.Contains(q["query"], "max_over_time((sum(") && !strings.Contains(q["query"], "[1m:15s]") {
			t.Errorf("peak query %s, want a 15s subquery per 1m step", q["query"])
		}
	}
}

func formatUnix(t time.Time) string { return fmt.Sprint(t.Unix()) }

// TestUsageEveryNamespace: an empty namespace sums the whole cluster, so
// no selector may ask for namespace="" (which matches no container).
func TestUsageEveryNamespace(t *testing.T) {
	t.Parallel()
	r := promhistory.RangeFor(now.Add(-time.Hour), now, 60)
	b := &rangeBackend{t: t, rules: []rangeRule{{contains: "", body: emptyMatrix}}}
	c := newRangeClient(t, b, map[string]string{"cluster": "eu"})
	scope := promhistory.Scope{Kind: promhistory.ScopeNamespace}
	if scope.String() != "all namespaces" {
		t.Errorf("String = %q", scope.String())
	}
	if _, err := c.Usage(t.Context(), scope, r); err != nil {
		t.Fatalf("Usage: %v", err)
	}
	qs := b.queries()
	if len(qs) != 5 {
		t.Fatalf("%d queries, want 5", len(qs))
	}
	for _, q := range qs {
		if strings.Contains(q["query"], "namespace=") || !strings.Contains(q["query"], `cluster="eu"`) {
			t.Errorf("query %s: want no namespace matcher and the configured label", q["query"])
		}
	}
}

func TestUsageCoverageStates(t *testing.T) {
	t.Parallel()
	r := promhistory.Range{Start: now.Add(-4 * time.Minute), End: now, Step: time.Minute}
	pt := func(minBefore int, v float64) promhistory.Point {
		return promhistory.Point{At: now.Add(-time.Duration(minBefore) * time.Minute), Value: v}
	}
	tests := []struct {
		name               string
		measured, expected []promhistory.Point
		want               promhistory.Coverage
	}{
		{name: "full", measured: []promhistory.Point{pt(4, 2), pt(3, 2), pt(2, 2), pt(1, 2), pt(0, 2)}, expected: []promhistory.Point{pt(4, 2), pt(3, 2), pt(2, 2), pt(1, 2), pt(0, 2)},
			want: promhistory.Coverage{Steps: 5, Full: 5, Known: true}},
		{name: "a gap and a partial step", measured: []promhistory.Point{pt(4, 2), pt(2, 1), pt(1, 2), pt(0, 2)}, expected: []promhistory.Point{pt(4, 2), pt(3, 2), pt(2, 2), pt(1, 2), pt(0, 2)},
			want: promhistory.Coverage{Steps: 5, Full: 3, Partial: 1, Empty: 1, Known: true}},
		{name: "nothing ran", measured: make([]promhistory.Point, 0), expected: make([]promhistory.Point, 0),
			want: promhistory.Coverage{Steps: 5, Full: 5, Known: true}},
		{name: "owners unknown", measured: []promhistory.Point{pt(4, 1), pt(3, 1)}, expected: nil,
			want: promhistory.Coverage{Steps: 5, Empty: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := promhistory.Usage{Range: r, Measured: tc.measured, Expected: tc.expected}
			if got := u.Coverage(); got != tc.want {
				t.Errorf("Coverage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestUsageStartsAtTheObjectsCreation: a Deployment recreated under the
// same name 20 minutes ago reads only its own 20 minutes; the owner series
// cannot tell its pods from the old object's.
func TestUsageStartsAtTheObjectsCreation(t *testing.T) {
	t.Parallel()
	r := promhistory.RangeFor(now.Add(-time.Hour), now, 60)
	b := &rangeBackend{t: t, rules: []rangeRule{{contains: "", body: emptyMatrix}}}
	c := newRangeClient(t, b, nil)
	created := now.Add(-20*time.Minute + 10*time.Second)
	u, err := c.Usage(t.Context(), promhistory.Scope{Kind: promhistory.ScopeWorkload, Workload: web, UID: "u2", Since: created}, r)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if !u.Clipped || u.Range.Start.Before(created) || u.Range.Start.Sub(created) >= r.Step || !u.Asked.Start.Equal(r.Start) {
		t.Fatalf("range %v (asked %v), clipped %v; want it to start at the first step after %v", u.Range.Start, u.Asked.Start, u.Clipped, created)
	}
	for _, q := range b.queries() {
		if q["start"] != formatUnix(u.Range.Start) {
			t.Errorf("query start %s, want %s", q["start"], formatUnix(u.Range.Start))
		}
	}
	// A rollout keeps the UID: the same scope key, one history.
	same := promhistory.Scope{Kind: promhistory.ScopeWorkload, Workload: web, UID: "u2", Since: created}
	other := promhistory.Scope{Kind: promhistory.ScopeWorkload, Workload: web, UID: "u1", Since: now.Add(-48 * time.Hour)}
	if c.UsageKey(same, r) != c.UsageKey(promhistory.Scope{Kind: promhistory.ScopeWorkload, Workload: web, UID: "u2", Since: created}, r) {
		t.Error("the same object got two keys")
	}
	if c.UsageKey(same, r) == c.UsageKey(other, r) {
		t.Error("a recreated object shares the key of the one before it")
	}
}

func TestUsageNonFiniteAndFailure(t *testing.T) {
	t.Parallel()
	r := promhistory.Range{Start: now.Add(-2 * time.Minute), End: now, Step: time.Minute}
	b := &rangeBackend{t: t, rules: []rangeRule{
		{contains: "count(", body: `{"status":"error","errorType":"execution","error":"boom"}`},
		{contains: "", body: matrix(at(2, "NaN"), at(1, "+Inf"), at(0, "1"))},
	}}
	c := newRangeClient(t, b, nil)
	u, err := c.Usage(t.Context(), promhistory.Scope{Kind: promhistory.ScopeNamespace, Namespace: "shop"}, r)
	if err != nil {
		t.Fatalf("Usage: %v (failed counts must not fail the read)", err)
	}
	if len(u.CPU) != 1 || u.CPU[0].Value != 1 {
		t.Errorf("CPU = %+v, want NaN and Inf dropped as gaps", u.CPU)
	}
	if u.Expected != nil || u.Measured != nil || u.Coverage().Known {
		t.Errorf("counts = %v / %v, want unknown", u.Measured, u.Expected)
	}

	bad := &rangeBackend{t: t, rules: []rangeRule{{contains: "", body: `{"status":"error","errorType":"bad_data","error":"parse error"}`}}}
	if _, err := newRangeClient(t, bad, nil).Usage(t.Context(), promhistory.Scope{Kind: promhistory.ScopePod, Namespace: "shop", Name: "web-1"}, r); err == nil {
		t.Error("a failed usage query must fail the read")
	}
}

// TestUsageSevenDaysAndZoom: a week reports its aligned end and that CPU
// is averaged over one step; zooming into the last 15 minutes reads at the
// scrape resolution with the five minute rate window.
func TestUsageSevenDaysAndZoom(t *testing.T) {
	t.Parallel()
	b := &rangeBackend{t: t, rules: []rangeRule{{contains: "", body: emptyMatrix}}}
	c := newRangeClient(t, b, nil)
	scope := promhistory.Scope{Kind: promhistory.ScopePod, Namespace: "shop", Name: "web-1"}
	week := promhistory.RangeFor(now.Add(-7*24*time.Hour), now.Add(-3*time.Second), 120)
	u, err := c.Usage(t.Context(), scope, week)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if u.Range.Step != 84*time.Minute || u.RateWindow != u.Range.Step || u.Range.End.After(now) {
		t.Errorf("week: step %v, rate window %v, end %v", u.Range.Step, u.RateWindow, u.Range.End)
	}
	if u.PeakResolution != 504*time.Second {
		t.Errorf("week: peak resolution %v, want a tenth of the step", u.PeakResolution)
	}
	zoom := promhistory.RangeFor(now.Add(-15*time.Minute), now, 120)
	u, err = c.Usage(t.Context(), scope, zoom)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if u.Range.Step != 15*time.Second || u.RateWindow != 5*time.Minute || u.PeakResolution != 15*time.Second {
		t.Errorf("zoom: step %v, rate window %v, peak resolution %v", u.Range.Step, u.RateWindow, u.PeakResolution)
	}
	for _, q := range b.queries() {
		if strings.Contains(q["query"], "max_over_time((sum(") && q["step"] == "15" {
			t.Errorf("a 15s step needs no peak subquery: %s", q["query"])
		}
	}
}
