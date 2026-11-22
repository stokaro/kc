package models

import (
	"fmt"
	"math"

	"github.com/sttts/kc/internal/tablecache"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var nodeGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}

// usageKind is the kind of object a usage column describes.
type usageKind int

const (
	usagePods usageKind = iota + 1
	usageNodes
)

func (k usageKind) String() string {
	switch k {
	case usagePods:
		return "pods"
	case usageNodes:
		return "nodes"
	}
	return "unknown"
}

// usageKindFor returns the usage kind for a list of gvr, or 0 when the
// resource has no usage columns.
func usageKindFor(gvr schema.GroupVersionResource) usageKind {
	switch gvr {
	case podGVR:
		return usagePods
	case nodeGVR:
		return usageNodes
	}
	return 0
}

// usageNA marks a value kc cannot compute: no metrics for the object, no
// request or limit to compare with, or metrics that cannot be read. The sort
// from #37 treats it as a missing cell.
const usageNA = "n/a"

// usageColumn is one appended column. Priority 1 columns show only in wide
// mode, like the server's own wide columns.
type usageColumn struct {
	name     string
	priority int32
	cell     func(usageValues) string
}

// usageValues is what one poll knows about one object. CPU is in millicores,
// memory in bytes. A zero request, limit or allocatable means "not set".
type usageValues struct {
	cpu, mem       int64
	cpuReq, memReq int64
	cpuLim, memLim int64
}

var podUsageColumns = []usageColumn{
	{name: "CPU", cell: func(v usageValues) string { return formatMilliCPU(v.cpu) }},
	{name: "MEM", cell: func(v usageValues) string { return formatMemory(v.mem) }},
	{name: "%CPU/R", cell: func(v usageValues) string { return formatPercent(v.cpu, v.cpuReq) }},
	{name: "%CPU/L", priority: 1, cell: func(v usageValues) string { return formatPercent(v.cpu, v.cpuLim) }},
	{name: "%MEM/R", priority: 1, cell: func(v usageValues) string { return formatPercent(v.mem, v.memReq) }},
	{name: "%MEM/L", cell: func(v usageValues) string { return formatPercent(v.mem, v.memLim) }},
}

// Node columns compare usage with allocatable, which usageValues carries in
// the request fields.
var nodeUsageColumns = []usageColumn{
	{name: "CPU", cell: func(v usageValues) string { return formatMilliCPU(v.cpu) }},
	{name: "%CPU", cell: func(v usageValues) string { return formatPercent(v.cpu, v.cpuReq) }},
	{name: "CPU/A", priority: 1, cell: func(v usageValues) string { return formatMilliCPU(v.cpuReq) }},
	{name: "MEM", cell: func(v usageValues) string { return formatMemory(v.mem) }},
	{name: "%MEM", cell: func(v usageValues) string { return formatPercent(v.mem, v.memReq) }},
	{name: "MEM/A", priority: 1, cell: func(v usageValues) string { return formatMemory(v.memReq) }},
}

func usageColumnsFor(kind usageKind) []usageColumn {
	switch kind {
	case usagePods:
		return podUsageColumns
	case usageNodes:
		return nodeUsageColumns
	}
	return nil
}

// formatMilliCPU writes CPU always in millicores ("250m", "1500m"), so a
// column holds one unit and sorts as a quantity.
func formatMilliCPU(milli int64) string {
	return fmt.Sprintf("%dm", milli)
}

// formatMemory writes memory always in MiB ("128Mi"), as k9s does.
func formatMemory(bytes int64) string {
	return fmt.Sprintf("%dMi", bytes/(1<<20))
}

// formatPercent writes used as a whole percent of total, without a % sign:
// the column title carries it, and a plain number sorts as a number.
func formatPercent(used, total int64) string {
	if total <= 0 {
		return usageNA
	}
	return fmt.Sprint(int64(math.Round(float64(used) * 100 / float64(total))))
}

