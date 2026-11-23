package info

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

func topPods(n int) []models.InfoConsumer {
	out := make([]models.InfoConsumer, n)
	for i := range out {
		out[i] = models.InfoConsumer{Namespace: "shop", Name: fmt.Sprintf("web-%d", i), CPU: "10m", Memory: fmt.Sprintf("%dMi", 900-i)}
	}
	return out
}

// attentionBars carry every word that must survive a narrow pane: a
// state flag, an attention mark and the units of the lines under the bar.
func attentionBars() []models.InfoBar {
	b := bars(25, 55)
	b[1].AttentionText, b[1].Attention = "! lim", models.RowHealthNotReady
	// As in a real summary: the line the mark names is in that state.
	b[1].MoreHealth = []models.RowHealth{models.RowHealthOK, models.RowHealthNotReady}
	return b
}

func shown(h *harness, width, height int) string {
	return ansi.Strip(strings.Join(h.w.lines(width, height), "\n"))
}

func startedHarness(t *testing.T, summary models.InfoSummary, width, height int) *harness {
	t.Helper()
	h := newHarness(t)
	h.summary = summary
	h.w.SetFocus(t.Context(), true)
	_ = h.w.Init(t.Context())
	_, _ = h.w.Update(t.Context(), tea.WindowSizeMsg{Width: width, Height: height})
	return h
}

// TestWidgetLayoutAtTerminalSizes renders the Info pane as it is at an
// 80x24, a 120x30 and a wide terminal (about half the columns, less the
// frame): no line is wider than the pane, every state word, mark and unit
// is in the content, and the bar's percentage stays next to the bar.
func TestWidgetLayoutAtTerminalSizes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		inline        bool // the first line under a bar moves next to it
	}{
		{name: "80x24", width: 38, height: 19},
		{name: "120x30", width: 58, height: 25},
		{name: "wide", width: 98, height: 40, inline: true},
		{name: "very narrow", width: 22, height: 19},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startedHarness(t, models.InfoSummary{UsageAt: harnessNow, Bars: attentionBars(), Top: topPods(2)}, tc.width, tc.height)
			all := h.w.content(tc.width)
			var text []string
			for i, l := range all {
				if w := ansi.StringWidth(l.text); w > tc.width {
					t.Errorf("line %d is %d cells wide in %d: %q", i, w, tc.width, ansi.Strip(l.text))
				}
				text = append(text, ansi.Strip(l.text))
			}
			// Wrapping may split a line; the words stay whole.
			flat := strings.Join(strings.Fields(strings.Join(text, " ")), " ")
			for _, want := range []string{"under-req", "! lim", "55%", "25%", "10.0 Gi allocatable", "4.0 cores", "lim 64%", "shop/web-1"} {
				if !strings.Contains(flat, want) {
					t.Errorf("content misses %q:\n%s", want, strings.Join(text, "\n"))
				}
			}
			for _, line := range text {
				if !strings.HasPrefix(line, " CPU") {
					continue
				}
				bar := strings.TrimRight(strings.TrimPrefix(line, " CPU    "), " ")
				cells := strings.IndexFunc(bar, func(r rune) bool { return r != '█' && r != '░' && !strings.ContainsRune(string(eighths), r) })
				if n := len([]rune(bar[:max(cells, 0)])); n > maxBarCells {
					t.Errorf("bar is %d cells, want at most %d: %q", n, maxBarCells, line)
				}
				if !strings.Contains(line, " 25%") {
					t.Errorf("the CPU line misses its percentage next to the bar: %q", line)
				}
				if got := strings.Contains(line, "pods 1.0/4.0 cores allocatable"); got != tc.inline {
					t.Errorf("detail next to the bar = %v, want %v: %q", got, tc.inline, line)
				}
			}
		})
	}
}

// harnessNow is the clock of newHarness.
var harnessNow = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

