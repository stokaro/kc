package workloadaction_test

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/workloadaction"
)

func TestRowsPerWorkloadKind(t *testing.T) {
	tests := []struct {
		gvr  schema.GroupVersionResource
		want []string
	}{
		{gvr: workloadaction.DeploymentsGVR, want: []string{"Scale...", "Restart...", "Roll back..."}},
		{gvr: workloadaction.StatefulSetsGVR, want: []string{"Scale...", "Restart..."}},
		{gvr: workloadaction.DaemonSetsGVR, want: []string{"Restart..."}},
		{gvr: workloadaction.ReplicaSetsGVR, want: []string{"Scale..."}},
		// Same resource name in another group: no actions.
		{gvr: schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "deployments"}, want: nil},
		{gvr: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.gvr.String(), func(t *testing.T) {
			t.Parallel()
			env, _, _ := newEnv(t)
			r := ref(tt.gvr, "web")
			rows := workloadaction.Rows(env, r, []string{"namespaces", ns, "deployments", "web"})
			var labels []string
			ids := make(map[string]bool)
			for _, row := range rows {
				id, cells, _, _ := row.Columns()
				labels = append(labels, cells[0])
				ids[id] = true
				mr, ok := row.(models.MessageRow)
				if !ok {
					t.Fatalf("row %q is not a MessageRow", cells[0])
				}
				req, ok := mr.EnterMessage().(workloadaction.Request)
				if !ok || req.Ref != r || req.Action.Label != cells[0] {
					t.Errorf("row %q sends %#v, want a request for %v", cells[0], mr.EnterMessage(), r)
				}
				if row.(models.Item).Details() == "" {
					t.Errorf("row %q has no status line text", cells[0])
				}
			}
			if !slices.Equal(labels, tt.want) {
				t.Errorf("rows = %q, want %q", labels, tt.want)
			}
			if len(ids) != len(rows) {
				t.Errorf("row IDs are not unique: %v", ids)
			}
		})
	}
}
