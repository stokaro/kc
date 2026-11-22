package models

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sttts/kc/internal/tablecache"
	"github.com/sttts/kc/pkg/appconfig"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func deployment(ns, name string, images ...string) unstructured.Unstructured {
	containers := make([]any, len(images))
	for i, img := range images {
		containers[i] = map[string]any{"name": "c", "image": img}
	}
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"replicas": int64(2),
			"template": map[string]any{"spec": map[string]any{"containers": containers}},
		},
	}}
}

// columnDeps returns Deps with the columns config and a recorder for
// reported errors. It resets the once-per-process report set.
func columnDeps(t *testing.T, cols map[string][]appconfig.CustomColumn) (Deps, *[]error) {
	t.Helper()
	reportedColumnErrors.Clear()
	t.Cleanup(func() { reportedColumnErrors.Clear() })
	cfg := appconfig.Default()
	cfg.Columns = cols
	var got []error
	return Deps{Ctx: t.Context(), AppConfig: cfg, ReportError: func(err error) { got = append(got, err) }}, &got
}

func TestPlanCustomColumns(t *testing.T) {
	cols := map[string][]appconfig.CustomColumn{
		"apps/v1/deployments": {
			{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"},
			{Name: "UID", JSONPath: ".metadata.uid", Wide: true},
			{Name: "Up-to-date", Hide: true},
		},
		"v1/pods": {{Name: "*", Hide: true}},
	}
	tests := []struct {
		name      string
		gvr       schema.GroupVersionResource
		mode      string
		wantNil   bool
		wantTitle []string
		wantHide  []string
		hideAll   bool
	}{
		{name: "normal mode skips wide", gvr: deploymentsGVR, mode: appconfig.ColumnsModeNormal, wantTitle: []string{"IMAGE"}, wantHide: []string{"up-to-date"}},
		{name: "wide mode adds wide", gvr: deploymentsGVR, mode: appconfig.ColumnsModeWide, wantTitle: []string{"IMAGE", "UID"}, wantHide: []string{"up-to-date"}},
		{name: "hide all", gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, mode: appconfig.ColumnsModeNormal, hideAll: true},
		{name: "other version", gvr: schema.GroupVersionResource{Group: "apps", Version: "v1beta1", Resource: "deployments"}, wantNil: true},
		{name: "not configured", gvr: schema.GroupVersionResource{Version: "v1", Resource: "services"}, wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _ := columnDeps(t, cols)
			cc := planCustomColumns(deps, tt.gvr, tt.mode)
			if tt.wantNil {
				if cc != nil {
					t.Fatalf("want no plan, got %+v", cc)
				}
				return
			}
			if cc == nil {
				t.Fatal("want a plan, got nil")
			}
			var titles []string
			for _, c := range cc.tableColumns() {
				titles = append(titles, c.Title)
			}
			if !reflect.DeepEqual(titles, tt.wantTitle) {
				t.Fatalf("titles = %v, want %v", titles, tt.wantTitle)
			}
			var hide []string
			for h := range cc.hide {
				hide = append(hide, h)
			}
			if !reflect.DeepEqual(hide, tt.wantHide) {
				t.Fatalf("hide = %v, want %v", hide, tt.wantHide)
			}
			if cc.hideAll != tt.hideAll {
				t.Fatalf("hideAll = %v, want %v", cc.hideAll, tt.hideAll)
			}
		})
	}

	t.Run("no config", func(t *testing.T) {
		deps, _ := columnDeps(t, nil)
		if cc := planCustomColumns(deps, deploymentsGVR, ""); cc != nil {
			t.Fatalf("want nil, got %+v", cc)
		}
		if cc := planCustomColumns(Deps{}, deploymentsGVR, ""); cc != nil {
			t.Fatalf("want nil without AppConfig, got %+v", cc)
		}
	})
}

