// Package metricsources tells which data sources of a context can answer
// the questions of the Info panel and the dashboard: current usage from
// metrics.k8s.io, and history from a Prometheus-compatible backend with the
// metric families it needs. Each check is one bounded request, run only
// when the user asks for the report.
package metricsources

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/stokaro/kc/internal/promhistory"
)

// State is the answer of one check.
type State int

const (
	// Available: the source answers.
	Available State = iota
	// NoBackend: no Prometheus backend is configured for the context.
	NoBackend
	// Forbidden: the API server refused the request for this user.
	Forbidden
	// Missing: the backend has no series of the metric family.
	Missing
	// Stale: the family has series in the last hour but none now: its
	// exporter stopped or is no longer scraped.
	Stale
	// Mismatch: series exist, but none match the configured labels.
	Mismatch
	// Warning: the source works, with a risk the detail names.
	Warning
	// Failed: the request failed for another reason.
	Failed
)

var stateNames = [...]string{"ok", "no backend", "forbidden", "missing", "stale", "labels match nothing", "warning", "failed"}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "unknown"
}

// Usable reports whether what the check names can be shown.
func (s State) Usable() bool { return s == Available || s == Warning }

// Check is the answer for one source or metric family.
type Check struct {
	// Source is where the data comes from: "metrics.k8s.io" or the
	// backend's "namespace/service:port".
	Source string
	// Name is what was checked: "pod usage", "kube_pod_owner".
	Name string
	// Needs is what kc shows from it.
	Needs string
	State State
	// Detail explains the state in one line.
	Detail string
}

// Family is a Prometheus metric family a view needs.
type Family struct {
	Metric string
	// Exporter is the component that exposes it.
	Exporter string
	// Needs is what kc shows from it.
	Needs string
	// Namespaced is true when the series carry a namespace label, so the
	// check can be limited to the namespace scope.
	Namespaced bool
}

// Families are the metric families kc's history and dashboard read, in the
// order the report lists them.
var Families = []Family{
	{Metric: "kube_pod_owner", Exporter: "kube-state-metrics", Needs: "tying pods to workloads over time (workload history)", Namespaced: true},
	{Metric: "container_cpu_usage_seconds_total", Exporter: "cAdvisor (kubelet)", Needs: "CPU history", Namespaced: true},
	{Metric: "container_memory_working_set_bytes", Exporter: "cAdvisor (kubelet)", Needs: "memory history", Namespaced: true},
	{Metric: "kube_pod_container_resource_requests", Exporter: "kube-state-metrics", Needs: "requests and limits history", Namespaced: true},
	{Metric: "container_cpu_cfs_throttled_periods_total", Exporter: "cAdvisor (kubelet)", Needs: "CPU throttling", Namespaced: true},
	{Metric: "node_cpu_seconds_total", Exporter: "node_exporter", Needs: "node CPU and load history", Namespaced: false},
}

// staleLookback is how far back a family without current samples is
// looked for, to tell a stopped exporter from one that never ran.
const staleLookback = time.Hour

// clusterLabel is the label name multi-cluster setups (Thanos, Mimir,
// kube-prometheus with external labels) commonly use.
const clusterLabel = "cluster"

// Querier is the part of a Prometheus client the checks use.
type Querier interface {
	Scalar(ctx context.Context, expr string, at time.Time) (float64, []string, error)
	Selector(extra ...string) string
	Labels() map[string]string
}

// ProbePrometheus checks each family of Families on q, limited to
// namespace when it is set and the family is namespaced. It runs one to
// three instant count queries per family and stops early when the backend
// itself is refused or unreachable, since every further query would fail
// the same way.
func ProbePrometheus(ctx context.Context, q Querier, source, namespace string, now time.Time) []Check {
	var out []Check
	for _, f := range Families {
		c := probeFamily(ctx, q, source, namespace, f, now)
		out = append(out, c)
		if c.State == Forbidden || c.State == Failed {
			out[len(out)-1].Detail += "; further checks skipped"
			return out
		}
	}
	if len(q.Labels()) == 0 {
		out = append(out, probeClusters(ctx, q, source, now))
	}
	return out
}

