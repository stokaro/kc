package info

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
)

// TestCompactLayoutKeepsWhatMatters renders Info at the widths of a 25%
// panel in a 150- and a 100-column terminal and at a width just over the
// compact limit. A compact pane leaves out the healthy lines under a bar,
// keeps the lines that need attention and the reasons for n/a, and shows
// a top pod without the namespace the title names.
func TestCompactLayoutKeepsWhatMatters(t *testing.T) {
	t.Parallel()
	summary := func() models.InfoSummary {
		b := attentionBars()
		b[0].More = append(b[0].More, "pods use n/a: loading")
		b[0].MoreHealth = []models.RowHealth{models.RowHealthOK, models.RowHealthOK, models.RowHealthUnknown}
		return models.InfoSummary{
			UsageAt: harnessNow, Bars: b,
			Top: []models.InfoConsumer{
				{Namespace: "shop", Name: "web-6989b69646-nm64t", CPU: "0m", Memory: "7Mi"},
				{Namespace: "shop", Name: "loadgen-86b9887b8-rgzdg", CPU: "218m", Memory: "1Mi"},
			},
			TopTitle: "Top pods by memory in namespace shop", TopNamespace: "shop",
		}
	}
	tests := []struct {
		name    string
		width   int
		compact bool
	}{
		{name: "25% of 150 columns", width: 35, compact: true},
		{name: "25% of 100 columns", width: 23, compact: true},
		{name: "half of 100 columns", width: compactWidth, compact: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startedHarness(t, summary(), tc.width, 40)
			var text []string
			for i, l := range h.w.content(tc.width) {
				if w := ansi.StringWidth(l.text); w > tc.width {
					t.Errorf("line %d is %d cells wide in %d: %q", i, w, tc.width, ansi.Strip(l.text))
				}
				text = append(text, ansi.Strip(l.text))
			}
			all := strings.Join(text, "\n")
			flat := strings.Join(strings.Fields(all), " ")
			for _, want := range []string{"lim 64%", "pods use n/a: loading", "! lim", "under-req", "web-6989b69646-nm64t"} {
				if !strings.Contains(flat, want) {
					t.Errorf("content misses %q:\n%s", want, all)
				}
			}
			for _, healthy := range []string{"node 1.2/4.0 cores capacity", "req 10%, lim 20%"} {
				if got := strings.Contains(flat, healthy); got == tc.compact {
					t.Errorf("healthy line %q shown = %v in width %d:\n%s", healthy, got, tc.width, all)
				}
			}
			if got := strings.Contains(flat, "shop/web-"); got == tc.compact {
				t.Errorf("top pod namespace shown = %v in width %d:\n%s", got, tc.width, all)
			}
		})
	}
}

// TestCompactTopPodKeepsAForeignNamespace checks that a top pod outside
// the titled namespace, or under a title without one, keeps its namespace.
func TestCompactTopPodKeepsAForeignNamespace(t *testing.T) {
	t.Parallel()
	c := models.InfoConsumer{Namespace: "batch", Name: "worker-0", CPU: "5m", Memory: "40Mi"}
	for _, ns := range []string{"", "shop"} {
		got := ansi.Strip(strings.Join(compactConsumerLines(c, ns, [2]int{6, 5}, 35), "\n"))
		if !strings.Contains(got, "batch/worker-0") {
			t.Errorf("namespace %q: %q misses batch/worker-0", ns, got)
		}
	}
	// A name too long for the line goes under the numbers, whole.
	long := models.InfoConsumer{Namespace: "shop", Name: "loadgen-86b9887b8-rgzdg", CPU: "218m", Memory: "1Mi"}
	lines := compactConsumerLines(long, "shop", [2]int{3, 4}, 23)
	if len(lines) < 2 || strings.Contains(lines[0], "loadgen") {
		t.Fatalf("lines = %q, want the numbers alone, then the name", lines)
	}
	if got := strings.Join(strings.Fields(strings.Join(lines[1:], "")), ""); got != long.Name {
		t.Errorf("name lines join to %q, want %q", got, long.Name)
	}
}

// TestCompactPaneLeavesOutAHistoryStillCollecting checks that the
// "collecting" label, which has no line to label yet, shows in a wide
// pane only, and that a compact pane shows the label once there is a line.
func TestCompactPaneLeavesOutAHistoryStillCollecting(t *testing.T) {
	t.Parallel()
	b := bars(25, 55)[0]
	var h history
	h.add(harnessNow, &usageSample{percent: 25, base: "allocatable"}, nil)
	block := func(width int) string {
		return ansi.Strip(strings.Join(barBlock(b, &h, harnessNow, width, barCells([]models.InfoBar{b}, width)), "\n"))
	}
	if got := block(60); !strings.Contains(got, "collecting, 1 sample") {
		t.Errorf("wide pane misses the collecting label:\n%s", got)
	}
	if got := block(35); strings.Contains(got, "collecting") {
		t.Errorf("compact pane shows the collecting label:\n%s", got)
	}
	now := harnessNow
	for range 3 {
		now = now.Add(15 * time.Second)
		h.add(now, &usageSample{percent: 25, base: "allocatable"}, nil)
	}
	got := ansi.Strip(strings.Join(barBlock(b, &h, now, 35, barCells([]models.InfoBar{b}, 35)), "\n"))
	if !strings.Contains(got, "history") || !strings.Contains(got, "▕") {
		t.Errorf("compact pane misses the history once it has a line:\n%s", got)
	}
}

// TestCompactTopPodNameStaysOnItsLine checks that the number columns of
// the top pods are as wide as their values, so a typical pod name fits
// after them in a 25% panel of a 150-column terminal.
func TestCompactTopPodNameStaysOnItsLine(t *testing.T) {
	t.Parallel()
	top := []models.InfoConsumer{
		{Namespace: "shop", Name: "loadgen-86b9887b8-rgzdg", CPU: "218m", Memory: "8Mi"},
		{Namespace: "shop", Name: "web-6989b69646-nm64t", CPU: "0m", Memory: "5Mi"},
	}
	cols := consumerColumns(top)
	for _, c := range top {
		lines := compactConsumerLines(c, "shop", cols, 35)
		if len(lines) != 1 || !strings.HasSuffix(lines[0], c.Name) || !strings.Contains(lines[0], c.CPU) {
			t.Errorf("%s: lines = %q, want one line with the numbers and the name", c.Name, lines)
		}
	}
	// The columns line up: every name starts in the same cell.
	a := compactConsumerLines(top[0], "shop", cols, 35)[0]
	b := compactConsumerLines(top[1], "shop", cols, 35)[0]
	if strings.Index(a, "loadgen") != strings.Index(b, "web-") {
		t.Errorf("names start in different cells:\n%q\n%q", a, b)
	}
}