func TestCustomColumnCells(t *testing.T) {
	obj := deployment("default", "web", "nginx:1", "envoy:2")
	obj.SetLabels(map[string]string{"app": "web"})
	tests := []struct {
		name     string
		jsonPath string
		want     string
	}{
		{name: "scalar", jsonPath: ".spec.replicas", want: "2"},
		{name: "relaxed without dot", jsonPath: "metadata.name", want: "web"},
		{name: "braced", jsonPath: "{.metadata.namespace}", want: "default"},
		{name: "array joined", jsonPath: ".spec.template.spec.containers[*].image", want: "nginx:1,envoy:2"},
		{name: "index", jsonPath: ".spec.template.spec.containers[1].image", want: "envoy:2"},
		{name: "label", jsonPath: ".metadata.labels.app", want: "web"},
		{name: "missing field", jsonPath: ".status.readyReplicas", want: "<none>"},
		{name: "filter", jsonPath: `.spec.template.spec.containers[?(@.image=="envoy:2")].image`, want: "envoy:2"},
		{name: "bad expression stays empty", jsonPath: ".spec[*", want: ""},
		{name: "evaluation error stays empty", jsonPath: ".spec.replicas[0]", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _ := columnDeps(t, map[string][]appconfig.CustomColumn{
				"apps/v1/deployments": {{Name: "COL", JSONPath: tt.jsonPath}},
			})
			cc := planCustomColumns(deps, deploymentsGVR, appconfig.ColumnsModeNormal)
			got := cc.cellsFor(&obj)
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("cells = %q, want [%q]", got, tt.want)
			}
		})
	}

	t.Run("missing object gives empty cells", func(t *testing.T) {
		deps, _ := columnDeps(t, map[string][]appconfig.CustomColumn{
			"apps/v1/deployments": {{Name: "A", JSONPath: ".a"}, {Name: "B", JSONPath: ".b"}},
		})
		cc := planCustomColumns(deps, deploymentsGVR, "")
		cc.setObjects([]unstructured.Unstructured{obj})
		if got := cc.cells("default", "gone"); !reflect.DeepEqual(got, []string{"", ""}) {
			t.Fatalf("cells = %q", got)
		}
		if got := cc.cells("other", "web"); !reflect.DeepEqual(got, []string{"", ""}) {
			t.Fatalf("an object of another namespace matched: %q", got)
		}
		if got := cc.cells("default", "web"); !reflect.DeepEqual(got, []string{"<none>", "<none>"}) {
			t.Fatalf("cells = %q", got)
		}
	})

	t.Run("nil plan adds nothing", func(t *testing.T) {
		var cc *customColumns
		if cc.cells("a", "b") != nil || cc.cellsFor(&obj) != nil || cc.tableColumns() != nil {
			t.Fatal("nil plan added columns")
		}
		cc.setObjects([]unstructured.Unstructured{obj})
		vis := []int{0, 1, 2}
		if got := cc.serverColumns(nil, vis); !reflect.DeepEqual(got, vis) {
			t.Fatalf("serverColumns = %v", got)
		}
	})
}

