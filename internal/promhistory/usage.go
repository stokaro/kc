package promhistory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// ScopeKind is what a usage read sums over.
type ScopeKind int

const (
	// ScopeNamespace sums the pods of one namespace, or of every
	// namespace when Namespace is empty.
	ScopeNamespace ScopeKind = iota
	// ScopeWorkload sums the pods the owner series tie to a workload.
	ScopeWorkload
	// ScopePod is one pod.
	ScopePod
	// ScopeNode sums the pods kube_pod_info places on a node.
	ScopeNode
)

// Scope names what a usage read covers.
type Scope struct {
	Kind ScopeKind
	// Workload is the target of ScopeWorkload.
	Workload Target
	// Namespace is the namespace of every kind but ScopeNode; Name is the
	// pod of ScopePod and the node of ScopeNode.
	Namespace, Name string
	// UID and Since are the identity and creation time of the live object
	// (zero when unknown or for a namespace scope). A read never reaches
	// before Since: samples from before belong to an earlier object of
	// the same name, which owner series cannot tell from this one.
	UID   string
	Since time.Time
}

func (s Scope) String() string {
	switch s.Kind {
	case ScopeWorkload:
		return s.Workload.String()
	case ScopePod:
		return "pod " + s.Namespace + "/" + s.Name
	case ScopeNode:
		return "node " + s.Name
	default:
		if s.Namespace == "" {
			return "all namespaces"
		}
		return "namespace " + s.Namespace
	}
}

// key is s in a form fit for a cache key; it carries the UID, so a
// recreated object never shares a result with the one before it.
func (s Scope) key() string {
	return fmt.Sprintf("%d|%s|%s|%s|%s|%d", s.Kind, s.Workload, s.Namespace, s.Name, s.UID, s.Since.Unix())
}

// minRateWindow is the shortest window CPU rates are taken over: rate
// needs two samples, and five minutes covers scrape intervals up to two
// minutes. A step longer than that is the window itself, so every sample
// counts once.
const minRateWindow = 5 * time.Minute

// peakSamples bounds how many sub-steps a memory peak looks at per step.
const peakSamples = 10

// Usage is the CPU and memory of a scope over a range.
type Usage struct {
	Scope Scope
	// Asked is the range the caller asked for; Range is the part that was
	// read. Range starts later when the object was created inside the
	// asked range (Clipped is then true).
	Asked, Range Range
	Clipped      bool
	// CPU is in cores: the average of each RateWindow ending at a step.
	// Its peak is the largest such average, never a burst inside one.
	CPU        []Point
	RateWindow time.Duration
	// Memory is the working set in bytes at each step. MemoryPeak is the
	// largest working set within each step, sampled every PeakResolution;
	// it equals Memory when the step is the finest resolution.
	Memory, MemoryPeak []Point
	PeakResolution     time.Duration
	// Measured is how many pods had usage samples at each step; Expected
	// how many running pods the scope had then, from kube-state-metrics.
	// Expected is nil when it cannot be read.
	Measured, Expected []Point
	Warnings           []string
}

// Coverage is how much of a range the measured pods cover.
type Coverage struct {
	// Steps is the number of steps of the range.
	Steps int
	// Full is the steps where every expected pod was measured, Partial
	// those where some but not all were, Empty those without a sample.
	Full, Partial, Empty int
	// Known is false when the expected pods could not be read: then a
	// step with samples is neither full nor partial.
	Known bool
}

// Complete reports whether every step with expected pods was fully
// measured.
func (c Coverage) Complete() bool { return c.Known && c.Partial == 0 && c.Empty == 0 }

// Coverage compares Measured with Expected step by step. A step where
// kube-state-metrics expects no running pod and none was measured is
// full: nothing ran, nothing is missing.
func (u Usage) Coverage() Coverage {
	n := u.Range.Steps()
	c := Coverage{Steps: n, Known: u.Expected != nil}
	measured, expected := u.Range.index(u.Measured), u.Range.index(u.Expected)
	for i := range n {
		m, hasM := measured[i]
		e, hasE := expected[i]
		switch {
		case !hasM || m == 0:
			if c.Known && (!hasE || e == 0) {
				c.Full++
			} else {
				c.Empty++
			}
		case !c.Known:
		case m >= e:
			c.Full++
		default:
			c.Partial++
		}
	}
	return c
}

// index maps each point to its step; points off the range are dropped.
func (r Range) index(ps []Point) map[int]float64 {
	n := r.Steps()
	out := make(map[int]float64, len(ps))
	for _, p := range ps {
		i := int((p.At.Sub(r.Start) + r.Step/2) / r.Step)
		if p.At.Before(r.Start.Add(-r.Step/2)) || i >= n {
			continue
		}
		out[i] = p.Value
	}
	return out
}

// usageQueries are the range queries of one usage read.
type usageQueries struct {
	cpu, memory, peak, measured, expected string
	rateWindow, peakResolution            time.Duration
}

