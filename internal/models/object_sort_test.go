package models

import (
	"slices"
	"testing"
	"time"

	"github.com/sttts/kc/internal/tablecache"
	appconfig "github.com/sttts/kc/pkg/appconfig"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func sortRow(ns, name string, created int64, cells ...any) tablecache.Row {
	var r tablecache.Row
	r.Namespace, r.Name = ns, name
	r.CreationTimestamp = metav1.NewTime(time.Unix(created, 0))
	r.Cells = append([]any{name}, cells...)
	return r
}

func sortedNames(items []tablecache.Row, idxs []int) []string {
	out := make([]string, len(idxs))
	for i, ii := range idxs {
		out[i] = items[ii].Name
	}
	return out
}

func TestOrderRowIndicesByColumnValue(t *testing.T) {
	t.Parallel()
	podCols := []metav1.TableColumnDefinition{
		{Name: "Name", Type: "string", Format: "name"},
		{Name: "Ready", Type: "string"},
		{Name: "Status", Type: "string"},
		{Name: "Restarts", Type: "string"},
		{Name: "Age", Type: "string"},
		{Name: "IP", Type: "string", Priority: 1},
	}
	pods := []tablecache.Row{
		sortRow("", "a", 300, "1/1", "Running", "10 (2m ago)", "5m", "10.0.0.10"),
		sortRow("", "b", 100, "0/1", "Pending", "2", "15m", "<none>"),
		sortRow("", "c", 200, "2/2", "running", "0", "10m", "10.0.0.9"),
		sortRow("", "d", 400, "1/2", "Error", "<none>", "1m", "9.1.1.1"),
	}
	quotaCols := []metav1.TableColumnDefinition{
		{Name: "Name", Type: "string", Format: "name"},
		{Name: "CPU", Type: "string"},
		{Name: "Memory", Type: "string"},
		{Name: "Replicas", Type: "integer"},
		{Name: "Last Schedule", Type: "date"},
	}
	quotas := []tablecache.Row{
		sortRow("", "q1", 1, "1", "1Gi", int64(3), "2d"),
		sortRow("", "q2", 1, "250m", "512Mi", int64(10), "3h"),
		sortRow("", "q3", 1, "2", "128Mi", nil, "<unknown>"),
		sortRow("", "q4", 1, "500m", "2Gi", float64(1), "1y"),
	}
	cases := []struct {
		name  string
		items []tablecache.Row
		cols  []metav1.TableColumnDefinition
		order string
		want  []string
		mark  string
	}{
		{"restarts as counts, missing last", pods, podCols, "restarts", []string{"c", "b", "a", "d"}, "restarts"},
		{"restarts descending, missing still last", pods, podCols, "-restarts", []string{"a", "b", "c", "d"}, "-restarts"},
		{"ready as ratio", pods, podCols, "ready", []string{"b", "a", "d", "c"}, "ready"},
		{"status as text, case-insensitive, ties by name", pods, podCols, "status", []string{"d", "b", "a", "c"}, "status"},
		{"age by creation timestamp, not by the cell", pods, podCols, "age", []string{"d", "a", "c", "b"}, "age"},
		{"creation is age reversed", pods, podCols, "creation", []string{"b", "c", "a", "d"}, "-age"},
		{"ip as address, hidden column still sorts", pods, podCols, "ip", []string{"d", "c", "a", "b"}, "ip"},
		{"unknown column falls back to name", pods, podCols, "-cpu", []string{"a", "b", "c", "d"}, "name"},
		{"cpu as quantity", quotas, quotaCols, "cpu", []string{"q2", "q4", "q1", "q3"}, "cpu"},
		{"memory as quantity, descending", quotas, quotaCols, "-memory", []string{"q4", "q1", "q2", "q3"}, "-memory"},
		{"integer column, nil last", quotas, quotaCols, "replicas", []string{"q4", "q1", "q2", "q3"}, "replicas"},
		{"date column as duration", quotas, quotaCols, "last-schedule", []string{"q2", "q1", "q4", "q3"}, "last-schedule"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			idxs, sortBy := orderRowIndices(tc.items, tc.cols, tc.order, false)
			if got := sortedNames(tc.items, idxs); !slices.Equal(got, tc.want) {
				t.Errorf("order %q = %v, want %v", tc.order, got, tc.want)
			}
			if got := sortBy.order(); got != tc.mark {
				t.Errorf("applied order = %q, want %q", got, tc.mark)
			}
		})
	}
}

