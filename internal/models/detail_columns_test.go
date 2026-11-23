package models

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
)

// At half of a 120-column terminal a panel has about 58 columns. The
// detail column of /diagnosis, the related folders and /timeline gets the
// width the other columns give up: long names, resources and namespaces
// are cut first, and the line fits without panning.
func TestDetailColumnGetsTheRemainingWidth(t *testing.T) {
	t.Parallel()
	const width = 58
	evidence := "Back-off pulling image registry.example.com/shop/checkout:v2.4.1 for container app"
	change := "status.conditions[Ready] False -> True; status.readyReplicas 2 -> 3"
	tests := []struct {
		name   string
		cols   []table.Column
		cells  []string
		detail string
		// keep is how much of the detail must show, from its start.
		keep int
	}{
		{
			name:   "diagnosis",
			cols:   relatedColumns("Evidence"),
			cells:  []string{"checkout-7d9c8b6f5-x2x4q", "pods", "payments-production", evidence},
			detail: evidence, keep: 28,
		},
		{
			name:   "timeline",
			cols:   timelineColumns(),
			cells:  []string{"2026-11-23T10:15:00Z", "spec+metadata+status", "123456789012", change},
			detail: change, keep: 20,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rows := []table.Row{table.SimpleRow{ID: "a", Cells: tc.cells}}
			bt := table.NewBigTable(tc.cols, table.NewSliceList(rows), width, 6)
			bt.Refresh(t.Context())
			view := ansi.Strip(bt.View().Content)
			for i, l := range strings.Split(view, "\n") {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("line %d is %d cells wide, want at most %d: %q", i, w, width, l)
				}
			}
			if want := tc.detail[:tc.keep]; !strings.Contains(view, want) {
				t.Errorf("the detail column shows less than %q:\n%s", want, view)
			}
			for i, c := range tc.cols {
				if last := i == len(tc.cols)-1; last != (c.MinWidth == 0) && i != 0 {
					t.Errorf("column %q MinWidth %d: only the detail column may keep its width", c.Title, c.MinWidth)
				}
			}
		})
	}
}

// The status line of a related or diagnosis row names the object and says
// why it is listed, so the cut detail cell can be read in full.
func TestRelatedRowDetailsCarryTheReason(t *testing.T) {
	t.Parallel()
	ref := RelatedRef{
		GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespace: "shop", Name: "web-1",
		Relation: "Back-off pulling image registry.example.com/shop/web:v2",
	}
	rows := relatedRows(Deps{}, nil, []string{"x"}, []RelatedRef{ref})
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	d, ok := rows[0].(interface{ Details() string })
	if !ok {
		t.Fatalf("row %T has no details", rows[0])
	}
	if got := d.Details(); !strings.Contains(got, "shop/web-1") || !strings.HasSuffix(got, ": "+ref.Relation) {
		t.Errorf("details = %q, want the object and the reason", got)
	}
}
