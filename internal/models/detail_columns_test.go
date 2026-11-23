package models

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
)

// The detail column of /diagnosis, the related folders and /timeline is
// free text. It takes the rest of the line and gives up width first, so the
// columns that name the object keep their natural width; only below the
// detail column's floor (reasonMinWidth) do they narrow, and the line still
// fits without panning. At half of a 200-column terminal a panel has about
// 98 columns, at half of a 120-column one about 58.
func TestDetailColumnGetsTheRemainingWidth(t *testing.T) {
	t.Parallel()
	evidence := "Back-off pulling image registry.example.com/shop/checkout:v2.4.1 for container app"
	change := "status.conditions[Ready] False -> True; status.readyReplicas 2 -> 3"
	tests := []struct {
		name  string
		width int
		cols  []table.Column
		cells []string
		// whole cells must show in full; detail must show from its start.
		whole  []string
		detail string
	}{
		{
			name:   "diagnosis, wide pane",
			width:  98,
			cols:   relatedColumns("Evidence"),
			cells:  []string{"checkout-7d9c8b6f5-x2x4q", "pods", "payments-production", evidence},
			whole:  []string{"checkout-7d9c8b6f5-x2x4q", "pods", "payments-production", "Evidence"},
			detail: evidence[:40],
		},
		{
			name:   "diagnosis, narrow pane",
			width:  58,
			cols:   relatedColumns("Evidence"),
			cells:  []string{"checkout-7d9c8b6f5-x2x4q", "pods", "payments-production", evidence},
			whole:  []string{"pods", "Evidence"},
			detail: evidence[:reasonMinWidth-1],
		},
		{
			name:   "timeline",
			width:  58,
			cols:   timelineColumns(),
			cells:  []string{"2026-11-23T10:15:00Z", "spec+metadata+status", "123456789012", change},
			whole:  []string{"2026-11-23T10:15:00Z", "Change"},
			detail: change[:reasonMinWidth-1],
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rows := []table.Row{table.SimpleRow{ID: "a", Cells: tc.cells}}
			bt := table.NewBigTable(tc.cols, table.NewSliceList(rows), tc.width, 6)
			bt.Refresh(t.Context())
			view := ansi.Strip(bt.View().Content)
			for i, l := range strings.Split(view, "\n") {
				if w := ansi.StringWidth(l); w > tc.width {
					t.Errorf("line %d is %d cells wide, want at most %d: %q", i, w, tc.width, l)
				}
			}
			for _, w := range tc.whole {
				if !strings.Contains(view, w) {
					t.Errorf("view misses %q:\n%s", w, view)
				}
			}
			if !strings.Contains(view, tc.detail) {
				t.Errorf("the detail column shows less than %q:\n%s", tc.detail, view)
			}
			if last := tc.cols[len(tc.cols)-1]; !last.Fill {
				t.Errorf("detail column %q is not a Fill column", last.Title)
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