func TestOrderRowIndicesNamespaceStaysOuterKey(t *testing.T) {
	t.Parallel()
	cols := []metav1.TableColumnDefinition{{Name: "Name"}, {Name: "Restarts"}}
	items := []tablecache.Row{
		sortRow("b", "x", 1, "5"),
		sortRow("a", "y", 1, "1"),
		sortRow("b", "z", 1, "7"),
		sortRow("a", "w", 1, "9"),
	}
	cases := []struct {
		order string
		want  []string
	}{
		{"-restarts", []string{"w", "y", "z", "x"}},
		{"namespace", []string{"w", "y", "x", "z"}},
		{"-namespace", []string{"x", "z", "w", "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.order, func(t *testing.T) {
			t.Parallel()
			idxs, _ := orderRowIndices(items, cols, tc.order, true)
			if got := sortedNames(items, idxs); !slices.Equal(got, tc.want) {
				t.Errorf("order %q = %v, want %v", tc.order, got, tc.want)
			}
		})
	}
	t.Run("namespace in one namespace falls back to name", func(t *testing.T) {
		t.Parallel()
		if got := resolveObjectSort("-namespace", cols, false).order(); got != appconfig.ObjectsOrderName {
			t.Errorf("applied order = %q, want name", got)
		}
	})
}

func TestObjectsFolderSortMarkAcrossNamespaces(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	rl := &tablecache.RowList{
		Columns: []metav1.TableColumnDefinition{
			{Name: "Name"},
			{Name: "Restarts"},
			{Name: "IP", Priority: 1},
		},
		Items: []tablecache.Row{
			sortRow("b", "x", 1, "5", "10.0.0.2"),
			sortRow("a", "y", 2, "1", "10.0.0.1"),
			sortRow("a", "w", 3, "9", "10.0.0.3"),
		},
	}
	cases := []struct {
		order    string
		wantIDs  []string
		wantMark SortMark
		marked   bool
		applied  string
	}{
		{"-restarts", []string{"a/w", "a/y", "b/x"}, SortMark{Column: 2, Desc: true}, true, "-restarts"},
		{"-namespace", []string{"b/x", "a/w", "a/y"}, SortMark{Column: 1, Desc: true}, true, "-namespace"},
		{"name", []string{"a/w", "a/y", "b/x"}, SortMark{Column: 0}, true, "name"},
		// IP is a wide column: it still sorts, but no shown column carries the mark.
		{"ip", []string{"a/y", "a/w", "b/x"}, SortMark{}, false, "ip"},
	}
	for _, tc := range cases {
		t.Run(tc.order, func(t *testing.T) {
			t.Parallel()
			folder := NewObjectsFolder(Deps{}, gvr, "", []string{"pods"}, nil)
			rows := folder.rowsFromRowList(rl, appconfig.ColumnsModeNormal, tc.order)
			ids := make([]string, len(rows))
			for i, r := range rows {
				ids[i], _, _, _ = r.Columns()
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("rows = %v, want %v", ids, tc.wantIDs)
			}
			mark, ok := folder.SortMark()
			if ok != tc.marked || mark != tc.wantMark {
				t.Errorf("SortMark = %+v, %v; want %+v, %v", mark, ok, tc.wantMark, tc.marked)
			}
			if got := folder.ObjectOrder(); got != tc.applied {
				t.Errorf("ObjectOrder = %q, want %q", got, tc.applied)
			}
			if got, want := folder.SortKeys(), []string{"name", "namespace", "restarts"}; !slices.Equal(got, want) {
				t.Errorf("SortKeys = %v, want %v", got, want)
			}
		})
	}
}

func TestDetectSortKindRefusesMixedCells(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cells []any
		want  sortKind
	}{
		{"numbers", []any{"1", "20", "<none>"}, sortNumber},
		{"one word turns numbers to text", []any{"1", "many"}, sortString},
		{"ports are not ratios", []any{"80/TCP", "443/TCP"}, sortString},
		{"cpu with a plain core count", []any{"250m", "1"}, sortQuantity},
		{"durations", []any{"5m", "2d3h", "45s"}, sortDuration},
		{"sizes are not durations", []any{"5m", "1Gi"}, sortQuantity},
		{"only placeholders", []any{"<none>", nil, ""}, sortString},
		{"nan is not a number", []any{"NaN", "Inf"}, sortString},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := detectSortKind(tc.cells); got != tc.want {
				t.Errorf("kind = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParseHumanDuration(t *testing.T) {
	t.Parallel()
	cases := map[string]time.Duration{
		"45s":   45 * time.Second,
		"3h20m": 3*time.Hour + 20*time.Minute,
		"2d4h":  52 * time.Hour,
		"1y30d": 395 * 24 * time.Hour,
	}
	for in, want := range cases {
		if got, ok := parseHumanDuration(in); !ok || got != want {
			t.Errorf("parseHumanDuration(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "5", "5x", "m5", "1Gi"} {
		if _, ok := parseHumanDuration(bad); ok {
			t.Errorf("parseHumanDuration(%q) accepted", bad)
		}
	}
}

func TestNormalizeObjectsOrder(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                "name",
		"  -Name ":        "-name",
		"creation":        "creation",
		"Nominated Node":  "nominated-node",
		"-Last  Schedule": "-last-schedule",
		"-":               "name",
		"--age":           "name",
	}
	for in, want := range cases {
		if got := appconfig.NormalizeObjectsOrder(in); got != want {
			t.Errorf("NormalizeObjectsOrder(%q) = %q, want %q", in, got, want)
		}
	}
}
