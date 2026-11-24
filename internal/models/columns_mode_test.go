package models

// White-box like the other ObjectsFolder tests: the rows are built from a
// RowList without a cluster.

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/tablecache"
	"github.com/stokaro/kc/pkg/appconfig"
)

// TestPanelColumnsModeOverridesConfig checks that the mode a panel applies
// (C-w) wins over the configured one, in both directions.
func TestPanelColumnsModeOverridesConfig(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	tests := []struct {
		name, config, applied, want string
	}{
		{name: "nothing applied reads the config", config: appconfig.ColumnsModeWide, want: appconfig.ColumnsModeWide},
		{name: "wide over normal", config: appconfig.ColumnsModeNormal, applied: "wide", want: appconfig.ColumnsModeWide},
		{name: "normal over wide", config: appconfig.ColumnsModeWide, applied: "normal", want: appconfig.ColumnsModeNormal},
		{name: "unknown is normal", config: appconfig.ColumnsModeWide, applied: "huge", want: appconfig.ColumnsModeNormal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := appconfig.Default()
			cfg.Objects.Columns = tc.config
			folder := NewObjectsFolder(Deps{AppConfig: cfg}, gvr, "shop", []string{"pods"}, nil)
			if tc.applied != "" {
				folder.ApplyColumnsMode(tc.applied)
			}
			if got := folder.requestedColumnsMode(); got != tc.want {
				t.Errorf("columns mode = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRestartsShowsTheCountInNormalMode checks the Restarts cell: the count
// alone in the normal mode, the time of the last restart too in the wide
// mode.
func TestRestartsShowsTheCountInNormalMode(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	rl := &tablecache.RowList{
		Columns: []metav1.TableColumnDefinition{
			{Name: "Name"},
			{Name: "Restarts"},
			{Name: "Age"},
			{Name: "IP", Priority: 1},
		},
		Items: []tablecache.Row{
			{Name: "web", Cells: []any{"web", "8 (5m58s ago)", "1h", "10.0.0.1"}},
			{Name: "api", Cells: []any{"api", "0", "1h", "10.0.0.2"}},
		},
	}
	tests := []struct {
		mode string
		want map[string]string
	}{
		{mode: appconfig.ColumnsModeNormal, want: map[string]string{"web": "8", "api": "0"}},
		{mode: appconfig.ColumnsModeWide, want: map[string]string{"web": "8 (5m58s ago)", "api": "0"}},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			folder := NewObjectsFolder(Deps{}, gvr, "shop", []string{"pods"}, nil)
			rows := folder.rowsFromRowList(rl, tc.mode, appconfig.ObjectsOrderName, nil)
			if len(rows) != 2 {
				t.Fatalf("got %d rows, want 2", len(rows))
			}
			for _, r := range rows {
				_, cells, _, _ := r.Columns()
				name := strings.TrimPrefix(cells[0], "/")
				if got := cells[1]; got != tc.want[name] {
					t.Errorf("%s: Restarts = %q, want %q", name, got, tc.want[name])
				}
			}
		})
	}
}
