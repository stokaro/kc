---
name: kc-tui
description: Change or review kc's Bubble Tea v2 panels, widgets, event routing, terminal integration, layout, or asynchronous UI updates. Use for focus, stale results, selection, resize, rendering, and stream lifecycle bugs; skip backend-only Go changes and prose edits.
---

# TUI changes in kc

Paths below are relative to the repository root. Inspect the affected component
and one matching test; read only the reference needed for that behavior.
Keep kc's MC/FAR navigation and keymap. A generic TUI design guide does not
authorize changing the product's interaction model.

## Event loop and lifecycle

- Use the `charm.land/*/v2` APIs selected in `go.mod`: `View() tea.View`,
  `tea.KeyPressMsg`, and declarative terminal fields. `tea.KeyMsg` is still a
  valid interface for handling both press and release events.
- Keep `Update`, `View`, and UI callbacks free of blocking I/O. Trace helpers
  too: discovery and a RESTMapper lookup can issue network requests. Capture
  the target on the UI goroutine, perform I/O in a `tea.Cmd`, and apply its
  result through a message. Never mutate the model from a command goroutine.
- Reuse [the widget contract](../../../internal/ui/panelcontent/widget.go) and
  [reload.Loader](../../../internal/ui/panelcontent/reload/loader.go). Give
  each asynchronous result an owner and generation/connection identity;
  reject stale results and release their resources.
- Follow [log stream lifecycle](../../../internal/ui/panelcontent/logs/widget.go)
  for cancellation, late opens/reads, bounded buffering, and teardown.
  Do not create a new polling loop for behavior the shared loader handles.
- Resolve actions from the selected row's origin, not whichever panel is
  active when a command returns. Keep `Selection.Source`, origin stamping,
  and effective target connection consistent; use `kc-kubernetes` for changes
  crossing that boundary.

## Input and rendering

- Route keys to their current owner: modal, text input, shell line, terminal,
  or panel. Reserve only intentional global shortcuts. Background results,
  resize, and lifecycle messages must still reach their owners behind overlays
  or while another component has focus.
- Preserve selection and marks by stable row ID across refresh, sorting, and
  filtering. Re-resolve the row before an action; a retained array index is
  not the target's identity.
- Measure rendered text in terminal cells with the existing ANSI-aware
  helpers. Account for borders, padding, tiny sizes, and resize. Preserve the
  table's documented ASCII-cell contract unless widening it is the task.
- Keep terminal restoration and subprocess handoff with the existing owner.
  Do not print diagnostics into the rendered screen. Preserve text fallbacks
  when optional graphics or terminal capabilities are unavailable.

## Focused verification

1. Exercise state transitions with constructed messages and fake I/O. Cover
   a late response after selection/context changes, teardown, and messages
   arriving while an overlay or terminal owns focus when those paths change.
2. For layout changes, render at fixed normal, narrow, and tiny sizes. Check
   cell widths, frame height, visible controls, and resize; inspect a snapshot
   for changes to visual appearance. Fix time/color inputs for determinism.
3. `make test-tui` runs the UI/table/headless-driver unit and render suites.
   For terminal integration changes, `make test-headless` exercises the built
   binaries through startup, resize, help, and exit against envtest. Add a
   focused scenario when those checks do not cover the changed contract.
   Never run kc/headless against the user's cluster configuration.

Start with `make test-unit PACKAGES='./internal/ui/panelcontent/logs'` for a
logs-widget change, substituting the actual affected package; use
`./internal/ui` for app routing. Existing examples:
[stale loader tests](../../../internal/ui/panelcontent/reload/loader_test.go),
[stream tests](../../../internal/ui/panelcontent/logs/widget_test.go),
[whole-frame assertions](../../../internal/ui/dialog_render_test.go), and
[terminal input tests](../../../internal/ui/terminal_input_test.go).
