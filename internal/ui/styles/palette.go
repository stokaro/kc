package styles

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Palette holds the colors of the panel area: the two file panels, their
// frames, table headers, the cursor row and the scope label in the bottom
// border. Dialogs, the function key bar and the viewer are not part of it.
type Palette struct {
	Background       color.Color // panel background
	Foreground       color.Color // panel text
	Frame            color.Color // panel frame lines
	Header           color.Color // table header text
	Marked           color.Color // marked (selected with Insert) rows
	CursorForeground color.Color // cursor row text
	CursorBackground color.Color // cursor row background
	TitleForeground  color.Color // path label of the focused panel
	TitleBackground  color.Color
	Context          color.Color // context name in the bottom border
	Namespace        color.Color // namespace name in the bottom border, "outside Work" mark
}

// DefaultPalette is the Midnight Commander look kc has always had.
func DefaultPalette() Palette {
	return Palette{
		Background:       lipgloss.Blue,
		Foreground:       lipgloss.White,
		Frame:            lipgloss.White,
		Header:           lipgloss.Yellow,
		Marked:           lipgloss.Yellow,
		CursorForeground: lipgloss.Black,
		CursorBackground: lipgloss.Cyan,
		TitleForeground:  lipgloss.Black,
		TitleBackground:  lipgloss.White,
		Context:          lipgloss.BrightYellow,
		Namespace:        lipgloss.BrightCyan,
	}
}

var current = DefaultPalette()

// Current returns the palette in use.
func Current() Palette { return current }

// Apply makes p the palette in use and rebuilds the panel styles from it.
// It is not safe for concurrent use: call it once at startup, before the
// first frame is drawn.
func Apply(p Palette) {
	current = p
	PanelHeaderStyle = lipgloss.NewStyle().Background(p.Background).Foreground(p.Foreground)
	PanelContentStyle = lipgloss.NewStyle().Background(p.Background).Foreground(p.Foreground)
	PanelFooterStyle = lipgloss.NewStyle().Background(p.Background).Foreground(p.Foreground)
	PanelItemStyle = lipgloss.NewStyle().Background(p.Background).Foreground(p.Foreground)
	PanelItemSelectedStyle = lipgloss.NewStyle().Background(p.CursorBackground).Foreground(p.CursorForeground)
	PanelTableHeaderStyle = lipgloss.NewStyle().
		Background(p.Background).
		Foreground(p.Header).
		Bold(true).
		Align(lipgloss.Center)
}

// ContrastText returns black or bright white, whichever reads better on bg.
func ContrastText(bg color.Color) color.Color {
	// The 16 ANSI colors look different in every terminal theme; go by how
	// dark they usually are instead of by the xterm values.
	if c, ok := bg.(ansi.BasicColor); ok {
		switch c {
		case lipgloss.Black, lipgloss.Red, lipgloss.Blue, lipgloss.Magenta, lipgloss.BrightBlack, lipgloss.BrightBlue:
			return lipgloss.BrightWhite
		default:
			return lipgloss.Black
		}
	}
	r, g, b, _ := bg.RGBA()
	// Relative luminance with the Rec. 601 weights, on 16-bit channels.
	lum := 299*uint64(r) + 587*uint64(g) + 114*uint64(b)
	if lum > 1000*0xffff/2 {
		return lipgloss.Black
	}
	return lipgloss.BrightWhite
}
