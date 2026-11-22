package models

import (
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stokaro/kc/internal/tablecache"
	"github.com/stokaro/kc/pkg/appconfig"
)

func TestFormatPercent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		used, total int64
		want        string
	}{
		{"half", 250, 500, "50"},
		{"rounds", 1, 3, "33"},
		{"rounds up", 2, 3, "67"},
		{"above 100", 300, 200, "150"},
		{"zero usage", 0, 100, "0"},
		{"no total", 100, 0, usageNA},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := formatPercent(tc.used, tc.total); got != tc.want {
				t.Errorf("formatPercent(%d, %d) = %q, want %q", tc.used, tc.total, got, tc.want)
			}
		})
	}
}

func TestFormatQuantities(t *testing.T) {
	t.Parallel()
	if got := formatMilliCPU(1500); got != "1500m" {
		t.Errorf("formatMilliCPU = %q", got)
	}
	if got := formatMemory(128<<20 + 512<<10); got != "128Mi" {
		t.Errorf("formatMemory = %q", got)
	}
}

func container(name string, requests, limits map[string]any) map[string]any {
	res := make(map[string]any)
	if requests != nil {
		res["requests"] = requests
	}
	if limits != nil {
		res["limits"] = limits
	}
	return map[string]any{"name": name, "resources": res}
}

