package ui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/setdiff"
)

var selectionGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// objectPanel shows "..", then cm-0 to cm-9 of namespace shop as object
// rows with UIDs, in a folder that filters by name.
func objectPanel(t *testing.T) *Panel {
	t.Helper()
	ctx := t.Context()
	f := &filterFolder{}
	f.all = append(f.all, models.BackItem{})
	for i := range 10 {
		name := fmt.Sprintf("cm-%d", i)
		row := models.NewObjectRow(name, []string{name}, nil, selectionGVR, "shop", name, nil)
		row.SetInstance(types.UID("uid-"+name), models.ObjectOrigin{})
		f.all = append(f.all, row)
	}
	// Another resource in the same folder never joins the selection.
	f.all = append(f.all, models.NewObjectRow("cm-x", []string{"cm-x"}, nil, schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, "shop", "cm-x", nil))
	f.SetRowFilter(models.RowFilter{})
	p := NewPanel("t")
	p.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	p.UseFolder(ctx, true)
	p.SetFolder(ctx, f, false)
	return p
}

func memberNames(sel setdiff.Selection) []string {
	out := make([]string, 0, len(sel.Members))
	for _, m := range sel.Members {
		out = append(out, m.Namespace+"/"+m.Name+"@"+string(m.UID))
	}
	return out
}

func TestPanelSelection(t *testing.T) {
	ctx := t.Context()

	t.Run("no filter, no marks: whole list", func(t *testing.T) {
		p := objectPanel(t)
		if sel := panelSelection(ctx, p, selectionGVR); sel.Scope != setdiff.Whole || sel.Members != nil {
			t.Errorf("selection %+v, want the whole list", sel)
		}
	})

	t.Run("filter: the rows it keeps", func(t *testing.T) {
		p := objectPanel(t)
		p.SetRowFilter(ctx, mustFilter(t, "^cm-[01]$"))
		sel := panelSelection(ctx, p, selectionGVR)
		got := memberNames(sel)
		if sel.Scope != setdiff.Visible || sel.Filter != "^cm-[01]$" || len(got) != 2 || got[0] != "shop/cm-0@uid-cm-0" || got[1] != "shop/cm-1@uid-cm-1" {
			t.Errorf("selection %v %q %v", sel.Scope, sel.Filter, got)
		}
	})

	t.Run("filter that keeps nothing: an empty visible set", func(t *testing.T) {
		p := objectPanel(t)
		p.SetRowFilter(ctx, mustFilter(t, "^nothing$"))
		sel := panelSelection(ctx, p, selectionGVR)
		if sel.Scope != setdiff.Visible || len(sel.Members) != 0 {
			t.Errorf("selection %+v, want an empty visible set, not the whole list", sel)
		}
	})

	t.Run("marks win over the filter", func(t *testing.T) {
		p := objectPanel(t)
		p.SetRowFilter(ctx, mustFilter(t, "cm"))
		if n, err := p.listWidget(ctx).MarkGroup(ctx, "cm-[37x]", true); err != nil || n != 3 {
			t.Fatalf("MarkGroup = %d, %v", n, err)
		}
		sel := panelSelection(ctx, p, selectionGVR)
		got := memberNames(sel)
		// cm-x is a Secret: marked, but not of the compared resource.
		if sel.Scope != setdiff.Marked || len(got) != 2 || got[0] != "shop/cm-3@uid-cm-3" || got[1] != "shop/cm-7@uid-cm-7" {
			t.Errorf("selection %v %v", sel.Scope, got)
		}
	})
}

func TestPairSelections(t *testing.T) {
	t.Parallel()
	side := func(s setdiff.Scope) setSide {
		return setSide{set: setdiff.Set{Selection: setdiff.Selection{Scope: s}}}
	}
	for _, tc := range []struct {
		name        string
		left, right setdiff.Scope
		wantL       setdiff.Scope
		wantR       setdiff.Scope
	}{
		{"both whole", setdiff.Whole, setdiff.Whole, setdiff.Whole, setdiff.Whole},
		{"left filtered", setdiff.Visible, setdiff.Whole, setdiff.Visible, setdiff.Counterparts},
		{"right marked", setdiff.Whole, setdiff.Marked, setdiff.Counterparts, setdiff.Marked},
		{"both narrowed", setdiff.Marked, setdiff.Visible, setdiff.Marked, setdiff.Visible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, r := side(tc.left), side(tc.right)
			pairSelections(&l, &r)
			if l.set.Selection.Scope != tc.wantL || r.set.Selection.Scope != tc.wantR {
				t.Errorf("got %v/%v, want %v/%v", l.set.Selection.Scope, r.set.Selection.Scope, tc.wantL, tc.wantR)
			}
		})
	}
}
