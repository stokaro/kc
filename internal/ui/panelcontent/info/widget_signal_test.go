package info

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
)

// TestWidgetColorsEachSignal checks that the bar is colored by what it
// shows, each line under it by its own state, and that another line's
// warning is named after the percentage in that line's color, so the text
// alone still tells it apart without color.
func TestWidgetColorsEachSignal(t *testing.T) {
	t.Parallel()
	warn, fail := models.NotReadyStyle(), models.JobFailedStyle()
	if warn.Render("x") == "x" || warn.Render("x") == fail.Render("x") {
		t.Fatal("the row styles render no distinct escape codes; the test would prove nothing")
	}
	limLine := "req 25%, lim 116% (overcommitted)"
	tests := []struct {
		name       string
		bar        models.InfoBar
		wantPlain  string            // the end of the bar line without color
		wantStyled []string          // rendered pieces the bar line holds
		notStyled  []string          // rendered pieces it must not hold
		lines      map[string]string // a line under the bar: its rendering
	}{
		{
			name: "modest usage, overcommitted limits",
			bar: models.InfoBar{
				Label: "Memory", Known: true, Usage: true, Percent: 22, Flag: "ok",
				Detail: "pods 2.3/10.4 Gi allocatable, 22%", More: []string{"node 6.0/14.9 Gi capacity, 40%", limLine},
				Health: models.RowHealthOK, DetailHealth: models.RowHealthOK, MoreHealth: []models.RowHealth{models.RowHealthOK, models.RowHealthNotReady},
				Attention: models.RowHealthNotReady, AttentionText: "! lim",
			},
			wantPlain:  " 22% ! lim",
			wantStyled: []string{warn.Render(" ! lim")},
			notStyled:  []string{warn.Render(" 22%"), warn.Render("█")},
			lines: map[string]string{
				"pods 2.3/10.4 Gi allocatable, 22%": "pods 2.3/10.4 Gi allocatable, 22%",
				limLine:                             warn.Render(limLine),
			},
		},
		{
			name: "full usage",
			bar: models.InfoBar{
				Label: "CPU", Known: true, Usage: true, Percent: 95, Flag: "full",
				Detail: "pods 3.8/4.0 cores allocatable, 95% (full)", More: []string{"node 3.9/4.0 cores capacity, 97% (full)", "req 20%, lim 50%"},
				Health: models.RowHealthFailed, DetailHealth: models.RowHealthFailed, MoreHealth: []models.RowHealth{models.RowHealthFailed, models.RowHealthOK},
				Attention: models.RowHealthFailed,
			},
			wantPlain:  " 95% full",
			wantStyled: []string{fail.Render("  95% full")},
			lines: map[string]string{
				"pods 3.8/4.0 cores allocatable, 95% (full)": fail.Render("pods 3.8/4.0 cores allocatable, 95% (full)"),
				"req 20%, lim 50%":                           "req 20%, lim 50%",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			h := newHarness(t)
			h.summary = models.InfoSummary{UsageAt: h.now, Bars: []models.InfoBar{tc.bar}}
			h.w.SetFocus(ctx, true)
			_ = h.w.Init(ctx)
			var bar string
			byText := make(map[string]string)
			for _, l := range h.w.lines(60, 40) {
				s := ansi.Strip(l)
				if strings.HasPrefix(s, " "+tc.bar.Label) {
					bar = l
				}
				byText[strings.TrimSpace(s)] = strings.TrimLeft(l, " ")
			}
			if !strings.HasSuffix(ansi.Strip(bar), tc.wantPlain) {
				t.Errorf("bar line %q, want it to end in %q", ansi.Strip(bar), tc.wantPlain)
			}
			for _, want := range tc.wantStyled {
				if !strings.Contains(bar, want) {
					t.Errorf("bar line %q lacks %q", bar, want)
				}
			}
			for _, bad := range tc.notStyled {
				if strings.Contains(bar, bad) {
					t.Errorf("bar line %q holds %q", bar, bad)
				}
			}
			for text, want := range tc.lines {
				if got := byText[text]; got != want {
					t.Errorf("line %q renders %q, want %q", text, got, want)
				}
			}
		})
	}
}
