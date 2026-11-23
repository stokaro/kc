package promhistory_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/stokaro/kc/internal/promhistory"
	"github.com/stokaro/kc/pkg/appconfig"
)

const token = "s3cr3t-token-value"

// now is on a minute boundary, so a one hour window at the default 60
// points has steps on whole minutes.
var now = time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)

// backend is a fake Prometheus behind the service proxy path. Each
// handler answers by the metric its query reads.
type backend struct {
	t       *testing.T
	path    string // the expected proxy prefix
	mu      sync.Mutex
	queries []string
	params  []map[string]string
	cpu     string // result JSON of the CPU range query
	mem     string
	pods    string
	owners  string
	status  int    // non-zero: answer every request with it
	body    string // body for status
	block   chan struct{}
}

func matrix(values ...string) string {
	return `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` + strings.Join(values, ",") + `]}]}}`
}

const emptyMatrix = `{"status":"success","data":{"resultType":"matrix","result":[]}}`
const emptyVector = `{"status":"success","data":{"resultType":"vector","result":[]}}`

func vector(v string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"` + v + `"]}]}}`
}

// at is a sample k minutes before now.
func at(minutesBefore int, v string) string {
	return fmt.Sprintf(`[%d,"%s"]`, now.Add(-time.Duration(minutesBefore)*time.Minute).Unix(), v)
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		b.t.Errorf("Authorization = %q, want the kubeconfig bearer token", got)
	}
	q := r.URL.Query()
	b.mu.Lock()
	b.queries = append(b.queries, q.Get("query"))
	p := map[string]string{"path": r.URL.Path}
	for k := range q {
		p[k] = q.Get(k)
	}
	b.params = append(b.params, p)
	b.mu.Unlock()
	if b.block != nil {
		select {
		case <-b.block:
		case <-r.Context().Done():
			return
		}
	}
	if b.status != 0 {
		w.WriteHeader(b.status)
		_, _ = w.Write([]byte(b.body))
		return
	}
	if !strings.HasPrefix(r.URL.Path, b.path) {
		b.t.Errorf("path %q, want prefix %q", r.URL.Path, b.path)
	}
	query := q.Get("query")
	var out string
	switch {
	case strings.Contains(query, "container_cpu_usage_seconds_total"):
		out = b.cpu
	case strings.Contains(query, "container_memory_working_set_bytes"):
		out = b.mem
	case strings.HasPrefix(query, "count(max_over_time("):
		out = b.pods
	case strings.HasPrefix(query, "count(kube_pod_owner"):
		out = b.owners
	default:
		b.t.Errorf("unexpected query %q", query)
	}
	_, _ = w.Write([]byte(out))
}

func newClient(t *testing.T, b *backend, cfg appconfig.MetricsBackendConfig) *promhistory.Client {
	t.Helper()
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	c, err := promhistory.NewServiceProxyClient(&rest.Config{Host: srv.URL, BearerToken: token}, cfg)
	if err != nil {
		t.Fatalf("NewServiceProxyClient: %v", err)
	}
	return c
}

var promBackend = appconfig.MetricsBackendConfig{Namespace: "monitoring", Service: "prometheus-k8s", Port: "9090"}

var web = promhistory.Target{Kind: "Deployment", Namespace: "shop", Name: "web"}

func TestFetchBeforeStartupWithGaps(t *testing.T) {
	t.Parallel()
	b := &backend{
		t:    t,
		path: "/api/v1/namespaces/monitoring/services/prometheus-k8s:9090/proxy/api/v1/",
		// Samples an hour back, long before any kc session; 10..1 minutes
		// before now are missing.
		cpu:    matrix(at(60, "0.5"), at(59, "0.25"), at(11, "1.5"), at(0, "0.75")),
		mem:    matrix(at(60, "1048576"), at(0, "NaN")),
		pods:   vector("7"),
		owners: vector("40"),
	}
	c := newClient(t, b, promBackend)
	h, err := c.Fetch(t.Context(), web, promhistory.Options{Window: time.Hour, Now: now.Add(20 * time.Second)})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !h.Start.Equal(now.Add(-time.Hour)) || !h.End.Equal(now) || h.Step != time.Minute {
		t.Fatalf("window = %v..%v step %v, want an hour back to %v by the minute", h.Start, h.End, h.Step, now)
	}
	if h.Source != "monitoring/prometheus-k8s:9090" {
		t.Errorf("Source = %q", h.Source)
	}
	if got := h.Steps(); got != 61 {
		t.Fatalf("Steps = %d, want 61", got)
	}
	if len(h.CPU) != 4 || !h.CPU[0].At.Equal(now.Add(-time.Hour)) || h.CPU[0].Value != 0.5 {
		t.Fatalf("CPU = %+v, want 4 points from an hour back", h.CPU)
	}
	if got := h.Missing(h.CPU); got != 57 {
		t.Errorf("Missing(CPU) = %d, want 57", got)
	}
	// NaN is a gap, not a zero.
	if len(h.Memory) != 1 || h.Missing(h.Memory) != 60 {
		t.Errorf("Memory = %+v, want the NaN sample dropped", h.Memory)
	}
	if !h.PodsKnown || h.Pods != 7 || h.NoOwners {
		t.Errorf("pods = %d known %v noOwners %v, want 7 known", h.Pods, h.PodsKnown, h.NoOwners)
	}
	cells := h.Buckets(h.CPU, 61)
	if cells[0] != 0.5 || cells[1] != 0.25 || !math.IsNaN(cells[2]) || cells[49] != 1.5 || cells[60] != 0.75 {
		t.Errorf("Buckets = %v, want values at 0, 1, 49, 60 and gaps elsewhere", cells)
	}
	// Folding keeps the peak of each cell and gaps where no step had a
	// point.
	folded := h.Buckets(h.CPU, 6)
	if len(folded) != 6 || folded[0] != 0.5 || !math.IsNaN(folded[1]) || folded[4] != 1.5 || folded[5] != 0.75 {
		t.Errorf("Buckets(6) = %v", folded)
	}
	for _, p := range b.params {
		if strings.HasSuffix(p["path"], "/query_range") {
			if p["start"] != fmt.Sprint(now.Add(-time.Hour).Unix()) || p["end"] != fmt.Sprint(now.Unix()) || p["step"] != "60" {
				t.Errorf("range params = %v", p)
			}
		}
	}
	// The owner check runs only when no series came back.
	if len(b.queries) != 3 {
		t.Errorf("queries = %d, want 3: %q", len(b.queries), b.queries)
	}
}

func TestFetchWithoutSeries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		owners   string
		noOwners bool
	}{
		{name: "owner series missing", owners: emptyVector, noOwners: true},
		{name: "owner series present", owners: vector("12"), noOwners: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := &backend{t: t, path: "/api/", cpu: emptyMatrix, mem: emptyMatrix, pods: emptyVector, owners: tc.owners}
			h, err := newClient(t, b, promBackend).Fetch(t.Context(), web, promhistory.Options{Window: time.Hour, Now: now})
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if h.CPU != nil || h.Memory != nil {
				t.Errorf("series = %v %v, want none (unavailable, not zero)", h.CPU, h.Memory)
			}
			if h.NoOwners != tc.noOwners || h.Pods != 0 || !h.PodsKnown {
				t.Errorf("noOwners %v pods %d known %v", h.NoOwners, h.Pods, h.PodsKnown)
			}
			if len(b.queries) != 4 {
				t.Errorf("queries = %d, want the owner check as the fourth", len(b.queries))
			}
		})
	}
}

// selectorRE finds every vector selector of a query.
var selectorRE = regexp.MustCompile(`([a-z_]+)\{([^}]*)\}`)

func TestQueriesCarryClusterLabels(t *testing.T) {
	t.Parallel()
	for _, kind := range promhistory.Kinds {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			b := &backend{t: t, path: "/api/", cpu: emptyMatrix, mem: emptyMatrix, pods: emptyVector, owners: emptyVector}
			cfg := promBackend
			cfg.Labels = map[string]string{"cluster": "prod-eu", "region": "eu"}
			target := promhistory.Target{Kind: kind, Namespace: "shop", Name: `we"b`}
			if _, err := newClient(t, b, cfg).Fetch(t.Context(), target, promhistory.Options{Window: time.Hour, Now: now}); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			selectors := 0
			for _, q := range b.queries {
				for _, m := range selectorRE.FindAllStringSubmatch(q, -1) {
					selectors++
					// Same-name namespaces of other clusters stay out of
					// every series, the owner joins included.
					if !strings.HasPrefix(m[2], `cluster="prod-eu",region="eu",namespace="shop"`) {
						t.Errorf("selector %s{%s} lacks the cluster labels and namespace", m[1], m[2])
					}
				}
				if strings.Contains(q, `we"b`) {
					t.Errorf("query %q carries the name unescaped", q)
				}
			}
			// Without this, a parser that found nothing would pass.
			if selectors < 5 {
				t.Errorf("found %d selectors in %q", selectors, b.queries)
			}
			joined := strings.Join(b.queries, "\n")
			var want []string
			switch kind {
			case "Deployment":
				want = []string{`kube_replicaset_owner{`, `owner_kind="Deployment"`, `owner_kind="ReplicaSet"`, `"replicaset"`}
			case "CronJob":
				want = []string{`kube_job_owner{`, `owner_kind="CronJob"`, `owner_kind="Job"`, `"job_name"`}
			default:
				want = []string{`owner_kind="` + kind + `"`, `owner_name="we\"b"`}
			}
			for _, w := range want {
				if !strings.Contains(joined, w) {
					t.Errorf("queries lack %s:\n%s", w, joined)
				}
			}
		})
	}
}

func TestServiceProxyPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  appconfig.MetricsBackendConfig
		path string
	}{
		{name: "service only", cfg: appconfig.MetricsBackendConfig{Namespace: "mon", Service: "prom"}, path: "/api/v1/namespaces/mon/services/prom/proxy/api/v1/query"},
		{name: "named port", cfg: appconfig.MetricsBackendConfig{Namespace: "mon", Service: "prom", Port: "web"}, path: "/api/v1/namespaces/mon/services/prom:web/proxy/api/v1/query"},
		{name: "https and prefix", cfg: appconfig.MetricsBackendConfig{Namespace: "mon", Service: "mimir", Scheme: "https", PathPrefix: "/prometheus/"}, path: "/api/v1/namespaces/mon/services/https:mimir:/proxy/prometheus/api/v1/query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := &backend{t: t, path: "/api/", cpu: emptyMatrix, mem: emptyMatrix, pods: emptyVector, owners: vector("1")}
			if _, err := newClient(t, b, tc.cfg).Fetch(t.Context(), web, promhistory.Options{Window: time.Hour, Now: now}); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			found := false
			for _, p := range b.params {
				found = found || p["path"] == tc.path
			}
			if !found {
				t.Errorf("paths %v, want %s", b.params, tc.path)
			}
		})
	}
}

func TestFetchErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "forbidden proxy", status: http.StatusForbidden, body: `{"kind":"Status","message":"services \"prometheus-k8s:9090\" is forbidden: User \"dev\" cannot get resource \"services/proxy\""}`, want: "cannot get resource"},
		{name: "no endpoints", status: http.StatusServiceUnavailable, body: `{"kind":"Status","message":"no endpoints available for service \"prometheus-k8s:9090\""}`, want: "HTTP 503: no endpoints available"},
		{name: "prometheus error", status: http.StatusBadRequest, body: `{"status":"error","errorType":"bad_data","error":"parse error"}`, want: "bad_data: parse error"},
		{name: "not json", status: http.StatusBadGateway, body: `<html>bad gateway</html>`, want: "HTTP 502: Bad Gateway"},
		{name: "too large", status: http.StatusOK, body: strings.Repeat(" ", 4<<20+10), want: "larger than"},
		{name: "too many series", status: http.StatusOK, body: `{"status":"success","data":{"resultType":"vector","result":[` + strings.TrimSuffix(strings.Repeat(`{"metric":{},"value":[1,"1"]},`, 9), ",") + `]}}`, want: "9 series"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := &backend{t: t, status: tc.status, body: tc.body}
			_, err := newClient(t, b, promBackend).Fetch(t.Context(), web, promhistory.Options{Window: time.Hour, Now: now})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("err %q carries the bearer token", err)
			}
		})
	}
}

func TestTransportErrorIsRedacted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	host := srv.URL
	srv.Close()
	c, err := promhistory.NewServiceProxyClient(&rest.Config{Host: host, BearerToken: token}, promBackend)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Fetch(t.Context(), web, promhistory.Options{Window: time.Hour, Now: now})
	if err == nil {
		t.Fatal("Fetch against a closed server succeeded")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "query_range?") {
		t.Errorf("err %q carries the token or the request URL", err)
	}
}

func TestFetchCanceled(t *testing.T) {
	t.Parallel()
	b := &backend{t: t, block: make(chan struct{})}
	c := newClient(t, b, promBackend)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := c.Fetch(ctx, web, promhistory.Options{Window: time.Hour, Now: now})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Fetch did not return after cancel")
	}
	close(b.block)
}

func TestNewServiceProxyClientRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  appconfig.MetricsBackendConfig
	}{
		{name: "no service", cfg: appconfig.MetricsBackendConfig{Namespace: "mon"}},
		{name: "bad scheme", cfg: appconfig.MetricsBackendConfig{Namespace: "mon", Service: "p", Scheme: "ftp"}},
		{name: "bad label", cfg: appconfig.MetricsBackendConfig{Namespace: "mon", Service: "p", Labels: map[string]string{`cluster"}`: "x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := promhistory.NewServiceProxyClient(&rest.Config{Host: "https://127.0.0.1:1"}, tc.cfg); err == nil {
				t.Error("accepted")
			}
		})
	}
	// The control: a valid backend is accepted.
	if _, err := promhistory.NewServiceProxyClient(&rest.Config{Host: "https://127.0.0.1:1"}, promBackend); err != nil {
		t.Errorf("valid backend refused: %v", err)
	}
}

func TestUnknownKind(t *testing.T) {
	t.Parallel()
	b := &backend{t: t}
	_, err := newClient(t, b, promBackend).Fetch(t.Context(), promhistory.Target{Kind: "Pod", Namespace: "a", Name: "b"}, promhistory.Options{Window: time.Hour})
	if err == nil || len(b.queries) != 0 {
		t.Errorf("err = %v after %d queries, want a refusal before any request", err, len(b.queries))
	}
}