// TestBarLineSpillsWhatDoesNotFit checks that a narrow pane moves the
// mark, then the percentage, to the next line instead of cutting them.
func TestBarLineSpillsWhatDoesNotFit(t *testing.T) {
	t.Parallel()
	b := attentionBars()[1] // " 55% under-req" and " ! lim"
	tests := []struct {
		width       int
		line, spill string
	}{
		{width: 40, line: " 55% under-req ! lim"},
		{width: 27, line: " 55% under-req", spill: "! lim"},
		{width: 16, spill: "55% under-req ! lim"},
	}
	for _, tc := range tests {
		line, spill := barLine(b, tc.width, barCells([]models.InfoBar{b}, tc.width))
		line, spill = ansi.Strip(line), ansi.Strip(spill)
		if tc.line != "" && !strings.HasSuffix(line, tc.line) {
			t.Errorf("width %d: line %q, want it to end in %q", tc.width, line, tc.line)
		}
		if spill != tc.spill {
			t.Errorf("width %d: spill %q, want %q", tc.width, spill, tc.spill)
		}
		if w := ansi.StringWidth(line); w > tc.width {
			t.Errorf("width %d: line is %d cells: %q", tc.width, w, line)
		}
	}
}

// TestWidgetScrollsAndSelectsTopPods drives the keys: scrolling reaches the
// last line and back, the frame marks hidden lines, Up and Down select the
// top pods on screen, and Enter opens the selected one by its full GVR.
func TestWidgetScrollsAndSelectsTopPods(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const width, height = 50, 8
	h := startedHarness(t, models.InfoSummary{UsageAt: harnessNow, Bars: bars(25, 55), Top: topPods(3)}, width, height)
	key := func(k string) tea.Cmd {
		t.Helper()
		cmd, handled := h.w.handleKey(k)
		if !handled {
			t.Fatalf("%s was not handled", k)
		}
		return cmd
	}
	total := len(h.w.content(width))
	if total <= height {
		t.Fatalf("content has %d lines, the test needs more than %d", total, height)
	}
	info := h.w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: width})
	if info.TopIndicator == "^" || info.BottomIndicator != "v" || !strings.Contains(info.FooterStatus, fmt.Sprintf("1-%d/%d", height, total)) {
		t.Fatalf("top of the content: frame %+v", info)
	}
	if !strings.Contains(info.FooterStatus, "Down selects a pod") {
		t.Fatalf("no hint how to select a pod: %q", info.FooterStatus)
	}

	// End shows the last line, a top pod; Home the first.
	key("end")
	if view := shown(h, width, height); !strings.Contains(view, "shop/web-2") || strings.Contains(view, "ctx: namespace ns") {
		t.Fatalf("end:\n%s", view)
	}
	info = h.w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: width})
	if info.TopIndicator != "^" || info.BottomIndicator == "v" {
		t.Fatalf("bottom of the content: frame %+v", info)
	}
	key("home")
	if view := shown(h, width, height); !strings.HasPrefix(view, " ctx: namespace ns") {
		t.Fatalf("home:\n%s", view)
	}
	// PgDn moves by a page less one line, until the end.
	key("pgdown")
	if h.w.offset != height-1 {
		t.Fatalf("offset after PgDn = %d, want %d", h.w.offset, height-1)
	}
	key("pgup")
	if h.w.offset != 0 {
		t.Fatalf("offset after PgUp = %d", h.w.offset)
	}

	// Down scrolls until the next pod is on screen and selects it, so it
	// selects each pod in turn; past the last one it changes nothing.
	var seen []string
	for range total + 2 {
		key("down")
		if f := h.w.focus; f != "" && (len(seen) == 0 || seen[len(seen)-1] != f) {
			seen = append(seen, f)
		}
		if cur := h.w.focusIndex(); cur >= 0 && !h.w.visible(func() [2]int { _, s := h.w.spans(); return s[cur] }()) {
			t.Fatalf("selected pod %q is off screen at offset %d", h.w.focus, h.w.offset)
		}
	}
	if got := strings.Join(seen, ","); got != "shop/web-0,shop/web-1,shop/web-2" || h.w.focus != "shop/web-2" {
		t.Fatalf("Down selected %s, ending at %q", got, h.w.focus)
	}
	if info := h.w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: width}); !strings.Contains(info.FooterStatus, "Enter opens the pod") {
		t.Fatalf("no hint how to open the pod: %q", info.FooterStatus)
	}
	lines := h.w.lines(width, height)
	if last := lines[len(lines)-1]; ansi.Strip(last) == last || !strings.Contains(ansi.Strip(last), "shop/web-2") {
		t.Fatalf("the selected pod is not highlighted: %q", last)
	}
	msg := key("enter")()
	req, ok := msg.(models.OpenObjectRequest)
	if !ok {
		t.Fatalf("Enter returned %T, want OpenObjectRequest", msg)
	}
	ref, missing := req.Link.LinkTarget()
	want := models.ObjectRef{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true, Namespace: "shop", Name: "web-2"}
	if ref != want || missing != "" {
		t.Fatalf("link = %+v %q, want %+v", ref, missing, want)
	}

	// Up walks back and clears the selection above the first pod; Enter
	// without a selection is left to the panel.
	for range 3 {
		key("up")
	}
	if h.w.focus != "" {
		t.Fatalf("focus after Up past the first pod = %q", h.w.focus)
	}
	if _, handled := h.w.handleKey("enter"); handled {
		t.Fatal("Enter without a selected pod was handled")
	}
	// Without a selection Up only scrolls, and Down at the end selects the
	// first pod on screen. Home scrolls it off screen and drops it.
	offset := h.w.offset
	key("up")
	if h.w.focus != "" || h.w.offset != max(offset-1, 0) {
		t.Fatalf("Up without a selection: focus %q offset %d, want none and %d", h.w.focus, h.w.offset, max(offset-1, 0))
	}
	key("end")
	key("down")
	if h.w.focus == "" {
		t.Fatal("Down at the end selected no pod")
	}
	key("home")
	if h.w.focus != "" {
		t.Fatalf("a pod off screen stays selected: %q", h.w.focus)
	}
}

