package models

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/internal/table"
)

// TestOverviewRowsKeepTheirStateInANarrowPane renders /overview in the
// pane of an 80x24 terminal (38 columns): every row shows its whole state
// without horizontal panning, and a name or value that does not fit ends
// in "…" (ADR 0176). A 120-column pane cuts nothing.
func TestOverviewRowsKeepTheirStateInANarrowPane(t *testing.T) {
	t.Parallel()
	objs := []crclient.Object{readyNode("n1", "4", "8Gi", 110)}
	for i := range 3 {
		objs = append(objs, waiting(overviewRunningPod("shop", fmt.Sprintf("loop-%02d", i)), "CrashLoopBackOff"))
	}
	deps := Deps{Ctx: t.Context(), CtxName: "dev", KubectlContext: "dev"}
	e := newOverviewEngine(deps, "shop")
	e.close()
	e.mu.Lock()
	e.report = measuredReport(t, "shop", &overviewMeasured{}, objs...)
	e.mu.Unlock()
	f := newOverviewFolder(deps, "shop", []string{"overview"}, e)
	rows := f.Lines(t.Context(), 0, 200)
	for _, tc := range []struct {
		width int
		cut   bool
	}{{width: 38, cut: true}, {width: 120}} {
		bt := table.NewBigTable(f.Columns(), f, tc.width, len(rows)+2)
		bt.Refresh(t.Context())
		lines := strings.Split(ansi.Strip(bt.View().Content), "\n")
		if len(lines) < len(rows)+1 {
			t.Fatalf("width %d: %d lines for %d rows", tc.width, len(lines), len(rows))
		}
		checked, cut := 0, false
		for i, r := range rows {
			_, cells, _, _ := r.Columns()
			line := lines[i+1]
			if w := ansi.StringWidth(line); w > tc.width {
				t.Errorf("width %d: line %q is %d cells", tc.width, line, w)
			}
			cut = cut || strings.Contains(line, "…")
			if len(cells) < 3 || cells[2] == "" {
				continue
			}
			checked++
			if !strings.HasSuffix(strings.TrimRight(line, " "), cells[2]) {
				t.Errorf("width %d: row %v shows %q, want it to end in its state", tc.width, cells, line)
			}
		}
		if checked == 0 {
			t.Fatalf("width %d: no row with a state", tc.width)
		}
		if cut != tc.cut {
			t.Errorf("width %d: cut = %v, want %v:\n%s", tc.width, cut, tc.cut, strings.Join(lines, "\n"))
		}
	}
}