func TestCustomColumnErrorsReportedOnce(t *testing.T) {
	t.Run("config error once across loads", func(t *testing.T) {
		deps, got := columnDeps(t, map[string][]appconfig.CustomColumn{
			"apps/v1/deployments": {{Name: "BAD", JSONPath: ".spec[*"}},
			"deployments":         {{Name: "X", JSONPath: ".a"}},
		})
		for range 3 {
			cc := planCustomColumns(deps, deploymentsGVR, "")
			if len(cc.tableColumns()) != 1 {
				t.Fatal("a bad column must stay visible (and empty)")
			}
		}
		if len(*got) != 1 {
			t.Fatalf("reported %d times, want once: %v", len(*got), *got)
		}
		msg := (*got)[0].Error()
		for _, want := range []string{"BAD", `columns key "deployments"`} {
			if !strings.Contains(msg, want) {
				t.Fatalf("report %q does not name %q", msg, want)
			}
		}
	})

	t.Run("evaluation error once across rows", func(t *testing.T) {
		deps, got := columnDeps(t, map[string][]appconfig.CustomColumn{
			"apps/v1/deployments": {{Name: "R", JSONPath: ".spec.replicas[0]"}},
		})
		for i := range 2 {
			cc := planCustomColumns(deps, deploymentsGVR, "")
			for _, name := range []string{"a", "b", "c"} {
				obj := deployment("default", name, "img")
				cc.cellsFor(&obj)
			}
			if len(*got) != 1 {
				t.Fatalf("after load %d: reported %d times, want once: %v", i, len(*got), *got)
			}
		}
		if !strings.Contains((*got)[0].Error(), "column R of deployments") {
			t.Fatalf("report = %v", (*got)[0])
		}
	})

	t.Run("evaluation error text varying per row reported once", func(t *testing.T) {
		deps, got := columnDeps(t, map[string][]appconfig.CustomColumn{
			"apps/v1/deployments": {{Name: "THIRD", JSONPath: ".spec.template.spec.containers[2].image"}},
		})
		cc := planCustomColumns(deps, deploymentsGVR, "")
		// "index 2, length 1" and "index 2, length 2": different text, one column.
		for _, images := range [][]string{{"a"}, {"a", "b"}} {
			obj := deployment("default", "web", images...)
			if cells := cc.cellsFor(&obj); cells[0] != "" {
				t.Fatalf("cell = %q, want empty", cells[0])
			}
		}
		if len(*got) != 1 {
			t.Fatalf("reported %d times, want once: %v", len(*got), *got)
		}
		if !strings.Contains((*got)[0].Error(), "length 1") {
			t.Fatalf("report = %v, want the first row's error", (*got)[0])
		}
	})

	t.Run("valid config reports nothing", func(t *testing.T) {
		deps, got := columnDeps(t, map[string][]appconfig.CustomColumn{
			"apps/v1/deployments": {{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"}},
		})
		cc := planCustomColumns(deps, deploymentsGVR, "")
		obj := deployment("default", "web", "img")
		cc.cellsFor(&obj)
		if len(*got) != 0 {
			t.Fatalf("unexpected reports: %v", *got)
		}
	})
}

func TestRowsFromRowListMergesCustomColumns(t *testing.T) {
	rl := &tablecache.RowList{
		Columns: []metav1.TableColumnDefinition{
			{Name: "Name"}, {Name: "Ready"}, {Name: "Up-to-date"}, {Name: "Age"},
			{Name: "Containers", Priority: 1},
		},
		Items: []tablecache.Row{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
				TableRow:   metav1.TableRow{Cells: []any{"web", "1/1", "1", "5m", "c"}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
				TableRow:   metav1.TableRow{Cells: []any{"api", "0/1", "0", "1m", "c"}},
			},
		},
	}
	objects := []unstructured.Unstructured{deployment("default", "web", "nginx:1"), deployment("default", "api", "api:2")}

	tests := []struct {
		name      string
		cols      []appconfig.CustomColumn
		mode      string
		wantTitle []string
		wantWeb   []string
	}{
		{
			name:      "append",
			cols:      []appconfig.CustomColumn{{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"}},
			mode:      appconfig.ColumnsModeNormal,
			wantTitle: []string{"Name", "Ready", "Up-to-date", "Age", "IMAGE"},
			wantWeb:   []string{"/web", "1/1", "1", "5m", "nginx:1"},
		},
		{
			name: "hide one, case-insensitive",
			cols: []appconfig.CustomColumn{
				{Name: "up-to-date", Hide: true},
				{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"},
			},
			mode:      appconfig.ColumnsModeNormal,
			wantTitle: []string{"Name", "Ready", "Age", "IMAGE"},
			wantWeb:   []string{"/web", "1/1", "5m", "nginx:1"},
		},
		{
			name: "replace keeps Name",
			cols: []appconfig.CustomColumn{
				{Name: "*", Hide: true},
				{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"},
				{Name: "REPLICAS", JSONPath: ".spec.replicas"},
			},
			mode:      appconfig.ColumnsModeWide,
			wantTitle: []string{"Name", "IMAGE", "REPLICAS"},
			wantWeb:   []string{"/web", "nginx:1", "2"},
		},
		{
			name: "wide adds server and custom wide columns",
			cols: []appconfig.CustomColumn{
				{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image", Wide: true},
			},
			mode:      appconfig.ColumnsModeWide,
			wantTitle: []string{"Name", "Ready", "Up-to-date", "Age", "Containers", "IMAGE"},
			wantWeb:   []string{"/web", "1/1", "1", "5m", "c", "nginx:1"},
		},
		{
			name: "wide column hidden in normal mode",
			cols: []appconfig.CustomColumn{
				{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image", Wide: true},
			},
			mode:      appconfig.ColumnsModeNormal,
			wantTitle: []string{"Name", "Ready", "Up-to-date", "Age"},
			wantWeb:   []string{"/web", "1/1", "1", "5m"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _ := columnDeps(t, map[string][]appconfig.CustomColumn{"apps/v1/deployments": tt.cols})
			folder := NewObjectsFolder(deps, deploymentsGVR, "default", []string{"namespaces", "default", "deployments"}, nil)
			cc := planCustomColumns(deps, deploymentsGVR, tt.mode)
			cc.setObjects(objects)
			rows := folder.rowsFromRowList(rl, tt.mode, appconfig.ObjectsOrderName, cc)
			folder.installAgeHooks(nil)
			var titles []string
			for _, c := range folder.Columns() {
				titles = append(titles, c.Title)
			}
			if !reflect.DeepEqual(titles, tt.wantTitle) {
				t.Fatalf("titles = %v, want %v", titles, tt.wantTitle)
			}
			if len(rows) != 2 {
				t.Fatalf("got %d rows", len(rows))
			}
			// Rows are ordered by name: api, web.
			id, cells, _, _ := rows[1].Columns()
			if id != "web" {
				t.Fatalf("second row = %q, want web", id)
			}
			if !reflect.DeepEqual(cells, tt.wantWeb) {
				t.Fatalf("web cells = %q, want %q", cells, tt.wantWeb)
			}
			_, apiCells, _, _ := rows[0].Columns()
			if len(apiCells) != len(tt.wantTitle) {
				t.Fatalf("api cells %q do not match the %d columns", apiCells, len(tt.wantTitle))
			}
		})
	}
}

func TestRowsFromListAddsCustomColumns(t *testing.T) {
	deps, _ := columnDeps(t, map[string][]appconfig.CustomColumn{
		"apps/v1/deployments": {{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"}},
	})
	folder := NewObjectsFolder(deps, deploymentsGVR, "default", []string{"namespaces", "default", "deployments"}, nil)
	cc := planCustomColumns(deps, deploymentsGVR, "")
	list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{deployment("default", "web", "nginx:1")}}
	rows := folder.rowsFromList(list, appconfig.ObjectsOrderName, cc)
	var titles []string
	for _, c := range folder.Columns() {
		titles = append(titles, c.Title)
	}
	if want := []string{" Name", "IMAGE"}; !reflect.DeepEqual(titles, want) {
		t.Fatalf("titles = %v, want %v", titles, want)
	}
	_, cells, _, _ := rows[0].Columns()
	if want := []string{"/web", "nginx:1"}; !reflect.DeepEqual(cells, want) {
		t.Fatalf("cells = %q, want %q", cells, want)
	}
}

func TestObjectsFolderKubectlCommandCustomColumns(t *testing.T) {
	image := appconfig.CustomColumn{Name: "IMAGE", JSONPath: ".spec.template.spec.containers[*].image"}
	tests := []struct {
		name      string
		cols      []appconfig.CustomColumn
		namespace string
		want      string
	}{
		{
			name:      "none configured",
			namespace: "default",
			want:      "kubectl get deployments.apps -n default",
		},
		{
			name:      "extend",
			cols:      []appconfig.CustomColumn{image},
			namespace: "default",
			want:      "kubectl get deployments.apps -n default -o 'custom-columns=NAME:.metadata.name,IMAGE:.spec.template.spec.containers[*].image'  # kc also shows the server columns",
		},
		{
			name:      "replace across namespaces",
			cols:      []appconfig.CustomColumn{{Name: "*", Hide: true}, image},
			namespace: "",
			want:      "kubectl get deployments.apps -A -o 'custom-columns=NAME:.metadata.name,NAMESPACE:.metadata.namespace,IMAGE:.spec.template.spec.containers[*].image'",
		},
		{
			name:      "hide only",
			cols:      []appconfig.CustomColumn{{Name: "Age", Hide: true}},
			namespace: "default",
			want:      "kubectl get deployments.apps -n default",
		},
		{
			name:      "not expressible",
			cols:      []appconfig.CustomColumn{{Name: "PAIR", JSONPath: ".spec.template.spec.containers[0,1].image"}},
			namespace: "default",
			want:      "kubectl get deployments.apps -n default  # kc adds custom columns",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cols map[string][]appconfig.CustomColumn
			if tt.cols != nil {
				cols = map[string][]appconfig.CustomColumn{"apps/v1/deployments": tt.cols}
			}
			deps, _ := columnDeps(t, cols)
			folder := NewObjectsFolder(deps, deploymentsGVR, tt.namespace, nil, nil)
			if got := folder.KubectlCommand(); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}