// TestWidgetMouse checks the wheel scrolls and a click selects a top pod
// and a second click opens it; a click elsewhere selects nothing.
func TestWidgetMouse(t *testing.T) {
	t.Parallel()
	const width, height = 50, 8
	h := startedHarness(t, models.InfoSummary{UsageAt: harnessNow, Bars: bars(25, 55), Top: topPods(3)}, width, height)
	if _, handled := h.w.Update(t.Context(), panelcontent.MouseMsg{Intent: panelcontent.MouseIntentWheel, DeltaY: 1}); !handled || h.w.offset != 1 {
		t.Fatalf("wheel down: handled %v, offset %d", handled, h.w.offset)
	}
	click := func(row int) tea.Cmd {
		cmd, _ := h.w.Update(t.Context(), panelcontent.MouseMsg{Intent: panelcontent.MouseIntentClick, Button: tea.MouseLeft, Row: row})
		return cmd
	}
	if cmd := click(0); cmd != nil || h.w.focus != "" {
		t.Fatalf("a click on a bar: cmd %v, focus %q", cmd != nil, h.w.focus)
	}
	h.w.offset = len(h.w.content(width)) // clamps to the end
	lines := h.w.lines(width, height)
	row := -1
	for i, l := range lines {
		if strings.Contains(ansi.Strip(l), "shop/web-1") {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("web-1 not on screen:\n%s", strings.Join(lines, "\n"))
	}
	if cmd := click(row); cmd != nil || h.w.focus != "shop/web-1" {
		t.Fatalf("first click: cmd %v, focus %q", cmd != nil, h.w.focus)
	}
	cmd := click(row)
	if cmd == nil {
		t.Fatal("a click on the selected pod opened nothing")
	}
	if ref, _ := cmd().(models.OpenObjectRequest).Link.LinkTarget(); ref.Name != "web-1" {
		t.Fatalf("opened %+v", ref)
	}
}

// TestWidgetKeepsTheSelectionByIdentity checks live updates and scope
// changes: a pod that moves in the list stays selected, one that leaves
// it is no longer selected, and another scope starts at the top.
func TestWidgetKeepsTheSelectionByIdentity(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const width, height = 50, 8
	h := startedHarness(t, models.InfoSummary{UsageAt: harnessNow, Bars: bars(25, 55), Top: topPods(3)}, width, height)
	h.w.offset = 100
	h.w.lines(width, height)
	h.w.setFocus(1)

	reordered := topPods(3)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	h.last().summary.Top = reordered
	_, _ = h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen})
	if h.w.focus != "shop/web-1" || h.w.focusIndex() != 0 {
		t.Fatalf("after a reorder focus = %q at %d, want shop/web-1 at 0", h.w.focus, h.w.focusIndex())
	}
	h.last().summary.Top = topPods(1)
	_, _ = h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen})
	if h.w.focus != "" {
		t.Fatalf("a pod that left the list stays selected: %q", h.w.focus)
	}
	// Content that shrinks clamps the offset.
	if h.w.lines(width, height); h.w.offset != len(h.w.content(width))-height {
		t.Fatalf("offset %d not clamped to %d", h.w.offset, len(h.w.content(width))-height)
	}

	h.last().summary.Top = topPods(3)
	_, _ = h.w.Update(ctx, tickMsg{w: h.w, gen: h.w.gen})
	h.w.setFocus(2)
	if h.w.focus != "shop/web-2" {
		t.Fatalf("focus = %q, want shop/web-2", h.w.focus)
	}
	h.summary.Top = topPods(3)
	h.setScope("ctx|other", "ctx: namespace other")
	_ = h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if h.w.offset != 0 || h.w.focus != "" {
		t.Fatalf("a new scope kept offset %d and focus %q", h.w.offset, h.w.focus)
	}
	// So does a node under the other panel's cursor.
	h.w.offset = 3
	h.target.Node = "n1"
	_ = h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if h.w.offset != 0 {
		t.Fatalf("a node selection kept offset %d", h.w.offset)
	}
}

