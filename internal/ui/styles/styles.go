package styles

import "charm.land/lipgloss/v2"

// Color constants (ANSI base codes)
// Note: Prefer lipgloss built-in ANSI constants where possible in code.
const (
	ColorBlack            = "0"  // prefer lipgloss.Black
	ColorDarkerBlue       = "4"  // prefer lipgloss.Blue
	ColorCyan             = "6"  // prefer lipgloss.Cyan
	ColorGrey             = "7"  // prefer lipgloss.White (base white)
	ColorDarkGrey         = "8"  // bright black (grey)
	ColorWhite            = "15" // bright white
	ColorOrange           = "208"
	ColorModalBg          = ColorGrey // mc dialogs: black on light grey
	ColorModalTitle       = ColorDarkerBlue
	ColorDangerBg         = "1"  // red dialogs for changes to the cluster
	ColorDangerFg         = "15" // bright white
	ColorModalFg          = ColorBlack
	ColorModalSelBg       = ColorCyan
	ColorModalButtonBg    = ColorDarkGrey
	ColorModalButtonFg    = ColorModalFg
	ColorModalButtonSelBg = ColorCyan
	ColorModalCursor      = ColorOrange
)

// Common styles
var (
	// Panel styles
	PanelHeaderStyle = lipgloss.NewStyle().
				Background(lipgloss.Blue).
				Foreground(lipgloss.White)

	PanelContentStyle = lipgloss.NewStyle().
				Background(lipgloss.Blue).
				Foreground(lipgloss.White)

	PanelFooterStyle = lipgloss.NewStyle().
				Background(lipgloss.Blue).
				Foreground(lipgloss.White)

	PanelItemStyle = lipgloss.NewStyle().
			Background(lipgloss.Blue).
			Foreground(lipgloss.White)

	// Current row highlight (cursor): cyan background, black text
	PanelItemSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Cyan).
				Foreground(lipgloss.Black)

		// Table header style for server-side Table rendering in panel content
	PanelTableHeaderStyle = lipgloss.NewStyle().
				Background(lipgloss.Blue).
				Foreground(lipgloss.Yellow).
				Bold(true).
				Align(lipgloss.Center)

		// Border and frame styles
	BorderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Cyan)

		// Panel frame styles (use lipgloss borders)
	PanelFrameLeftStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Cyan).
				BorderRight(false)

	PanelFrameRightStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Cyan).
				BorderLeft(false)

		// Function bar slots (Midnight Commander layout): key number on
		// black, label filling the rest of the slot.
	FunctionBarKeyStyle = lipgloss.NewStyle().
				Background(lipgloss.Black).
				Foreground(lipgloss.White)

	FunctionBarLabelStyle = lipgloss.NewStyle().
				Background(lipgloss.Cyan).
				Foreground(lipgloss.Black)

	FunctionBarDisabledStyle = lipgloss.NewStyle().
					Background(lipgloss.Color(ColorDarkGrey)).
					Foreground(lipgloss.White)

	FunctionKeyBarStyle = lipgloss.NewStyle().
				Background(lipgloss.Black).
				Foreground(lipgloss.White)

		// Toggle message styles
	ToggleMessageStyle = lipgloss.NewStyle().
				Background(lipgloss.Blue).
				Foreground(lipgloss.White).
				Padding(0, 1)

	ToggleMessageTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Blue).
				Foreground(lipgloss.White).
				Bold(true)

		// Modal styles
	ModalBorderStyle = lipgloss.NewStyle().
				BorderForeground(lipgloss.Cyan)

	ModalTitleStyle = lipgloss.NewStyle().
			Background(lipgloss.Blue).
			Foreground(lipgloss.White)

		// Resource selector styles
	ResourceSelectorHeaderStyle = lipgloss.NewStyle().
					Background(lipgloss.Blue).
					Foreground(lipgloss.White)

	ResourceSelectorFooterStyle = lipgloss.NewStyle().
					Background(lipgloss.Blue).
					Foreground(lipgloss.White)
)
