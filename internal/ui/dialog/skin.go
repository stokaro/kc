// Package dialog draws the widgets of kc's dialogs in Midnight Commander's
// dialog skin: cyan input lines, bracketed buttons with the default one
// marked "[< OK >]", check boxes, hotkey letters in blue and separator
// rules that the dialog frame joins with tees. Form lays the widgets out
// and runs the keyboard and mouse handling every dialog shares.
package dialog

import (
	"charm.land/lipgloss/v2"

	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// Skin is the color set of one kind of dialog.
type Skin struct {
	Normal   lipgloss.Style // text, frame and unfocused widgets
	Focus    lipgloss.Style // the focused button or check box
	Hot      lipgloss.Style // hotkey letter of an unfocused widget
	HotFocus lipgloss.Style // hotkey letter of the focused widget
	Input    lipgloss.Style // input line
	Error    lipgloss.Style // a validation message
}

// Normal is mc's dialog skin: black on light gray, focus black on cyan,
// hotkeys blue, input lines black on cyan.
func Normal() Skin {
	gray := lipgloss.Color(uistyles.ColorModalBg)
	cyan := lipgloss.Cyan
	return Skin{
		Normal:   lipgloss.NewStyle().Background(gray).Foreground(lipgloss.Black),
		Focus:    lipgloss.NewStyle().Background(cyan).Foreground(lipgloss.Black),
		Hot:      lipgloss.NewStyle().Background(gray).Foreground(lipgloss.Blue),
		HotFocus: lipgloss.NewStyle().Background(cyan).Foreground(lipgloss.Blue),
		Input:    lipgloss.NewStyle().Background(cyan).Foreground(lipgloss.Black),
		Error:    lipgloss.NewStyle().Background(gray).Foreground(lipgloss.Red),
	}
}

// Danger is mc's error dialog skin, which kc uses for dialogs that change
// the cluster: white on red, focus black on light gray, hotkeys yellow.
func Danger() Skin {
	red := lipgloss.Color(uistyles.ColorDangerBg)
	white := lipgloss.Color(uistyles.ColorDangerFg)
	gray := lipgloss.Color(uistyles.ColorModalBg)
	return Skin{
		Normal:   lipgloss.NewStyle().Background(red).Foreground(white),
		Focus:    lipgloss.NewStyle().Background(gray).Foreground(lipgloss.Black),
		Hot:      lipgloss.NewStyle().Background(red).Foreground(lipgloss.BrightYellow),
		HotFocus: lipgloss.NewStyle().Background(gray).Foreground(lipgloss.Blue),
		Input:    lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black),
		Error:    lipgloss.NewStyle().Background(red).Foreground(lipgloss.BrightYellow).Bold(true),
	}
}
