package promhistory_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/promhistory"
)

// stat is one series of a resources query: its stat and resource labels
// and a value per step, "" for a step without a sample.
type stat struct {
	stat, resource string
	values         []string
}

// statMatrix is a matrix of the stats over the steps of r.
func statMatrix(r promhistory.Range, stats ...stat) string {
	var results []string
	for _, s := range stats {
		labels := fmt.Sprintf(`"stat":%q`, s.stat)
		if s.resource != "" {
			labels += fmt.Sprintf(`,"resource":%q`, s.resource)
		}
		var vals []string
		for i, v := range s.values {
			if v != "" {
				vals = append(vals, fmt.Sprintf(`[%d,"%s"]`, r.Start.Add(time.Duration(i)*r.Step).Unix(), v))
			}
		}
		results = append(results, `{"metric":{`+labels+`},"values":[`+strings.Join(vals, ",")+`]}`)
	}
	return `{"status":"success","data":{"resultType":"matrix","result":[` + strings.Join(results, ",") + `]}}`
}

// usageRules answer the usage queries with one measured pod.
func usageRules(r promhistory.Range) []rangeRule {
	return []rangeRule{
		{contains: "count(sum by (namespace, pod)", body: steady(r, "2")},
		{contains: "count((", body: steady(r, "2")},
		{contains: "max_over_time((sum(container_memory", body: steady(r, "300")},
		{contains: "container_cpu_usage_seconds_total", body: steady(r, "0.5")},
		{contains: "container_memory_working_set_bytes", body: steady(r, "200")},
	}
}

func values(ps []promhistory.Point) []float64 {
	out := make([]float64, len(ps))
	for i, p := range ps {
		out[i] = p.Value
	}
	return out
}

// TestResourcesHistory: requests and limits come per step from
// kube-state-metrics, so a request raised at the third step shows from
// there on; a step where one container had no limit has no limit sum.
func TestResourcesHistory(t *testing.T) {
	t.Parallel()
	r := promhistory.Range{Start: now.Add(-3 * time.Minute), End: now, Step: time.Minute}
	b := &rangeBackend{t: t}
	b.rules = append([]rangeRule{
		{contains: "kube_pod_container_resource_requests", body: statMatrix(r,
			stat{"sum", "cpu", []string{"0.5", "0.5", "1", "1"}},
			stat{"count", "cpu", []string{"2", "2", "2", "2"}},
			stat{"sum", "memory", []string{"256", "256", "256", "256"}},
			stat{"count", "memory", []string{"2", "2", "2", "2"}},
			stat{"containers", "", []string{"2", "2", "2", "2"}},
		)},
		{contains: "kube_pod_container_resource_limits", body: statMatrix(r,
			stat{"sum", "cpu", []string{"1", "1", "2", "2"}},
			stat{"count", "cpu", []string{"2", "2", "1", "2"}},
			stat{"sum", "memory", []string{"512", "", "512", "512"}},
			stat{"count", "memory", []string{"2", "", "2", "2"}},
		)},
	}, usageRules(r)...)
	c := newRangeClient(t, b, map[string]string{"cluster": "eu"})
	u, err := c.Usage(t.Context(), promhistory.Scope{Kind: promhistory.ScopeWorkload, Workload: web}, r)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	res := u.Resources
	if !res.Known() || u.ResourcesErr != "" {
		t.Fatalf("resources = %+v, err %q; want a known history", res, u.ResourcesErr)
	}
	if got := fmt.Sprint(values(res.CPU.Requests)); got != "[0.5 0.5 1 1]" {
		t.Errorf("CPU requests = %s, want the raised request from the third step", got)
	}
	if got := fmt.Sprint(values(res.FullLimits(res.CPU))); got != "[1 1 0 2]" {
		t.Errorf("full CPU limits = %s, want zero at the step with one unlimited container", got)
	}
	if got := res.FullLimits(res.CPU); !got[3].At.Equal(r.End) {
		t.Errorf("last full limit at %v, want %v", got[3].At, r.End)
	}
	if got := fmt.Sprint(values(res.FullLimits(res.Memory))); got != "[512 512 512]" {
		t.Errorf("full memory limits = %s, want the step without samples left out", got)
	}
	if res.CPU.Capacity != nil {
		t.Errorf("a workload has no capacity, got %v", res.CPU.Capacity)
	}
	if qs := b.queries(); len(qs) != 7 {
		t.Errorf("%d queries, want 7", len(qs))
	}
}

