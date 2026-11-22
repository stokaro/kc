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

// WorkloadLogsProvider identifies rows that open a merged logs viewer.
type WorkloadLogsProvider interface {
	WorkloadLogsSpec() WorkloadLogsSpec
	// WorkloadLogsCommand returns the kubectl command that prints the same
	// merged log. selector is the workload's resolved pod selector (unused
	// for a single pod); maxRequests is the stream limit.
	WorkloadLogsCommand(spec WorkloadLogsSpec, selector string, maxRequests int) string
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
func (w *WorkloadLogItem) WorkloadLogsCommand(spec WorkloadLogsSpec, selector string, maxRequests int) string {
	return workloadLogsCommand(w.deps, spec, selector, maxRequests)
}

// workloadLogsCommand prints `kubectl logs` for a merged log: a selector or
// one pod, every container, a prefix per line, the tail per container, the
// stream limit, then follow. kubectl's tail default with a selector is 10,
// so --tail is always printed.
func workloadLogsCommand(deps Deps, spec WorkloadLogsSpec, selector string, maxRequests int) string {
	args := []string{"logs"}
	if spec.IsPod() {
		args = append(args, spec.Pod)
	} else {
		args = append(args, "-l", selector)
	}
	args = append(args, "-n", spec.Namespace, "--all-containers", "--prefix")
	tail := spec.TailLines
	if tail <= 0 {
		tail = DefaultLogsTailLines
	}
	args = append(args, fmt.Sprintf("--tail=%d", tail))
	if maxRequests > 0 {
		args = append(args, fmt.Sprintf("--max-log-requests=%d", maxRequests))
	}
	if spec.Follow {
		args = append(args, "-f")
	}
	return kubectlCommand(deps, args...)
}
