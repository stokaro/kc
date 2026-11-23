package models

// White-box: column terms read the cells a listing records in the row
// facts, and a black-box test would need a live table cache to list rows.

import (
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/tablecache"
	appconfig "github.com/stokaro/kc/pkg/appconfig"
)

func podFilterList() *tablecache.RowList {
	return &tablecache.RowList{
		Columns: []metav1.TableColumnDefinition{
			{Name: "Name"},
			{Name: "Status"},
			{Name: "Restarts"},
			{Name: "Node", Priority: 1},
			{Name: "CPU"},
			{Name: "MEM"},
		},
		Items: []tablecache.Row{
			sortRow("prod", "api", 1, "Running", "4 (2m ago)", "n1", "750m", "1500Mi"),
			sortRow("dev", "api", 2, "Running", "0", "n2", "1", "2Gi"),
			sortRow("prod", "web", 3, "CrashLoopBackOff", "12", "n1", "n/a", "n/a"),
			sortRow("kube-system", "dns", 4, "Running", "<none>", "n1", "5m", "20Mi"),
		},
	}
}

func keptIDs(t *testing.T, folder *ObjectsFolder, rl *tablecache.RowList, text string) []string {
	t.Helper()
	f, err := ParseRowFilter(text)
	if err != nil {
		t.Fatalf("ParseRowFilter(%q): %v", text, err)
	}
	if err := folder.ValidateRowFilter(f); err != nil {
		t.Fatalf("ValidateRowFilter(%q): %v", text, err)
	}
	var ids []string
	for _, r := range folder.rowsFromRowList(rl, appconfig.ColumnsModeNormal, appconfig.ObjectsOrderName, nil) {
		if f.keepRow(r) {
			id, _, _, _ := r.Columns()
			ids = append(ids, id)
		}
	}
	return ids
}

func TestRowFilterColumnTerms(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	cases := []struct {
		text string
		want []string
	}{
		{"cpu>500m", []string{"dev/api", "prod/api"}},
		{"cpu>=1", []string{"dev/api"}},
		{"mem>1Gi", []string{"dev/api", "prod/api"}},
		{"mem<1Gi", []string{"kube-system/dns"}},
		{"restarts>3", []string{"prod/api", "prod/web"}},
		{"restarts<=3", []string{"dev/api"}},
		// Node is a wide column: hidden in normal mode, it still filters.
		{"node=n1 restarts>3", []string{"prod/api", "prod/web"}},
		{"status!=running", []string{"prod/web"}},
		{"status=Running,CrashLoopBackOff -n prod", []string{"prod/api", "prod/web"}},
		{"api cpu>500m -n prod,stage", []string{"prod/api"}},
		{"-l app=none cpu>0", nil},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			t.Parallel()
			folder := NewObjectsFolder(Deps{}, gvr, "", []string{"pods"}, nil)
			// The same names in two namespaces stay two rows.
			if got := keptIDs(t, folder, podFilterList(), tc.text); !slices.Equal(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("live update", func(t *testing.T) {
		t.Parallel()
		folder := NewObjectsFolder(Deps{}, gvr, "", []string{"pods"}, nil)
		rl := podFilterList()
		if got := keptIDs(t, folder, rl, "mem>1800Mi"); !slices.Equal(got, []string{"dev/api"}) {
			t.Fatalf("first listing kept %v", got)
		}
		rl.Items[0] = sortRow("prod", "api", 1, "Running", "4 (2m ago)", "n1", "750m", "3Gi")
		if got := keptIDs(t, folder, rl, "mem>1800Mi"); !slices.Equal(got, []string{"dev/api", "prod/api"}) {
			t.Fatalf("after the update kept %v", got)
		}
	})
}

func TestValidateRowFilterColumns(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	folder := NewObjectsFolder(Deps{}, gvr, "", []string{"pods"}, nil)
	folder.rowsFromRowList(podFilterList(), appconfig.ColumnsModeNormal, appconfig.ObjectsOrderName, nil)
	cases := []struct {
		text, wantErr string
	}{
		{"cpu>500m", ""},
		{"age>1h namespace=prod name=api", ""},
		{"gpu>1", `no "gpu" column`},
		{"cpu>fast", "not a quantity"},
		{"restarts>many", "not a count"},
		{"age>soon", "not a duration"},
		{"status=whatever", ""},
	}
	for _, tc := range cases {
		f, err := ParseRowFilter(tc.text)
		if err != nil {
			t.Fatalf("ParseRowFilter(%q): %v", tc.text, err)
		}
		err = folder.ValidateRowFilter(f)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("ValidateRowFilter(%q) = %v, want nil", tc.text, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("ValidateRowFilter(%q) = %v, want %q", tc.text, err, tc.wantErr)
		}
	}
}