// usageObjectKey returns the snapshot key of an object.
func usageObjectKey(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// mergeUsageColumns appends the usage columns of kind to rl and returns the
// result. It never writes into rl, whose rows may be shared with the cache.
// A nil snapshot, or one taken where metrics.k8s.io is not served, returns rl
// unchanged. A stale or failed snapshot keeps the columns with empty or n/a
// cells, so they do not come and go between polls.
//
// namespace is the list's namespace, used for rows that do not carry one.
func mergeUsageColumns(rl *tablecache.RowList, kind usageKind, namespace string, snap *usageSnapshot, fresh bool) *tablecache.RowList {
	cols := usageColumnsFor(kind)
	if rl == nil || len(cols) == 0 || snap == nil || !snap.served {
		return rl
	}
	out := *rl
	out.Columns = make([]metav1.TableColumnDefinition, 0, len(rl.Columns)+len(cols))
	out.Columns = append(out.Columns, rl.Columns...)
	for _, c := range cols {
		out.Columns = append(out.Columns, metav1.TableColumnDefinition{
			Name:     c.name,
			Type:     "string",
			Priority: c.priority,
		})
	}
	width := len(rl.Columns)
	out.Items = make([]tablecache.Row, len(rl.Items))
	for i := range rl.Items {
		row := rl.Items[i]
		cells := make([]interface{}, width, width+len(cols))
		copy(cells, row.Cells)
		// Nodes are keyed by name only: tablecache may guess a namespace
		// for cluster-scoped rows from their second cell.
		ns := ""
		if kind == usagePods {
			ns = row.Namespace
			if ns == "" {
				ns = namespace
			}
		}
		vals, ok := snap.values[usageObjectKey(ns, rowName(&row))]
		for _, c := range cols {
			switch {
			case !fresh:
				cells = append(cells, "")
			case snap.err != nil || !ok:
				cells = append(cells, usageNA)
			default:
				cells = append(cells, c.cell(vals))
			}
		}
		row.Cells = cells
		out.Items[i] = row
	}
	return &out
}

// containerResources sums CPU (millicores) and memory (bytes) for one field
// ("requests" or "limits") over the containers a pod runs for its whole life:
// the regular containers and the restartable init containers (sidecars).
// Metrics cover the same containers, so usage compares like with like.
// complete is false when a counted container does not set the resource,
// which for limits means the pod has none.
func containerResources(pod *unstructured.Unstructured, field string) (cpu, mem int64, cpuComplete, memComplete bool) {
	cpuComplete, memComplete = true, true
	add := func(c map[string]interface{}) {
		res, _, _ := unstructured.NestedStringMap(c, "resources", field)
		if q, err := resource.ParseQuantity(res["cpu"]); err == nil {
			cpu += q.MilliValue()
		} else {
			cpuComplete = false
		}
		if q, err := resource.ParseQuantity(res["memory"]); err == nil {
			mem += q.Value()
		} else {
			memComplete = false
		}
	}
	containers, _, _ := unstructured.NestedSlice(pod.Object, "spec", "containers")
	for _, c := range containers {
		if m, ok := c.(map[string]interface{}); ok {
			add(m)
		}
	}
	inits, _, _ := unstructured.NestedSlice(pod.Object, "spec", "initContainers")
	for _, c := range inits {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if policy, _, _ := unstructured.NestedString(m, "restartPolicy"); policy == "Always" {
			add(m)
		}
	}
	return cpu, mem, cpuComplete, memComplete
}

// podAllocation returns the requests and limits a pod's usage is compared
// with. Missing requests count as zero; a limit counts only when every
// container sets it, because one unlimited container leaves the pod unlimited.
func podAllocation(pod *unstructured.Unstructured) usageValues {
	var v usageValues
	if pod == nil {
		return v
	}
	v.cpuReq, v.memReq, _, _ = containerResources(pod, "requests")
	cpuLim, memLim, cpuOK, memOK := containerResources(pod, "limits")
	if cpuOK {
		v.cpuLim = cpuLim
	}
	if memOK {
		v.memLim = memLim
	}
	return v
}

// nodeAllocatable returns a node's allocatable CPU and memory in the request
// fields of usageValues.
func nodeAllocatable(node *unstructured.Unstructured) usageValues {
	var v usageValues
	if node == nil {
		return v
	}
	alloc, _, _ := unstructured.NestedStringMap(node.Object, "status", "allocatable")
	if q, err := resource.ParseQuantity(alloc["cpu"]); err == nil {
		v.cpuReq = q.MilliValue()
	}
	if q, err := resource.ParseQuantity(alloc["memory"]); err == nil {
		v.memReq = q.Value()
	}
	return v
}

// usageFromMap reads cpu and memory from a metrics usage map.
func usageFromMap(usage map[string]string) (cpu, mem int64) {
	if q, err := resource.ParseQuantity(usage["cpu"]); err == nil {
		cpu = q.MilliValue()
	}
	if q, err := resource.ParseQuantity(usage["memory"]); err == nil {
		mem = q.Value()
	}
	return cpu, mem
}

// podMetricsUsage sums the container usage of one PodMetrics object.
func podMetricsUsage(pm *unstructured.Unstructured) (cpu, mem int64) {
	containers, _, _ := unstructured.NestedSlice(pm.Object, "containers")
	for _, c := range containers {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		usage, _, _ := unstructured.NestedStringMap(m, "usage")
		ccpu, cmem := usageFromMap(usage)
		cpu += ccpu
		mem += cmem
	}
	return cpu, mem
}

// buildContainerUsage returns the usage of each container of each PodMetrics
// object, by pod key.
func buildContainerUsage(metrics []unstructured.Unstructured) map[string][]containerUsage {
	out := make(map[string][]containerUsage, len(metrics))
	for i := range metrics {
		pm := &metrics[i]
		containers, _, _ := unstructured.NestedSlice(pm.Object, "containers")
		list := make([]containerUsage, 0, len(containers))
		for _, c := range containers {
			m, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(m, "name")
			usage, _, _ := unstructured.NestedStringMap(m, "usage")
			cpu, mem := usageFromMap(usage)
			list = append(list, containerUsage{name: name, cpu: cpu, mem: mem})
		}
		out[usageObjectKey(pm.GetNamespace(), pm.GetName())] = list
	}
	return out
}

// nodeMetricsUsage reads the usage of one NodeMetrics object.
func nodeMetricsUsage(nm *unstructured.Unstructured) (cpu, mem int64) {
	usage, _, _ := unstructured.NestedStringMap(nm.Object, "usage")
	return usageFromMap(usage)
}

// buildUsageValues joins metrics with the objects from the shared cache.
// Objects without metrics get no entry, and show as n/a. objects may be nil
// when the cache could not be read; percentages are n/a then.
func buildUsageValues(kind usageKind, metrics []unstructured.Unstructured, objects []unstructured.Unstructured) map[string]usageValues {
	alloc := make(map[string]usageValues, len(objects))
	for i := range objects {
		o := &objects[i]
		key := usageObjectKey(o.GetNamespace(), o.GetName())
		if kind == usageNodes {
			alloc[key] = nodeAllocatable(o)
		} else {
			alloc[key] = podAllocation(o)
		}
	}
	out := make(map[string]usageValues, len(metrics))
	for i := range metrics {
		m := &metrics[i]
		key := usageObjectKey(m.GetNamespace(), m.GetName())
		v := alloc[key]
		if kind == usageNodes {
			v.cpu, v.mem = nodeMetricsUsage(m)
		} else {
			v.cpu, v.mem = podMetricsUsage(m)
		}
		out[key] = v
	}
	return out
}

// withUsageColumns appends CPU and memory usage to a pod or node list. The
// columns merge into the server Table before visibility and order apply, so
// wide mode and column sorting treat them like server columns.
func (o *ObjectsFolder) withUsageColumns(rl *tablecache.RowList) *tablecache.RowList {
	kind := usageKindFor(o.gvr)
	if kind == 0 || o.Deps.Usage == nil {
		return rl
	}
	snap, fresh := o.Deps.Usage.snapshot(o.Deps, kind, o.namespace, o, func() {
		if o.rows != nil {
			o.rows.MarkDirty()
		}
	})
	return mergeUsageColumns(rl, kind, o.namespace, snap, fresh)
}
