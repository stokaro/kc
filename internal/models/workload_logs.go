package models

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// WorkloadLogsSpec describes a merged log stream: every container of the
// pods a workload selects, or every container of one pod.
type WorkloadLogsSpec struct {
	Namespace string
	// Workload is the object whose spec.selector picks the pods. It is the
	// zero value for a single pod.
	Workload     schema.GroupVersionResource
	WorkloadName string
	// Pod is the one pod whose containers are streamed when Workload is
	// the zero value.
	Pod       string
	Follow    bool
	TailLines int64
}

// IsPod reports whether the spec streams one pod rather than a workload.
func (s WorkloadLogsSpec) IsPod() bool { return s.Workload.Resource == "" }

// LogsSpec returns the stream options of spec as a LogsSpec, the type the
// logs viewer changes in F2. Container stays empty: the merged stream sets
// it per target.
func (s WorkloadLogsSpec) LogsSpec() LogsSpec {
	return LogsSpec{Namespace: s.Namespace, Pod: s.Pod, Follow: s.Follow, TailLines: s.TailLines}
}

// WorkloadLogsProvider identifies rows that open a merged logs viewer.
type WorkloadLogsProvider interface {
	WorkloadLogsSpec() WorkloadLogsSpec
	// WorkloadLogsCommand returns the kubectl command that prints the same
	// merged log with the stream options of logs. selector is the
	// workload's resolved pod selector (unused for a single pod);
	// maxRequests is the stream limit.
	WorkloadLogsCommand(spec WorkloadLogsSpec, logs LogsSpec, selector string, maxRequests int) string
}

// workloadLogsGVRs are the workloads with a spec.selector label selector
// that get a logs row.
var workloadLogsGVRs = map[schema.GroupVersionResource]bool{
	{Group: "apps", Version: "v1", Resource: "deployments"}:  true,
	{Group: "apps", Version: "v1", Resource: "statefulsets"}: true,
	{Group: "apps", Version: "v1", Resource: "daemonsets"}:   true,
	{Group: "apps", Version: "v1", Resource: "replicasets"}:  true,
	{Group: "batch", Version: "v1", Resource: "jobs"}:        true,
}

// WorkloadLogsSupported reports whether objects of gvr get a logs row.
func WorkloadLogsSupported(gvr schema.GroupVersionResource) bool {
	return workloadLogsGVRs[gvr]
}

// WorkloadLogItem is the logs row of a workload or the all-containers logs
// row of a pod.
type WorkloadLogItem struct {
	*SimpleItem
	deps Deps
	spec WorkloadLogsSpec
}

// NewWorkloadLogItem returns a merged logs row for spec.
func NewWorkloadLogItem(deps Deps, id string, cells []string, path []string, spec WorkloadLogsSpec) *WorkloadLogItem {
	item := NewSimpleItem(id, cells, path, GreenStyle())
	return &WorkloadLogItem{SimpleItem: item, deps: deps, spec: spec}
}

// WorkloadLogsSpec returns the spec of the row.
func (w *WorkloadLogItem) WorkloadLogsSpec() WorkloadLogsSpec { return w.spec }

// WorkloadLogsCommand returns the kubectl command for spec in this row's
// context.
func (w *WorkloadLogItem) WorkloadLogsCommand(spec WorkloadLogsSpec, logs LogsSpec, selector string, maxRequests int) string {
	return workloadLogsCommand(w.deps, spec, logs, selector, maxRequests)
}

// workloadLogsCommand prints `kubectl logs` for a merged log: a selector or
// one pod, every container, a prefix per line, the options of logs as
// logsCommand prints them, the stream limit, then follow. kubectl's tail
// default with a selector is 10, so --tail is always printed. Keep the
// options in step with LogsSpec.PodLogOptions.
func workloadLogsCommand(deps Deps, spec WorkloadLogsSpec, logs LogsSpec, selector string, maxRequests int) string {
	args := []string{"logs"}
	if spec.IsPod() {
		args = append(args, spec.Pod)
	} else {
		args = append(args, "-l", selector)
	}
	args = append(args, "-n", spec.Namespace, "--all-containers", "--prefix")
	args = append(args, fmt.Sprintf("--tail=%d", logs.EffectiveTailLines()))
	if since := FormatLogsSince(logs.SinceSeconds); since != "" {
		args = append(args, "--since="+since)
	}
	if logs.Timestamps {
		args = append(args, "--timestamps")
	}
	if logs.Previous {
		args = append(args, "-p")
	}
	if maxRequests > 0 {
		args = append(args, fmt.Sprintf("--max-log-requests=%d", maxRequests))
	}
	if logs.Follow {
		args = append(args, "-f")
	}
	return kubectlCommand(deps, args...)
}
