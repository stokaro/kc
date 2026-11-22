package ui

import tea "charm.land/bubbletea/v2"

// wheelStep returns -1 for a wheel-up event, 1 for wheel-down and 0 for any
// other message, horizontal wheels included. List dialogs move their
// selection by this step, as mc's list boxes do; the wheel never runs the
// selected entry.
//
// The pointer position is not checked here: a windowed Modal only passes
// wheel events that are over its window (see Modal.wheelOutsideWindow).
func wheelStep(msg tea.Msg) int {
	wheel, ok := msg.(tea.MouseWheelMsg)
	if !ok {
		return 0
	}
	switch wheel.Button {
	case tea.MouseWheelUp:
		return -1
	case tea.MouseWheelDown:
		return 1
	default:
		return 0
	}
}