func testPod(ns, name string, containers, inits []any) unstructured.Unstructured {
	spec := map[string]any{"containers": containers}
	if inits != nil {
		spec["initContainers"] = inits
	}
	u := unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetAPIVersion("v1")
	u.SetKind("Pod")
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func TestPodAllocation(t *testing.T) {
	t.Parallel()
	full := map[string]any{"cpu": "500m", "memory": "256Mi"}
	tests := []struct {
		name string
		pod  unstructured.Unstructured
		want usageValues
	}{
		{
			name: "sums containers",
			pod: testPod("ns", "p", []any{
				container("a", map[string]any{"cpu": "100m", "memory": "64Mi"}, full),
				container("b", map[string]any{"cpu": "0.2", "memory": "1Gi"}, full),
			}, nil),
			want: usageValues{cpuReq: 300, memReq: 64<<20 + 1<<30, cpuLim: 1000, memLim: 512 << 20},
		},
		{
			name: "one container without a limit leaves the pod unlimited",
			pod: testPod("ns", "p", []any{
				container("a", full, full),
				container("b", full, map[string]any{"cpu": "1"}),
			}, nil),
			want: usageValues{cpuReq: 1000, memReq: 512 << 20, cpuLim: 1500},
		},
		{
			name: "best effort",
			pod:  testPod("ns", "p", []any{container("a", nil, nil)}, nil),
			want: usageValues{},
		},
		{
			name: "sidecars count, plain init containers do not",
			pod: testPod("ns", "p",
				[]any{container("main", full, full)},
				[]any{
					container("setup", map[string]any{"cpu": "4", "memory": "4Gi"}, nil),
					func() any {
						c := container("proxy", map[string]any{"cpu": "100m", "memory": "32Mi"}, map[string]any{"cpu": "200m", "memory": "64Mi"})
						c["restartPolicy"] = "Always"
						return c
					}(),
				}),
			want: usageValues{cpuReq: 600, memReq: 288 << 20, cpuLim: 700, memLim: 320 << 20},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := podAllocation(&tc.pod); got != tc.want {
				t.Errorf("podAllocation = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func podMetrics(ns, name string, usages ...map[string]any) unstructured.Unstructured {
	var containers []any
	for _, u := range usages {
		containers = append(containers, map[string]any{"name": "c", "usage": u})
	}
	u := unstructured.Unstructured{Object: map[string]any{"containers": containers}}
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func TestBuildUsageValues(t *testing.T) {
	t.Parallel()
	t.Run("pods", func(t *testing.T) {
		t.Parallel()
		metrics := []unstructured.Unstructured{
			podMetrics("ns", "web", map[string]any{"cpu": "123456789n", "memory": "65536Ki"}, map[string]any{"cpu": "10m", "memory": "1Mi"}),
			podMetrics("ns", "orphan", map[string]any{"cpu": "1m", "memory": "1Mi"}),
		}
		pods := []unstructured.Unstructured{
			testPod("ns", "web", []any{container("a", map[string]any{"cpu": "200m", "memory": "128Mi"}, nil)}, nil),
			testPod("ns", "idle", []any{container("a", nil, nil)}, nil),
		}
		got := buildUsageValues(usagePods, metrics, pods)
		want := map[string]usageValues{
			// 123456789n rounds up to 124m.
			"ns/web":    {cpu: 134, mem: 65 << 20, cpuReq: 200, memReq: 128 << 20},
			"ns/orphan": {cpu: 1, mem: 1 << 20},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("buildUsageValues = %+v\nwant %+v", got, want)
		}
	})
	t.Run("nodes", func(t *testing.T) {
		t.Parallel()
		nm := unstructured.Unstructured{Object: map[string]any{"usage": map[string]any{"cpu": "250m", "memory": "1Gi"}}}
		nm.SetName("n1")
		node := unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
			"allocatable": map[string]any{"cpu": "4", "memory": "8Gi", "pods": "110"},
		}}}
		node.SetName("n1")
		got := buildUsageValues(usageNodes, []unstructured.Unstructured{nm}, []unstructured.Unstructured{node})
		want := map[string]usageValues{"n1": {cpu: 250, mem: 1 << 30, cpuReq: 4000, memReq: 8 << 30}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("buildUsageValues = %+v, want %+v", got, want)
		}
	})
}

func podRowList() *tablecache.RowList {
	row := func(name string) tablecache.Row {
		cells := make([]any, 2, 8) // spare capacity: appending in place would be visible
		cells[0], cells[1] = name, "Running"
		return tablecache.Row{
			Name: name, Namespace: "ns",
			Cells: cells,
		}
	}
	return &tablecache.RowList{
		Columns: []metav1.TableColumnDefinition{{Name: "Name"}, {Name: "Status"}},
		Items:   []tablecache.Row{row("a"), row("b")},
	}
}

func cellsOf(rl *tablecache.RowList) [][]any {
	out := make([][]any, len(rl.Items))
	for i := range rl.Items {
		out[i] = rl.Items[i].Cells
	}
	return out
}

func columnNames(rl *tablecache.RowList) []string {
	out := make([]string, len(rl.Columns))
	for i, c := range rl.Columns {
		out[i] = c.Name
	}
	return out
}

func TestMergeUsageColumns(t *testing.T) {
	t.Parallel()
	values := map[string]usageValues{
		"ns/a": {cpu: 250, mem: 100 << 20, cpuReq: 500, memReq: 200 << 20, cpuLim: 1000, memLim: 400 << 20},
	}
	allCols := []string{"Name", "Status", "CPU", "MEM", "%CPU/R", "%CPU/L", "%MEM/R", "%MEM/L"}
	tests := []struct {
		name      string
		snap      *usageSnapshot
		fresh     bool
		wantCols  []string
		wantCells [][]any
	}{
		{
			name:     "no poll yet",
			wantCols: []string{"Name", "Status"},
			wantCells: [][]any{
				{"a", "Running"},
				{"b", "Running"},
			},
		},
		{
			name:     "metrics.k8s.io not served",
			snap:     &usageSnapshot{},
			fresh:    true,
			wantCols: []string{"Name", "Status"},
			wantCells: [][]any{
				{"a", "Running"},
				{"b", "Running"},
			},
		},
		{
			name:     "values, and n/a for a pod without metrics",
			snap:     &usageSnapshot{served: true, values: values},
			fresh:    true,
			wantCols: allCols,
			wantCells: [][]any{
				{"a", "Running", "250m", "100Mi", "50", "25", "50", "25"},
				{"b", "Running", usageNA, usageNA, usageNA, usageNA, usageNA, usageNA},
			},
		},
		{
			name:     "forbidden",
			snap:     &usageSnapshot{served: true, err: errors.New("forbidden"), values: values},
			fresh:    true,
			wantCols: allCols,
			wantCells: [][]any{
				{"a", "Running", usageNA, usageNA, usageNA, usageNA, usageNA, usageNA},
				{"b", "Running", usageNA, usageNA, usageNA, usageNA, usageNA, usageNA},
			},
		},
		{
			name:     "stale snapshot keeps the columns empty",
			snap:     &usageSnapshot{served: true, values: values},
			wantCols: allCols,
			wantCells: [][]any{
				{"a", "Running", "", "", "", "", "", ""},
				{"b", "Running", "", "", "", "", "", ""},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := podRowList()
			got := mergeUsageColumns(in, usagePods, "ns", tc.snap, tc.fresh)
			if names := columnNames(got); !reflect.DeepEqual(names, tc.wantCols) {
				t.Errorf("columns = %v, want %v", names, tc.wantCols)
			}
			if cells := cellsOf(got); !reflect.DeepEqual(cells, tc.wantCells) {
				t.Errorf("cells = %v\nwant %v", cells, tc.wantCells)
			}
			// The input may be shared with the cache: it must stay as it was,
			// including the spare capacity of its cell slices.
			if names := columnNames(in); !reflect.DeepEqual(names, []string{"Name", "Status"}) {
				t.Errorf("input columns changed: %v", names)
			}
			for i := range in.Items {
				if spare := in.Items[i].Cells[:cap(in.Items[i].Cells)][2:]; spare[0] != nil {
					t.Errorf("row %d: merge wrote into the input's backing array: %v", i, spare)
				}
			}
		})
	}
}

func TestMergeUsageColumnsNodes(t *testing.T) {
	t.Parallel()
	rl := &tablecache.RowList{
		Columns: []metav1.TableColumnDefinition{{Name: "Name"}},
		Items: []tablecache.Row{{
			// tablecache guesses a namespace for cluster-scoped rows from
			// the second cell; the node must still match by name.
			Name: "n1", Namespace: "Ready",
			Cells: []any{"n1"},
		}},
	}
	snap := &usageSnapshot{served: true, values: map[string]usageValues{"n1": {cpu: 1000, mem: 2 << 30, cpuReq: 4000, memReq: 8 << 30}}}
	got := mergeUsageColumns(rl, usageNodes, "", snap, true)
	want := []any{"n1", "1000m", "25", "4000m", "2048Mi", "25", "8192Mi"}
	if !reflect.DeepEqual(got.Items[0].Cells, want) {
		t.Errorf("cells = %v, want %v", got.Items[0].Cells, want)
	}
	if names := columnNames(got); !reflect.DeepEqual(names, []string{"Name", "CPU", "%CPU", "CPU/A", "MEM", "%MEM", "MEM/A"}) {
		t.Errorf("columns = %v", names)
	}
}

// The folder shows the usage columns of priority 0 in normal mode and all of
// them in wide mode, like server columns.
func TestUsageColumnsInFolderRows(t *testing.T) {
	t.Parallel()
	folder := NewObjectsFolder(Deps{}, podGVR, "ns", []string{"namespaces", "ns", "pods"}, nil)
	snap := &usageSnapshot{served: true, values: map[string]usageValues{
		"ns/a": {cpu: 250, mem: 100 << 20, cpuReq: 500, memLim: 400 << 20},
	}}
	merged := mergeUsageColumns(podRowList(), usagePods, "ns", snap, true)

	tests := []struct {
		mode      string
		wantTitle []string
		wantCells []string
	}{
		{appconfig.ColumnsModeNormal, []string{"Name", "Status", "CPU", "MEM", "%CPU/R", "%MEM/L"}, []string{"/a", "Running", "250m", "100Mi", "50", "25"}},
		{appconfig.ColumnsModeWide, []string{"Name", "Status", "CPU", "MEM", "%CPU/R", "%CPU/L", "%MEM/R", "%MEM/L"}, []string{"/a", "Running", "250m", "100Mi", "50", usageNA, usageNA, "25"}},
	}
	for _, tc := range tests {
		rows := folder.rowsFromRowList(merged, tc.mode, appconfig.ObjectsOrderName, nil)
		folder.installAgeHooks(nil)
		var titles []string
		for _, c := range folder.Columns() {
			titles = append(titles, c.Title)
		}
		if !reflect.DeepEqual(titles, tc.wantTitle) {
			t.Errorf("%s: titles = %v, want %v", tc.mode, titles, tc.wantTitle)
		}
		_, cells, _, _ := rows[0].Columns()
		if !reflect.DeepEqual(cells, tc.wantCells) {
			t.Errorf("%s: cells = %v, want %v", tc.mode, cells, tc.wantCells)
		}
	}
}

func TestUsageKindFor(t *testing.T) {
	t.Parallel()
	if usageKindFor(podGVR) != usagePods || usageKindFor(nodeGVR) != usageNodes {
		t.Fatal("pods and nodes must have usage columns")
	}
	// Same resource name in another group is not a core pod list.
	other := podGVR
	other.Group = "metrics.k8s.io"
	if usageKindFor(other) != 0 {
		t.Fatal("metrics.k8s.io pods must not get usage columns")
	}
}
