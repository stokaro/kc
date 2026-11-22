package ui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// Padding between a dialog's window edge and its frame, as in mc.
const (
	dialogPadX = 1
	dialogPadY = 1
	// dialogShadowW is the width of the shadow on the right.
	dialogShadowW = 2
)

// shadowStyle draws what lies under a dialog's shadow: the text stays
// readable but dim, as in mc.
var shadowStyle = lipgloss.NewStyle().
	Background(lipgloss.Black).
	Foreground(lipgloss.Color(uistyles.ColorDarkGrey))

// padDialog surrounds a framed box of width w with padX columns and padY
// rows of the dialog background.
func padDialog(box string, w, padX, padY int, bg color.Color) string {
	if padX == 0 && padY == 0 {
		return box
	}
	fill := lipgloss.NewStyle().Background(bg)
	side := fill.Render(strings.Repeat(" ", padX))
	blank := fill.Render(strings.Repeat(" ", w+2*padX))
	lines := strings.Split(box, "\n")
	out := make([]string, 0, len(lines)+2*padY)
	for range padY {
		out = append(out, blank)
	}
	for _, l := range lines {
		out = append(out, side+l+side)
	}
	for range padY {
		out = append(out, blank)
	}
	return strings.Join(out, "\n")
}

// castShadow darkens the cells of screen that lie under the shadow of a
// window at (x, y) of size w x h: two columns on the right and one row below,
// shifted by the same amount. The shadowed text comes from base, the screen
// under the dialog.
func castShadow(screen, base string, x, y, w, h int) string {
	lines := strings.Split(screen, "\n")
	baseLines := strings.Split(base, "\n")
	shade := func(row, from, to int) {
		if row < 0 || row >= len(lines) || from >= to {
			return
		}
		width := ansi.StringWidth(lines[row])
		to = min(to, width)
		if from >= to {
			return
		}
		under := ""
		if row < len(baseLines) {
			under = ansi.Strip(ansi.Cut(baseLines[row], from, to))
		}
		if pad := to - from - ansi.StringWidth(under); pad > 0 {
			under += strings.Repeat(" ", pad)
		}
		lines[row] = ansi.Cut(lines[row], 0, from) + shadowStyle.Render(under) + ansi.Cut(lines[row], to, width)
	}
	for row := y + 1; row < y+h; row++ {
		shade(row, x+w, x+w+dialogShadowW)
	}
	shade(y+h, x+dialogShadowW, x+w+dialogShadowW)
	return strings.Join(lines, "\n")
}