// TestWidgetScrollsAFindingPreview checks that the preview of an overview
// finding scrolls like the summary, offers no pod to select, and that
// another finding starts at the top.
func TestWidgetScrollsAFindingPreview(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const width, height = 50, 6
	h := startedHarness(t, models.InfoSummary{UsageAt: harnessNow, Bars: bars(25, 55), Top: topPods(3)}, width, height)
	finding := &models.FindingPreview{ID: "health/pods-pending", Title: "/pods pending", Scope: "namespace shop", Value: "12", State: "warning",
		Why: "Pods in phase Pending.", TargetCount: 12, Inputs: 1,
		Targets: []models.FindingPreviewTarget{{Resource: "pods", Namespace: "shop", Name: "web-1", Value: "Unschedulable"}}}
	h.target.Finding = finding
	_ = h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if total := len(h.w.content(width)); total <= height {
		t.Fatalf("preview has %d lines, the test needs more than %d", total, height)
	}
	_, _ = h.w.handleKey("end")
	if view := shown(h, width, height); !strings.Contains(view, "M-c copies the 1 input command") {
		t.Fatalf("End did not reach the last line:\n%s", view)
	}
	info := h.w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: width})
	if info.TopIndicator != "^" || strings.Contains(info.FooterStatus, "selects a pod") {
		t.Fatalf("frame %+v", info)
	}
	if _, _ = h.w.handleKey("down"); h.w.focus != "" {
		t.Fatalf("Down selected %q in a finding preview", h.w.focus)
	}
	other := *finding
	other.ID = "health/pods-crashloop"
	h.target.Finding = &other
	_ = h.w.OnSelectionChanged(ctx, panelcontent.Selection{})
	if h.w.offset != 0 {
		t.Fatalf("another finding kept offset %d", h.w.offset)
	}
}