// TestResourceQueriesPerScope: every query carries the configured labels,
// counts only Pending and Running pods, and reads the allocatable only for
// a node or the whole cluster. With no configured labels the selectors
// stay valid.
func TestResourceQueriesPerScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		scope     promhistory.Scope
		labels    map[string]string
		want      []string
		wantNot   []string
		allocated bool
	}{
		{name: "workload", scope: promhistory.Scope{Kind: promhistory.ScopeWorkload, Workload: web}, labels: map[string]string{"cluster": "eu"},
			want: []string{`kube_pod_container_resource_requests{cluster="eu",namespace="shop",resource=~"cpu|memory"}`, `owner_name="web"`}},
		{name: "pod", scope: promhistory.Scope{Kind: promhistory.ScopePod, Namespace: "shop", Name: "web-1"}, labels: map[string]string{"cluster": "eu"},
			want: []string{`kube_pod_container_info{cluster="eu",namespace="shop",pod="web-1"}`}},
		{name: "node", scope: promhistory.Scope{Kind: promhistory.ScopeNode, Name: "n1"}, labels: map[string]string{"cluster": "eu"},
			want: []string{`kube_pod_info{cluster="eu",node="n1"}`, `kube_node_status_allocatable{cluster="eu",node="n1",resource=~"cpu|memory"}`}, wantNot: []string{"namespace="}, allocated: true},
		{name: "namespace", scope: promhistory.Scope{Kind: promhistory.ScopeNamespace, Namespace: "shop"},
			want: []string{`kube_pod_container_resource_limits{namespace="shop",resource=~"cpu|memory"}`}, wantNot: []string{"kube_node_status_allocatable", "{,"}},
		{name: "every namespace", scope: promhistory.Scope{Kind: promhistory.ScopeNamespace},
			want: []string{`kube_pod_container_info{} * on`, `kube_node_status_allocatable{resource=~"cpu|memory"}`}, wantNot: []string{"namespace="}, allocated: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := promhistory.RangeFor(now.Add(-time.Hour), now, 60)
			b := &rangeBackend{t: t, rules: []rangeRule{{contains: "", body: emptyMatrix}}}
			c := newRangeClient(t, b, tc.labels)
			u, err := c.Usage(t.Context(), tc.scope, r)
			if err != nil {
				t.Fatalf("Usage: %v", err)
			}
			if u.Resources == nil || u.Resources.Known() {
				t.Errorf("resources = %+v: an empty answer is a read without kube-state-metrics series", u.Resources)
			}
			var resQueries []string
			for _, q := range b.queries() {
				if strings.Contains(q["query"], "kube_pod_container_resource_") {
					resQueries = append(resQueries, q["query"])
				}
			}
			if len(resQueries) != 2 {
				t.Fatalf("%d resource queries, want 2", len(resQueries))
			}
			all := strings.Join(resQueries, "\n")
			for _, w := range tc.want {
				if !strings.Contains(all, w) {
					t.Errorf("queries lack %s:\n%s", w, all)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(all, w) {
					t.Errorf("queries contain %s:\n%s", w, all)
				}
			}
			for _, q := range resQueries {
				if !strings.Contains(q, `phase=~"Pending|Running"`) {
					t.Errorf("query %s does not count Pending and Running pods only", q)
				}
				for k, v := range tc.labels {
					if strings.Count(q, k+"="+fmt.Sprintf("%q", v)) < 3 {
						t.Errorf("query %s: the configured label %s is not on every selector", q, k)
					}
				}
			}
			if got := strings.Contains(all, "kube_node_status_allocatable"); got != tc.allocated {
				t.Errorf("allocatable read = %v, want %v", got, tc.allocated)
			}
		})
	}
}

// TestResourcesFailureKeepsUsage: two kube-state-metrics answering the
// same series, or a failed resources query, leave the usage intact and
// say why the requests and limits are missing.
func TestResourcesFailureKeepsUsage(t *testing.T) {
	t.Parallel()
	r := promhistory.Range{Start: now.Add(-3 * time.Minute), End: now, Step: time.Minute}
	tests := []struct {
		name, body, want string
	}{
		{name: "duplicate series", want: "two series for sum/cpu",
			body: statMatrix(r, stat{"sum", "cpu", []string{"1", "1", "1", "1"}}, stat{"sum", "cpu", []string{"1", "1", "1", "1"}})},
		{name: "query error", want: "bad_data",
			body: `{"status":"error","errorType":"bad_data","error":"parse error"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := &rangeBackend{t: t}
			b.rules = append([]rangeRule{
				{contains: "kube_pod_container_resource_requests", body: tc.body},
				{contains: "kube_pod_container_resource_limits", body: emptyMatrix},
			}, usageRules(r)...)
			c := newRangeClient(t, b, nil)
			u, err := c.Usage(t.Context(), promhistory.Scope{Kind: promhistory.ScopePod, Namespace: "shop", Name: "web-1"}, r)
			if err != nil {
				t.Fatalf("Usage: %v", err)
			}
			if len(u.CPU) != r.Steps() {
				t.Errorf("CPU = %v, want the usage kept", u.CPU)
			}
			if u.Resources != nil || !strings.Contains(u.ResourcesErr, tc.want) {
				t.Errorf("resources %+v, err %q; want none and %q", u.Resources, u.ResourcesErr, tc.want)
			}
		})
	}
}
