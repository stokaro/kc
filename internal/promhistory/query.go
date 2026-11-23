// Package promhistory reads the past CPU and memory usage of a workload from
// a Prometheus-compatible server that runs in the cluster. It asks through
// the API server's service proxy with the context's own credentials, so it
// holds no credential of its own. Pods are tied to the workload by the
// kube-state-metrics owner series as they were at each point in time, so
// pods that were replaced or deleted inside the window still count.
package promhistory

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Target is the workload whose history is read.
type Target struct {
	// Kind is the owner kind: Deployment, StatefulSet, DaemonSet,
	// ReplicaSet, Job or CronJob.
	Kind      string
	Namespace string
	Name      string
}

func (t Target) String() string { return t.Kind + " " + t.Namespace + "/" + t.Name }

// Kinds lists the workload kinds a Target may name.
var Kinds = []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob"}

var labelNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// selector is the label matchers of every series kc asks for: the backend's
// configured labels plus the extra matchers, in a stable order.
type selector struct {
	labels []string // `name="value"` pairs from the config, sorted
}

func newSelector(labels map[string]string) (selector, error) {
	var s selector
	for name, value := range labels {
		if !labelNameRE.MatchString(name) {
			return selector{}, fmt.Errorf("label %q is not a valid Prometheus label name", name)
		}
		s.labels = append(s.labels, name+"="+quote(value))
	}
	slices.Sort(s.labels)
	return s, nil
}

// with returns the matchers in braces: the configured labels, then extra.
func (s selector) with(extra ...string) string {
	return "{" + strings.Join(append(slices.Clone(s.labels), extra...), ",") + "}"
}

// quote makes a PromQL string literal. PromQL takes Go's escapes in
// double-quoted strings, so a value cannot end the literal.
func quote(v string) string { return strconv.Quote(v) }

func eq(name, value string) string { return name + "=" + quote(value) }

// podSet is a vector with one series per (namespace, pod) of the target,
// value 1, at every point in time where kube-state-metrics tied the pod to
// it. A Deployment's pods go through its ReplicaSets and a CronJob's
// through its Jobs, matched by the owner series of that time.
func podSet(s selector, t Target) (string, error) {
	ns := eq("namespace", t.Namespace)
	direct := func(kind, name string) string {
		return "max by (namespace, pod) (kube_pod_owner" + s.with(ns, eq("owner_kind", kind), eq("owner_name", name)) + ")"
	}
	// through joins the pods owned by any middleKind object with the
	// middle objects owned by the target; middleLabel holds the middle
	// object's name in its own owner series.
	through := func(middleKind, middleSeries, middleLabel string) string {
		middle := "max by (namespace, owner_name) (label_replace(" + middleSeries +
			s.with(ns, eq("owner_kind", t.Kind), eq("owner_name", t.Name)) +
			`, "owner_name", "$1", ` + quote(middleLabel) + `, "(.*)"))`
		return "max by (namespace, pod) (kube_pod_owner" + s.with(ns, eq("owner_kind", middleKind)) +
			" * on (namespace, owner_name) group_left() " + middle + ")"
	}
	switch t.Kind {
	case "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		return direct(t.Kind, t.Name), nil
	case "Deployment":
		return through("ReplicaSet", "kube_replicaset_owner", "replicaset"), nil
	case "CronJob":
		return through("Job", "kube_job_owner", "job_name"), nil
	default:
		return "", fmt.Errorf("no history for kind %q", t.Kind)
	}
}

// queries are the PromQL expressions of one history read.
type queries struct {
	cpu, memory string // range queries, one series each
	pods        string // instant query: pods seen in the window
	owners      string // instant query: are owner series there at all
}

// buildQueries returns the queries for t over window at step. CPU is in
// cores, memory in bytes (working set, as metrics.k8s.io reports it).
func buildQueries(labels map[string]string, t Target, window, step time.Duration) (queries, error) {
	s, err := newSelector(labels)
	if err != nil {
		return queries{}, err
	}
	pods, err := podSet(s, t)
	if err != nil {
		return queries{}, err
	}
	containers := s.with(eq("namespace", t.Namespace), `container!=""`, `container!="POD"`)
	join := " * on (namespace, pod) group_left() " + pods
	// rate needs at least two samples; five minutes covers scrape
	// intervals up to two minutes and a step longer than that.
	rateWindow := max(5*time.Minute, step)
	return queries{
		cpu:    "sum(rate(container_cpu_usage_seconds_total" + containers + "[" + promDuration(rateWindow) + "])" + join + ")",
		memory: "sum(container_memory_working_set_bytes" + containers + join + ")",
		pods:   "count(max_over_time((" + pods + ")[" + promDuration(window) + ":" + promDuration(step) + "]))",
		owners: "count(kube_pod_owner" + s.with(eq("namespace", t.Namespace)) + ")",
	}, nil
}

// promDuration formats d in PromQL's units: whole seconds at least.
func promDuration(d time.Duration) string {
	secs := int64(d / time.Second)
	switch {
	case secs > 0 && secs%3600 == 0:
		return strconv.FormatInt(secs/3600, 10) + "h"
	case secs > 0 && secs%60 == 0:
		return strconv.FormatInt(secs/60, 10) + "m"
	default:
		return strconv.FormatInt(max(secs, 1), 10) + "s"
	}
}

// workloadKinds maps the workload resources to their owner kind.
var workloadKinds = map[[2]string]string{
	{"apps", "deployments"}:  "Deployment",
	{"apps", "statefulsets"}: "StatefulSet",
	{"apps", "daemonsets"}:   "DaemonSet",
	{"apps", "replicasets"}:  "ReplicaSet",
	{"batch", "jobs"}:        "Job",
	{"batch", "cronjobs"}:    "CronJob",
}

// KindFor returns the owner kind of a workload resource: "Deployment" for
// apps/deployments. False for any other group and resource.
func KindFor(group, resource string) (string, bool) {
	k, ok := workloadKinds[[2]string{group, resource}]
	return k, ok
}
