package ui

import (
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestLayoutFooterSlots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		natural    []int
		keep       int
		width      int
		wantN      int
		wantStarts []int
	}{
		{
			name: "equal slots when every key fits", natural: []int{5, 8, 6}, keep: 3, width: 30,
			wantN: 3, wantStarts: []int{0, 10, 20, 30},
		},
		{
			name: "natural widths share the rest", natural: []int{4, 16, 4}, keep: 3, width: 30,
			wantN: 3, wantStarts: []int{0, 6, 24, 30},
		},
		{
			name: "keys past keep leave from the end", natural: []int{6, 6, 10, 10, 10}, keep: 2, width: 30,
			wantN: 3, wantStarts: []int{0, 9, 18, 30},
		},
		{
			name: "a bar too narrow for keep cuts labels", natural: []int{20, 20, 20}, keep: 2, width: 30,
			wantN: 2, wantStarts: []int{0, 15, 30},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, starts := layoutFooterSlots(tc.natural, tc.keep, tc.width)
			if n != tc.wantN || !slices.Equal(starts, tc.wantStarts) {
				t.Fatalf("layoutFooterSlots = %d %v, want %d %v", n, starts, tc.wantN, tc.wantStarts)
			}
		})
	}
}

// viewerBarHints are the keys of the YAML viewer, which used to cut
// "M-a Copy all" to "Copy al".
func viewerBarHints() []FooterHint {
	return append(viewerHints(),
		FooterHint{Key: "n", Label: "Next", Enabled: false},
		FooterHint{Key: "C-r", Label: "Reread", Enabled: true},
		FooterHint{Key: "M-a", Label: "Copy all", Enabled: true},
		FooterHint{Key: "M-c", Label: "Copy cmd", Enabled: true},
	)
}

func fullscreenModal(title string, content *hintedModel, width int) *Modal {
	m := NewModal(title, content)
	m.SetMode(ModalModeFullscreen)
	m.SetDimensions(width, 20)
	m.Show()
	return m
}

func TestFullscreenModalShowsWholeLabelsAndStatusOnTitleLine(t *testing.T) {
	t.Parallel()
	m := fullscreenModal("/deployments/web", &hintedModel{hints: viewerBarHints(), status: "paged, 2.4 MiB · line 39962/40000"}, 150)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	top, bar := lines[0], lines[len(lines)-1]
	for _, want := range []string{"/deployments/web", "paged, 2.4 MiB · line 39962/40000"} {
		if !strings.Contains(top, want) {
			t.Errorf("title line %q lacks %q", top, want)
		}
	}
	if !strings.HasSuffix(top, "39962/40000 ──") {
		t.Errorf("status should end the title line before the rule cell and the line end, got %q", top)
	}
	if strings.Contains(bar, "39962") {
		t.Errorf("the key bar should not carry the status, got %q", bar)
	}
	for _, want := range []string{"10Close", "nNext", "C-rReread", "M-aCopy all", "M-cCopy cmd"} {
		if !strings.Contains(bar, want) {
			t.Errorf("key bar %q lacks %q", bar, want)
		}
	}
	if got := lipgloss.Width(lines[0]); got != 150 {
		t.Errorf("title line is %d cells, want 150", got)
	}
}

func TestFullscreenModalCutsLongTitleFromTheStart(t *testing.T) {
	t.Parallel()
	title := "/local/" + strings.Repeat("very-long-directory/", 8) + "access.log"
	m := fullscreenModal(title, &hintedModel{hints: viewerBarHints(), status: "paged, 2.4 MiB, highlighting off · line 39962/40000"}, 120)
	top, _, _ := strings.Cut(ansi.Strip(m.View().Content), "\n")
	if !strings.Contains(top, "line 39962/40000") || !strings.Contains(top, "…") || !strings.Contains(top, "access.log") {
		t.Fatalf("title line should keep the status and the end of the title, got %q", top)
	}
	if got := lipgloss.Width(top); got != 120 {
		t.Fatalf("title line is %d cells, want 120", got)
	}
}

func TestFullscreenModalLeavesOutKeysThatDoNotFit(t *testing.T) {
	t.Parallel()
	hints := viewerHints()
	for _, h := range []FooterHint{
		{Key: "End", Label: "Follow"}, {Key: "M-g", Label: "Filter"}, {Key: "M-v", Label: "Raw"},
		{Key: "e", Label: "Next issue"}, {Key: "m", Label: "Mark"}, {Key: "u", Label: "Next mark"},
		{Key: "M-a", Label: "Copy shown"}, {Key: "M-A", Label: "Copy orig"},
	} {
		h.Enabled = true
		hints = append(hints, h)
	}
	m := fullscreenModal("/logs", &hintedModel{hints: hints}, 120)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	bar := lines[len(lines)-1]
	for _, want := range []string{"10Close", "EndFollow", "M-gFilter", "M-vRaw", "eNext issue"} {
		if !strings.Contains(bar, want) {
			t.Errorf("key bar %q lacks %q", bar, want)
		}
	}
	if strings.Contains(bar, "Copy") {
		t.Errorf("keys that do not fit should be left out whole, got %q", bar)
	}
	if key, _ := m.keyAtFooterColumn(119); key == "M-A" {
		t.Errorf("a key left out must not be clickable")
	}
}