func probeFamily(ctx context.Context, q Querier, source, namespace string, f Family, now time.Time) Check {
	c := Check{Source: source, Name: f.Metric, Needs: f.Needs}
	var extra []string
	where := ""
	if f.Namespaced && namespace != "" {
		extra = append(extra, promhistory.Eq("namespace", namespace))
		where = " in namespace " + namespace
	}
	sel := q.Selector(extra...)
	n, _, err := q.Scalar(ctx, "count("+f.Metric+sel+")", now)
	if err != nil {
		return failed(c, err)
	}
	if n > 0 {
		c.State, c.Detail = Available, fmt.Sprintf("%d series%s", int64(n), where)
		return c
	}
	recent, _, err := q.Scalar(ctx, "count(last_over_time("+f.Metric+sel+"["+promDuration(staleLookback)+"]))", now)
	if err != nil {
		return failed(c, err)
	}
	if recent > 0 {
		c.State = Stale
		c.Detail = fmt.Sprintf("%d series%s in the last hour, none now: %s stopped or is no longer scraped", int64(recent), where, f.Exporter)
		return c
	}
	if len(q.Labels()) > 0 {
		all, _, err := q.Scalar(ctx, "count("+f.Metric+promhistory.UnlabeledSelector(extra...)+")", now)
		if err == nil && all > 0 {
			c.State = Mismatch
			c.Detail = fmt.Sprintf("%d series%s, but none with labels %s: check the backend's labels", int64(all), where, labelText(q.Labels()))
			return c
		}
	}
	c.State = Missing
	c.Detail = "no series" + where + ": needs " + f.Exporter
	return c
}

// probeClusters warns when a backend without configured labels holds
// several clusters: objects with the same names in another cluster would
// be summed in.
func probeClusters(ctx context.Context, q Querier, source string, now time.Time) Check {
	c := Check{Source: source, Name: "cluster labels", Needs: "keeping other clusters' series out"}
	n, _, err := q.Scalar(ctx, "count(count by ("+clusterLabel+") (kube_pod_owner"+q.Selector()+"))", now)
	switch {
	case err != nil:
		return failed(c, err)
	case n > 1:
		c.State = Warning
		c.Detail = fmt.Sprintf("the backend holds %d values of label %q; set labels so that other clusters are not mixed in", int64(n), clusterLabel)
	default:
		c.State, c.Detail = Available, "no labels configured; the backend holds at most one value of label "+fmt.Sprintf("%q", clusterLabel)
	}
	return c
}

func failed(c Check, err error) Check {
	c.State, c.Detail = Failed, err.Error()
	if errors.Is(err, promhistory.ErrForbidden) {
		c.State = Forbidden
	}
	return c
}

// labelText is {a="b", c="d"} in a stable order.
func labelText(labels map[string]string) string {
	return promhistory.UnlabeledSelector(sortedMatchers(labels)...)
}

func sortedMatchers(labels map[string]string) []string {
	out := make([]string, 0, len(labels))
	for k, v := range labels {
		out = append(out, promhistory.Eq(k, v))
	}
	slices.Sort(out)
	return out
}

func promDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%ds", d/time.Second)
}

// NoBackendCheck is the report line of a context without a backend.
func NoBackendCheck(contextName string) Check {
	return Check{
		Source: "Prometheus",
		Name:   "backend",
		Needs:  "CPU and memory history, requests and limits history, throttling",
		State:  NoBackend,
		Detail: "no metricsHistory backend matches context " + contextName + ": enter the Service of a Prometheus-compatible server",
	}
}

// Text renders checks as aligned lines: state, name, detail.
func Text(checks []Check) []string {
	stateW, nameW := columnWidths(checks)
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, fmt.Sprintf("%-*s  %-*s  %s", stateW, c.State, nameW, c.Name, c.Detail))
	}
	return out
}

// DetailColumn is the column where Text starts the details of checks.
func DetailColumn(checks []Check) int {
	stateW, nameW := columnWidths(checks)
	return stateW + 2 + nameW + 2
}

func columnWidths(checks []Check) (stateW, nameW int) {
	for _, c := range checks {
		stateW, nameW = max(stateW, len(c.State.String())), max(nameW, len(c.Name))
	}
	return stateW, nameW
}

// Request is one report: the context, the namespace scope ("" for all),
// the Kubernetes side and the backend (nil when none is configured).
type Request struct {
	Context   string
	Namespace string
	Kube      Kube
	Backend   Querier
	// Source names the backend in the report.
	Source string
	Now    time.Time
}

// Run builds the report of r: the Kubernetes checks first, then the
// backend's. ctx bounds the whole report.
func Run(ctx context.Context, r Request) []Check {
	out := ProbeKube(ctx, r.Kube, r.Namespace)
	if r.Backend == nil {
		return append(out, NoBackendCheck(r.Context))
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	return append(out, ProbePrometheus(ctx, r.Backend, r.Source, r.Namespace, now)...)
}
