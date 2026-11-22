package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

func TestFunctionBarEnterHintInteractiveCommand(t *testing.T) {
	fb := newFunctionBar()
	state := functionBarState{
		Width:            120,
		PanelMode:        PanelModeCommand,
		PanelInteractive: true,
		Capabilities:     PanelCapabilities{HasHelp: true, HasOptions: true},
	}

	view := fb.Render(state)

	want := uistyles.FunctionBarKeyStyle.Render("Enter") + uistyles.FunctionBarLabelStyle.Render("Focus")
	if !strings.Contains(view, want) {
		t.Fatalf("expected enter focus hint, got:\n%s", view)
	}
}

func TestFunctionBarEnterHintDisabledWhenTerminalHasInput(t *testing.T) {
	fb := newFunctionBar()
	state := functionBarState{
		Width:            120,
		PanelMode:        PanelModeCommand,
		PanelInteractive: true,
		TerminalHasInput: true,
		Capabilities:     PanelCapabilities{HasHelp: true},
	}

	view := fb.Render(state)

	disabled := uistyles.FunctionBarKeyStyle.Render("Enter") + uistyles.FunctionBarDisabledStyle.Render("Focus")
	if !strings.Contains(view, disabled) {
		t.Fatalf("expected disabled enter hint when terminal has input, got:\n%s", view)
	}
}

func TestFunctionBarCommandFocusedHidesEnter(t *testing.T) {
	fb := newFunctionBar()
	state := functionBarState{
		Width:                120,
		PanelMode:            PanelModeCommand,
		PanelInteractive:     true,
		PanelHasCommandFocus: true,
	}

	view := fb.Render(state)

	if strings.Contains(view, "Enter") {
		t.Fatalf("did not expect enter hint while command focused, got:\n%s", view)
	}
	drop := uistyles.FunctionBarKeyStyle.Render(" Esc Esc") + uistyles.FunctionBarLabelStyle.Render("Drop focus")
	if !strings.Contains(view, drop) {
		t.Fatalf("expected drop focus hint while focused, got:\n%s", view)
	}
}

func TestMaskCapabilitiesByMode(t *testing.T) {
	caps := PanelCapabilities{CanView: true, CanCopy: true, CanEdit: true, CanDelete: true, CanCreateNS: true, HasOptions: true, HasContextMenu: true, HasHelp: true}

	got := maskCapabilitiesForMode(PanelModeDescribe, false, caps)
	if got.CanView || got.CanCopy || got.CanEdit || got.CanDelete || got.CanCreateNS {
		t.Fatalf("describe mode should disable resource actions: %+v", got)
	}

	got = maskCapabilitiesForMode(PanelModeCommand, true, caps)
	if got.HasHelp || got.HasOptions || got.HasContextMenu {
		t.Fatalf("focused command should hide help/options/menu: %+v", got)
	}
	if got.CanView || got.CanEdit || got.CanDelete || got.CanCopy || got.CanCreateNS {
		t.Fatalf("command mode should disable resource actions: %+v", got)
	}

	got = maskCapabilitiesForMode(PanelModeList, false, caps)
	if !got.CanView || !got.CanEdit || !got.HasOptions || !got.HasContextMenu {
		t.Fatalf("list mode should keep capabilities intact: %+v", got)
	}
}

func TestFunctionBarSlotsSpanFullWidth(t *testing.T) {
	for _, width := range []int{40, 97, 120, 203} {
		fb := newFunctionBar()
		view := fb.Render(functionBarState{Width: width, Capabilities: PanelCapabilities{HasHelp: true}})
		if got := lipgloss.Width(view); got != width {
			t.Fatalf("width %d: bar is %d columns wide", width, got)
		}
		plain := ansi.Strip(view)
		if width >= 80 && !strings.HasPrefix(plain, " 1Help") {
			t.Fatalf("width %d: expected bar to start with %q, got %q", width, " 1Help", plain)
		}
		if strings.Contains(plain, "Kubernetes Commander") {
			t.Fatalf("width %d: title should not be part of the bar: %q", width, plain)
		}
	}
}

func TestFunctionKeySlotsMatchRenderedLabels(t *testing.T) {
	const width = 120
	state := functionBarState{Width: width, Capabilities: PanelCapabilities{HasHelp: true}}
	keys := panelFunctionKeys(state)
	plain := []rune(ansi.Strip(renderFunctionKeySlots(keys, width)))
	starts := functionKeySlotStarts(len(keys), width)
	for i, k := range keys {
		if got := functionKeyAt(len(keys), width, starts[i]); got != i {
			t.Fatalf("slot %d (%s): click at its start maps to %d", i, k.Key, got)
		}
		if got := functionKeyAt(len(keys), width, starts[i+1]-1); got != i {
			t.Fatalf("slot %d (%s): click at its end maps to %d", i, k.Key, got)
		}
		if !strings.HasPrefix(string(plain[starts[i]:]), k.Key+k.Label) {
			t.Fatalf("slot %d: expected %q at column %d, got %q", i, k.Key+k.Label, starts[i], string(plain[starts[i]:starts[i+1]]))
		}
	}
	if got := functionKeyAt(len(keys), width, width); got != -1 {
		t.Fatalf("click past the bar should map to no key, got %d", got)
	}
}

func TestFunctionBarNumbersRightAligned(t *testing.T) {
	plain := ansi.Strip(renderFunctionKeySlots(panelFunctionKeys(functionBarState{Width: 200}), 200))
	for _, want := range []string{" 1Help", " 2Menu", " 9PullDn", "10Quit"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q in bar, got %q", want, plain)
		}
	}
}
