package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	uistyles "github.com/sttts/kc/internal/ui/styles"
)

type functionBarState struct {
	Width                int
	ToastActive          bool
	ToastText            string
	ToastInfo            bool
	ShowTerminal         bool
	ActivePanel          int
	PanelHasCommandFocus bool
	Capabilities         PanelCapabilities
	PanelMode            PanelViewMode
	PanelInteractive     bool
	TerminalHasInput     bool
}

type functionBar struct {
	cache cachedView
	key   string
}

func newFunctionBar() *functionBar { return &functionBar{} }

func (f *functionBar) Render(state functionBarState) string {
	sig := f.signature(state)
	if view, _, ok := f.cache.get(); ok && f.key == sig {
		return view
	}

	if state.ToastActive {
		f.key = sig
		f.cache.set(renderToast(state.ToastText, state.ToastInfo, state.Width), nil)
		view, _, _ := f.cache.get()
		return view
	}

	if !state.ShowTerminal && !state.PanelHasCommandFocus {
		f.key = sig
		f.cache.set(renderFunctionKeySlots(panelFunctionKeys(state), state.Width), nil)
		view, _, _ := f.cache.get()
		return view
	}

	f.key = sig
	f.cache.set(renderFunctionKeySlots(compactFunctionKeys(state), state.Width), nil)
	view, _, _ := f.cache.get()
	return view
}

// compactFunctionKeys lists the keys shown while the terminal is fullscreen or
// a panel command has keyboard focus.
func compactFunctionKeys(state functionBarState) []functionKey {
	if state.ShowTerminal {
		return []functionKey{{Key: " C-o", Label: "Return to panels", Enabled: true}}
	}
	return []functionKey{
		{Key: " Esc Esc", Label: "Drop focus", Enabled: true},
		{Key: "10", Label: "Quit", Enabled: true},
		{Key: " C-o", Label: "Fullscreen", Enabled: true},
	}
}

// functionKeyID identifies a slot of the panel function key bar so the click
// handler can map a slot back to its action.
type functionKeyID int

const (
	functionKeyHelp functionKeyID = iota
	functionKeyView
	functionKeyEdit
	functionKeyCopy
	functionKeyRenameMove
	functionKeyNamespace
	functionKeyDelete
	functionKeyCommands
	functionKeyPullDown
	functionKeyQuit
	functionKeyFocus
)

type functionKey struct {
	ID      functionKeyID
	Key     string
	Label   string
	Enabled bool
}

// panelFunctionKeys lists the keys shown in the panel function bar, in order.
// Like Midnight Commander, F-keys are shown by their number only, right-aligned
// in two columns (" 1" .. "10").
func panelFunctionKeys(state functionBarState) []functionKey {
	caps := state.Capabilities
	keys := []functionKey{
		{functionKeyHelp, " 1", "Help", caps.HasHelp},
		{functionKeyCommands, " 2", "Menu", caps.HasContextMenu},
		{functionKeyView, " 3", "View", caps.CanView},
		{functionKeyEdit, " 4", "Edit", caps.CanEdit},
		{functionKeyCopy, " 5", "Copy", caps.CanCopy},
		{functionKeyRenameMove, " 6", "", false},
		{functionKeyNamespace, " 7", "Namespace", caps.CanCreateNS},
		{functionKeyDelete, " 8", "Delete", caps.CanDelete},
		{functionKeyPullDown, " 9", "PullDn", true},
		{functionKeyQuit, "10", "Quit", true},
	}
	if state.PanelMode == PanelModeLogs {
		// F2 opens the log options in Logs mode (Panel.modeAction).
		keys[1] = functionKey{functionKeyCommands, " 2", "Options", caps.HasOptions}
	}
	if state.PanelMode == PanelModeCommand && state.PanelInteractive {
		keys = append(keys, functionKey{functionKeyFocus, "Enter", "Focus", !state.TerminalHasInput})
	}
	return keys
}

// functionKeySlotStarts splits width into n slots of (almost) equal size, the
// way Midnight Commander spreads its button bar across the screen. The result
// holds n+1 offsets; slot i covers [starts[i], starts[i+1]).
func functionKeySlotStarts(n, width int) []int {
	starts := make([]int, n+1)
	if n == 0 {
		return starts
	}
	for i := 0; i <= n; i++ {
		starts[i] = width * i / n
	}
	return starts
}

// functionKeyAt returns the index of the slot under column x, or -1.
func functionKeyAt(n, width, x int) int {
	starts := functionKeySlotStarts(n, width)
	for i := 0; i < n; i++ {
		if x >= starts[i] && x < starts[i+1] {
			return i
		}
	}
	return -1
}

func renderFunctionKeySlots(keys []functionKey, width int) string {
	if width <= 0 || len(keys) == 0 {
		return ""
	}
	return renderFunctionKeySlotsAt(keys, functionKeySlotStarts(len(keys), width))
}

// renderFunctionKeySlotsAt renders keys into the slots given by starts (n+1
// offsets): the key on black, the label filling the rest of the slot.
func renderFunctionKeySlotsAt(keys []functionKey, starts []int) string {
	var b strings.Builder
	for i, k := range keys {
		if i+1 >= len(starts) {
			break
		}
		slot := starts[i+1] - starts[i]
		key := truncateStringToWidth(k.Key, slot)
		b.WriteString(uistyles.FunctionBarKeyStyle.Render(key))
		labelWidth := slot - lipgloss.Width(key)
		if labelWidth <= 0 {
			continue
		}
		desc := uistyles.FunctionBarLabelStyle
		if !k.Enabled && strings.TrimSpace(k.Label) != "" {
			desc = uistyles.FunctionBarDisabledStyle
		}
		label := truncateStringToWidth(k.Label, labelWidth)
		b.WriteString(desc.Width(labelWidth).Render(label))
	}
	return b.String()
}

func (f *functionBar) signature(state functionBarState) string {
	c := state.Capabilities
	return fmt.Sprintf("w=%d toast=%t tt=%s term=%t panel=%d cmd=%t mode=%d interactive=%t termInput=%t caps=%t-%t-%t-%t-%t-%t-%t-%t",
		state.Width,
		state.ToastActive,
		state.ToastText+fmt.Sprint(state.ToastInfo),
		state.ShowTerminal,
		state.ActivePanel,
		state.PanelHasCommandFocus,
		state.PanelMode,
		state.PanelInteractive,
		state.TerminalHasInput,
		c.CanView, c.CanCopy, c.CanEdit, c.CanDelete, c.CanCreateNS, c.HasOptions, c.HasContextMenu, c.HasHelp)
}

func (f *functionBar) Invalidate() {
	f.key = ""
	f.cache.invalidate()
}