// buildUsageQueries returns the queries of scope over r. Every selector
// carries the configured labels.
func buildUsageQueries(s selector, scope Scope, r Range) (usageQueries, error) {
	var where []string
	join := ""
	running := "(kube_pod_status_phase" + s.with(eq("phase", "Running")) + " == 1)"
	expectedSet := ""
	switch scope.Kind {
	case ScopeNamespace:
		if scope.Namespace != "" {
			where = []string{eq("namespace", scope.Namespace)}
			running = "(kube_pod_status_phase" + s.with(eq("namespace", scope.Namespace), eq("phase", "Running")) + " == 1)"
		}
		expectedSet = running
	case ScopePod:
		where = []string{eq("namespace", scope.Namespace), eq("pod", scope.Name)}
		expectedSet = "(kube_pod_status_phase" + s.with(eq("namespace", scope.Namespace), eq("pod", scope.Name), eq("phase", "Running")) + " == 1)"
	case ScopeWorkload:
		set, err := podSet(s, scope.Workload)
		if err != nil {
			return usageQueries{}, err
		}
		where = []string{eq("namespace", scope.Workload.Namespace)}
		join = " * on (namespace, pod) group_left() " + set
		running = "(kube_pod_status_phase" + s.with(eq("namespace", scope.Workload.Namespace), eq("phase", "Running")) + " == 1)"
		expectedSet = "(" + set + " and on (namespace, pod) " + running + ")"
	case ScopeNode:
		if scope.Name == "" {
			return usageQueries{}, errors.New("no node")
		}
		set := "max by (namespace, pod) (kube_pod_info" + s.with(eq("node", scope.Name)) + ")"
		join = " * on (namespace, pod) group_left() " + set
		expectedSet = "(" + set + " and on (namespace, pod) " + running + ")"
	default:
		return usageQueries{}, fmt.Errorf("unknown scope kind %d", scope.Kind)
	}
	containers := s.with(append(where, `container!=""`, `container!="POD"`)...)
	q := usageQueries{rateWindow: max(minRateWindow, r.Step), peakResolution: r.Step}
	q.cpu = "sum(rate(container_cpu_usage_seconds_total" + containers + "[" + promDuration(q.rateWindow) + "])" + join + ")"
	q.memory = "sum(container_memory_working_set_bytes" + containers + join + ")"
	q.peak = q.memory
	if r.Step > minStep {
		q.peakResolution = max(minStep, (r.Step / peakSamples).Truncate(time.Second))
		q.peak = "max_over_time((" + q.memory + ")[" + promDuration(r.Step) + ":" + promDuration(q.peakResolution) + "])"
	}
	q.measured = "count(sum by (namespace, pod) (container_memory_working_set_bytes" + containers + join + "))"
	q.expected = "count(" + expectedSet + ")"
	return q, nil
}

// Usage reads the CPU, memory, memory peaks and contributor counts of
// scope over r: five range queries, at most three at a time. A failed
// usage query fails the read; failed contributor counts leave Expected or
// Measured nil.
func (c *Client) Usage(ctx context.Context, scope Scope, r Range) (Usage, error) {
	u := Usage{Scope: scope, Asked: r, Range: r}
	if !scope.Since.IsZero() && scope.Since.After(r.Start) {
		u.Clipped = true
		start := scope.Since.Truncate(r.Step)
		if start.Before(scope.Since) {
			start = start.Add(r.Step)
		}
		u.Range.Start = start
		if !u.Range.Start.Before(u.Range.End) {
			// Created within the last step: nothing to read yet.
			u.Range.Start = u.Range.End
			return u, nil
		}
	}
	if !u.Range.Valid() {
		return u, fmt.Errorf("empty range %v..%v", u.Range.Start, u.Range.End)
	}
	sel, err := newSelector(c.labels)
	if err != nil {
		return u, err
	}
	q, err := buildUsageQueries(sel, scope, u.Range)
	if err != nil {
		return u, err
	}
	u.RateWindow, u.PeakResolution = q.rateWindow, q.peakResolution
	var warnings [5][]string
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(3)
	g.Go(func() (err error) { u.CPU, warnings[0], err = c.rangeSeries(gctx, q.cpu, u.Range); return err })
	g.Go(func() (err error) { u.Memory, warnings[1], err = c.rangeSeries(gctx, q.memory, u.Range); return err })
	g.Go(func() (err error) { u.MemoryPeak, warnings[2], err = c.rangeSeries(gctx, q.peak, u.Range); return err })
	g.Go(func() error {
		pts, w, err := c.rangeSeries(gctx, q.measured, u.Range)
		if err == nil {
			u.Measured, warnings[3] = nonNil(pts), w
		}
		return nil
	})
	g.Go(func() error {
		pts, w, err := c.rangeSeries(gctx, q.expected, u.Range)
		if err == nil {
			u.Expected, warnings[4] = nonNil(pts), w
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return Usage{Scope: scope, Asked: r, Range: u.Range, Clipped: u.Clipped}, err
	}
	u.Warnings = dedupe(warnings[:]...)
	return u, nil
}

// nonNil keeps an empty result apart from a failed read: a count over
// nothing returns no series, which is a known zero.
func nonNil(ps []Point) []Point {
	if ps == nil {
		return make([]Point, 0)
	}
	return ps
}

// UsageKey is the cache key of a usage read of scope over r on c's
// backend: identical keys may share one read (see Shared).
func (c *Client) UsageKey(scope Scope, r Range) string {
	labels := make([]string, 0, len(c.labels))
	for k, v := range c.labels {
		labels = append(labels, k+"="+v)
	}
	slices.Sort(labels)
	return strings.Join([]string{c.base, strings.Join(labels, ","), "usage", scope.key(), r.key()}, "|")
}
