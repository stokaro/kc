package info

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// TestWidgetScopeTitles checks that the bars and the top pods carry their
// own scope labels, and that a new context shows none of the old ones
// before its first summary.
func TestWidgetScopeTitles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.summary = models.InfoSummary{
		UsageAt: h.now, Bars: bars(25, 55),
		BarsTitle: "Cluster capacity (all nodes, all namespaces)", TopTitle: "Top pods by memory in namespace ns",
		TopNote: "no pods in ns",
	}
	h.w.SetFocus(ctx, true)
	_ = h.w.Init(ctx)
	view := ansi.Strip(strings.Join(h.w.lines(60, 40), "\n"))
	for _, want := range []string{
		" ctx: namespace ns\n",
		"\n Cluster capacity (all nodes, all namespaces)\n CPU",
		"\n Top pods by memory in namespace ns\n   no pods in ns",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view misses %q:\n%s", want, view)
		}
	}

	h.summary = models.InfoSummary{Loading: true}
	h.setScope("other|ns", "other: namespace ns")
	h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	view = ansi.Strip(strings.Join(h.w.lines(60, 40), "\n"))
	if strings.Contains(view, "Cluster capacity") || strings.Contains(view, "Top pods") || !strings.Contains(view, "Loading...") {
		t.Fatalf("a new context shows the old labels:\n%s", view)
	}
}
