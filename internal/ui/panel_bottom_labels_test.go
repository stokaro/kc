package ui

// White-box like the rest of package ui: a Panel's folder and scope are set
// through unexported state.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestBreadcrumbTitleKeepsItsStart checks the top border title of a narrow
// panel: a title that does not fit as segments is cut at the end with "…"
// instead of collapsing to "...".
func TestBreadcrumbTitleKeepsItsStart(t *testing.T) {
	t.Parallel()
	style := lipgloss.NewStyle().Padding(0, 1)
	p := &Panel{}
	tests := []struct {
		title string
		width int
		want  string
	}{
		{title: "Info: kind-kc-demo: shop", width: 20, want: "Info: kind-kc-dem…"},
		{title: "Info: kind-kc-demo: shop", width: 40, want: "Info: kind-kc-demo: shop"},
		{title: "/pods/web-6989b69646-dgqgj", width: 15, want: ".../web-6989…"},
		{title: "/deployments/web/rollout", width: 22, want: ".../web/rollout"},
	}
	for _, tc := range tests {
		if got := p.ellipsizeBreadcrumbTitle(style, tc.title, tc.width); got != tc.want {
			t.Errorf("ellipsizeBreadcrumbTitle(%q, %d) = %q, want %q", tc.title, tc.width, got, tc.want)
		}
	}
}

// TestBottomBorderKeepsLabelsWhole checks how the bottom border shares its
// width: the context, the namespace and the filter stay whole while the
// status on the right ("2/2 • 100% • List") can give up width; the status
// is cut with "…" and keeps a few columns, and the line is as wide as the
// panel.
func TestBottomBorderKeepsLabelsWhole(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		filter string
		want   []string
		not    []string
	}{
		{
			// A half panel of a 150-column screen.
			name: "filter at 75 columns", width: 75, filter: "status!=Running",
			want: []string{"kind-kc-demo", " shop ", "Filter: status!=Running", "List"},
		},
		{
			// A half panel of a 100-column screen: the status gives up width
			// first, then the filter is cut with "…".
			name: "filter at 50 columns", width: 50, filter: "status!=Running",
			want: []string{"kind-kc-demo", " shop ", "Filter: st", "…"},
		},
		{
			// A quarter panel of a 150-column screen.
			name: "no filter at 37 columns", width: 37,
			want: []string{"kind-kc-demo", " shop "},
			not:  []string{"Filter"},
		},
		{
			// Too narrow for the scope and a useful status: the scope wins.
			name: "scope whole at 28 columns", width: 28,
			want: []string{"kind-kc-demo : shop"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			p := NewPanel("t")
			p.Update(tea.WindowSizeMsg{Width: tc.width, Height: 12})
			p.UseFolder(ctx, true)
			p.SetFolder(ctx, newFilterFolder("api", "web"), false)
			p.SetScope(panelScopeLabel{Context: "kind-kc-demo", Namespace: "shop"})
			if tc.filter != "" {
				p.SetRowFilter(ctx, mustFilter(t, tc.filter))
			}
			lines := strings.Split(ansi.Strip(p.Render(ctx, tc.width, 12, true)), "\n")
			line := lines[len(lines)-1]
			if w := ansi.StringWidth(line); w != tc.width {
				t.Errorf("bottom border is %d columns wide, want %d: %q", w, tc.width, line)
			}
			for _, w := range tc.want {
				if !strings.Contains(line, w) {
					t.Errorf("bottom border %q misses %q", line, w)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(line, w) {
					t.Errorf("bottom border %q shows %q", line, w)
				}
			}
		})
	}
}

func TestLabelRoom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		room   int
		status string
		want   int
	}{
		{name: "short status", room: 60, status: "1/2", want: 55},
		{name: "long status keeps minStatusRoom", room: 60, status: "Down selects a pod • Info", want: 60 - minStatusRoom},
		{name: "no status", room: 60, want: 60},
		{name: "never less than half", room: 16, status: "Down selects a pod • Info", want: 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := labelRoom(tc.room, tc.status); got != tc.want {
				t.Errorf("labelRoom(%d, %q) = %d, want %d", tc.room, tc.status, got, tc.want)
			}
		})
	}
}
