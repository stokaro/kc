package dashboard

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// The dashboard draws in the panels' palette: text on the panel
// background, frames in the frame color, the focused widget's title like
// a panel's cursor row. Only a chart's plot is black.
var (
	// gapBg shades plot columns and lane cells kc has no data for.
	gapBg     = lipgloss.Color("236")
	plotBg    = lipgloss.Black
	gapStyle  = lipgloss.NewStyle().Background(gapBg).Foreground(lipgloss.Color("248"))
	plotStyle = lipgloss.NewStyle().Background(plotBg)
)

// pal is the palette of the panels, read at each frame: a skin applies it
// at startup.
func pal() uistyles.Palette { return uistyles.Current() }

// baseStyle is plain text on the panel background.
func baseStyle() lipgloss.Style {
	p := pal()
	return lipgloss.NewStyle().Background(p.Background).Foreground(p.Foreground)
}

// on returns s on the panel background.
func on(s lipgloss.Style) lipgloss.Style { return s.Background(pal().Background) }

var (
	headStyle = func() lipgloss.Style { return on(lipgloss.NewStyle().Foreground(pal().Header).Bold(true)) }
	noteStyle = func() lipgloss.Style { return on(lipgloss.NewStyle().Foreground(lipgloss.Cyan)) }
	valStyle  = func() lipgloss.Style { return on(lipgloss.NewStyle().Foreground(lipgloss.BrightWhite).Bold(true)) }
	overStyle = func() lipgloss.Style { return on(lipgloss.NewStyle().Foreground(lipgloss.BrightYellow).Bold(true)) }
	badStyle  = func() lipgloss.Style { return on(lipgloss.NewStyle().Foreground(lipgloss.BrightRed).Bold(true)) }
	curStyle  = func() lipgloss.Style {
		p := pal()
		return lipgloss.NewStyle().Background(p.CursorBackground).Foreground(p.CursorForeground)
	}
)

// sgrPrefix is the escape sequence that starts text in st.
func sgrPrefix(st lipgloss.Style) string {
	s := st.Render("x")
	before, _, ok := strings.CutLast(s, "x")
	if !ok {
		return ""
	}
	return before
}

// onPanel makes every unstyled run of line, including the padding up to
// w cells, use the panel background: after each reset the base style
// starts again. Runs with a background of their own keep it.
func onPanel(line string, w int) string {
	base := baseStyle()
	pre := sgrPrefix(base)
	line = fit(line, w)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+pre)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+pre)
	return pre + line + "\x1b[m"
}

// frame draws a box of w x h cells around body. The title sits in the
// top border, the focused one like a panel's cursor row; footer sits in
// the bottom border at the right, like a panel's status. Body lines are
// cut or padded to the inside.
func frame(title, footer string, focused bool, w, h int, body []string) []string {
	if w < 4 || h < 2 {
		return nil
	}
	p := pal()
	border := lipgloss.NewStyle().Background(p.Background).Foreground(p.Frame)
	tst := lipgloss.NewStyle().Background(p.Background).Foreground(p.Foreground)
	if focused {
		tst = curStyle()
	}
	inner := w - 2
	top := border.Render("┌")
	if title != "" {
		t := ansi.Truncate(" "+title+" ", max(0, inner-2), "…")
		top += border.Render("─") + tst.Render(t)
		top += border.Render(strings.Repeat("─", max(0, inner-1-ansi.StringWidth(t))) + "┐")
	} else {
		top += border.Render(strings.Repeat("─", inner) + "┐")
	}
	out := make([]string, 0, h)
	out = append(out, top)
	for i := range h - 2 {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		out = append(out, border.Render("│")+onPanel(line, inner)+border.Render("│"))
	}
	bottom := strings.Repeat("─", inner)
	if footer != "" {
		f := ansi.Truncate(" "+footer+" ", max(0, inner-2), "…")
		fw := ansi.StringWidth(f)
		out = append(out, border.Render("└"+strings.Repeat("─", max(0, inner-fw-1)))+noteStyle().Render(f)+border.Render("─┘"))
	} else {
		out = append(out, border.Render("└"+bottom+"┘"))
	}
	return out
}
