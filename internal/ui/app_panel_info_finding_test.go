package ui

import (
	"testing"

	"github.com/stokaro/kc/internal/models"
	infowidget "github.com/stokaro/kc/internal/ui/panelcontent/info"
)

// previewRow is a row that describes itself as a finding.
type previewRow struct {
	models.Item
	preview models.FindingPreview
	ok      bool
}

func (r previewRow) FindingPreview() (models.FindingPreview, bool) { return r.preview, r.ok }

func TestNarrowInfoTarget(t *testing.T) {
	t.Parallel()
	plain := models.NewSimpleItem("x", []string{"x"}, nil, nil)
	tests := []struct {
		name    string
		item    models.Item
		finding string
		node    string
	}{
		{name: "a finding", item: previewRow{Item: plain, preview: models.FindingPreview{ID: "health/pods-pending"}, ok: true}, finding: "health/pods-pending"},
		{name: "a finding the report no longer has", item: previewRow{Item: plain}},
		{name: "a node", item: models.NewObjectRow("n1", []string{"n1"}, nil, infoNodesGVR, "", "n1", nil), node: "n1"},
		{name: "another row", item: plain},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var target infowidget.Target
			narrowInfoTarget(&target, tc.item)
			got := ""
			if target.Finding != nil {
				got = target.Finding.ID
			}
			if got != tc.finding || target.Node != tc.node || target.Workload != nil {
				t.Fatalf("target = finding %q node %q workload %v", got, target.Node, target.Workload)
			}
		})
	}
}
