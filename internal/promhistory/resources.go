package promhistory

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Resources is the history of the requests, limits and allocatable of a
// scope, from kube-state-metrics: what the pods asked for at each step,
// not today's spec drawn backward. It needs kube-state-metrics 2 or later
// (kube_pod_container_resource_requests with a resource label).
type Resources struct {
	CPU, Memory ResourceHistory
	// Containers is how many containers the pods that had not terminated
	// had at each step (kube_pod_container_info). Empty when
	// kube-state-metrics has no container series in the range.
	Containers []Point
}

// ResourceHistory is one resource, in cores or bytes.
type ResourceHistory struct {
	// Requests and Limits sum the containers of the pods that were Pending
	// or Running at each step, like the live sums. Requested and Limited
	// count the containers that set the resource then.
	Requests, Limits   []Point
	Requested, Limited []Point
	// Capacity is the allocatable of the node of a node scope, or of every
	// node for all namespaces; nil for other scopes.
	Capacity []Point
}

// Known reports whether kube-state-metrics had container series in the
// range: without them no request means "none set".
func (r *Resources) Known() bool { return r != nil && len(r.Containers) > 0 }

// FullLimits returns the limits per step, zero at the steps where a
// container had no limit for the resource: a sum missing a container is
// no ceiling. A step without samples stays without a point.
func (r *Resources) FullLimits(h ResourceHistory) []Point {
	if r == nil {
		return nil
	}
	containers := make(map[int64]float64, len(r.Containers))
	for _, p := range r.Containers {
		containers[p.At.Unix()] = p.Value
	}
	limited := make(map[int64]float64, len(h.Limited))
	for _, p := range h.Limited {
		limited[p.At.Unix()] = p.Value
	}
	var out []Point
	for _, p := range h.Limits {
		if n := containers[p.At.Unix()]; n == 0 || limited[p.At.Unix()] < n {
			p.Value = 0
		}
		out = append(out, p)
	}
	return out
}

// Statistic labels of the resource queries: each query returns several
// series told apart by "stat" and "resource".
const (
	statSum        = "sum"
	statCount      = "count"
	statContainers = "containers"
	statCapacity   = "capacity"
)

// resourceQueries are the two range queries of a resources read.
type resourceQueries struct{ requests, limits string }

// buildResourceQueries returns the queries of scope. A pod counts while it
// is Pending or Running, as the live sums count pods that have not
// terminated.
func buildResourceQueries(s selector, scope Scope) (resourceQueries, error) {
	const phase = `phase=~"Pending|Running"`
	var where []string
	var pods string
	capacity := ""
	switch scope.Kind {
	case ScopeNamespace:
		if scope.Namespace != "" {
			where = []string{eq("namespace", scope.Namespace)}
		} else {
			capacity = "sum by (resource) (kube_node_status_allocatable" + s.with(`resource=~"cpu|memory"`) + ")"
		}
		pods = "max by (namespace, pod) (kube_pod_status_phase" + s.with(append(slices.Clone(where), phase)...) + " == 1)"
	case ScopePod:
		where = []string{eq("namespace", scope.Namespace), eq("pod", scope.Name)}
		pods = "max by (namespace, pod) (kube_pod_status_phase" + s.with(append(slices.Clone(where), phase)...) + " == 1)"
	case ScopeWorkload:
		set, err := podSet(s, scope.Workload)
		if err != nil {
			return resourceQueries{}, err
		}
		where = []string{eq("namespace", scope.Workload.Namespace)}
		pods = "(" + set + " and on (namespace, pod) (kube_pod_status_phase" + s.with(eq("namespace", scope.Workload.Namespace), phase) + " == 1))"
	case ScopeNode:
		if scope.Name == "" {
			return resourceQueries{}, fmt.Errorf("no node")
		}
		set := "max by (namespace, pod) (kube_pod_info" + s.with(eq("node", scope.Name)) + ")"
		pods = "(" + set + " and on (namespace, pod) (kube_pod_status_phase" + s.with(phase) + " == 1))"
		capacity = "sum by (resource) (kube_node_status_allocatable" + s.with(eq("node", scope.Name), `resource=~"cpu|memory"`) + ")"
	default:
		return resourceQueries{}, fmt.Errorf("unknown scope kind %d", scope.Kind)
	}
	join := " * on (namespace, pod) group_left() " + pods
	of := func(metric string) string {
		return "(" + metric + s.with(append(slices.Clone(where), `resource=~"cpu|memory"`)...) + join + ")"
	}
	stat := func(expr, name string) string {
		return `label_replace(` + expr + `, "stat", "` + name + `", "", "")`
	}
	set := func(metric string, extra ...string) string {
		x := of(metric)
		parts := append([]string{
			stat("sum by (resource) "+x, statSum),
			stat("count by (resource) "+x, statCount),
		}, extra...)
		return strings.Join(parts, " or ")
	}
	containers := stat("count("+"kube_pod_container_info"+s.with(where...)+join+")", statContainers)
	extra := []string{containers}
	if capacity != "" {
		extra = append(extra, stat(capacity, statCapacity))
	}
	return resourceQueries{
		requests: set("kube_pod_container_resource_requests", extra...),
		limits:   set("kube_pod_container_resource_limits"),
	}, nil
}

// resources reads the requests and limits of scope over r: two range
// queries.
func (c *Client) resources(ctx context.Context, sel selector, scope Scope, r Range) (*Resources, []string, error) {
	q, err := buildResourceQueries(sel, scope)
	if err != nil {
		return nil, nil, err
	}
	req, w1, err := c.rangeStats(ctx, q.requests, r)
	if err != nil {
		return nil, w1, err
	}
	lim, w2, err := c.rangeStats(ctx, q.limits, r)
	if err != nil {
		return nil, append(w1, w2...), err
	}
	out := &Resources{
		CPU: ResourceHistory{
			Requests: req[statSum+"/cpu"], Requested: req[statCount+"/cpu"],
			Limits: lim[statSum+"/cpu"], Limited: lim[statCount+"/cpu"],
			Capacity: req[statCapacity+"/cpu"],
		},
		Memory: ResourceHistory{
			Requests: req[statSum+"/memory"], Requested: req[statCount+"/memory"],
			Limits: lim[statSum+"/memory"], Limited: lim[statCount+"/memory"],
			Capacity: req[statCapacity+"/memory"],
		},
		Containers: req[statContainers+"/"],
	}
	return out, append(w1, w2...), nil
}

// rangeStats runs a range query whose series carry "stat" and, but for
// counts of every container, "resource", and returns the finite points of
// each by "stat/resource". Two series with one key fail the read: the
// configured labels do not single out one kube-state-metrics.
func (c *Client) rangeStats(ctx context.Context, expr string, r Range) (map[string][]Point, []string, error) {
	ss, warnings, err := c.queryRange(ctx, expr, r.Start, r.End, r.Step)
	if err != nil {
		return nil, warnings, err
	}
	out := make(map[string][]Point, len(ss))
	for _, s := range ss {
		key := s.metric["stat"] + "/" + s.metric["resource"]
		if _, dup := out[key]; dup {
			return nil, warnings, fmt.Errorf("prometheus %s: two series for %s", c.Source, key)
		}
		out[key] = finitePoints([]series{s})
	}
	return out, warnings, nil
}
