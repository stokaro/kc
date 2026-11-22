# 0168. Mouse wheel in menus and list dialogs

- Status: Accepted
- Date: 2026-11-22
- Issue: #168

## Context

mc moves the selection with the mouse wheel in its menus and list boxes. kc did that only in the scope pickers (context, namespace, hotlist, panel history, workload picker); the menu bar drop-downs, the user menu (`F2`), the node action menu, the option dialogs, the panel mode list, the access view and the resource selector ignored the wheel. The issue left open where the pointer must be, whether the wheel wraps around at the ends like some keys do, and what the wheel does in a menu bar that is open but not dropped down.

## Decision

1. **One step per notch.** Wheel up moves the selection one entry up, wheel down one entry down, through the same move function the `Up` and `Down` keys use, so section headers in the user menu, spacer rows in the view options and separators and disabled entries in the menu bar are skipped. Horizontal wheel events do nothing. `wheelStep` in `internal/ui/mouse_wheel.go` turns a wheel event into -1, 1 or 0 for every dialog.

2. **No wrap.** The wheel stops at the first and last entry, also where `Up` and `Down` wrap (the menu bar drop-downs, the node action menu). Spinning the wheel past the end should not jump to the other end of a list.

3. **Nothing runs.** The wheel only moves the selection. It does not run a menu entry, choose a picker entry or change an option value. In the menu bar it also drops a pending left press, so the release after a wheel turn does not run the item the highlight has left.

4. **Pointer over the dialog.** A windowed modal passes a wheel event to its content only when the pointer is over the window, frame included (`Modal.wheelOutsideWindow`); other wheel events are dropped and do not reach the panels under the dialog. The content gets the event in its own coordinates through the existing `shiftMouseMsg`. Fullscreen modals (viewers, logs) take every wheel event, as before. In the menu bar, the wheel counts only over the drop-down of the current menu, and does nothing while no menu is dropped down; the wheel never drops a menu down.

5. **Scrolling.** Each list keeps its selection visible with the function it already uses for keys, so a list longer than its window scrolls with the wheel.

## Alternatives considered

- Handle the wheel anywhere on screen while a dialog is open, as the scope picker did. mc acts on the widget under the pointer, and a wheel over a panel moving a dialog list is surprising. Checking the position once in `Modal` covers every dialog without per-dialog hit tests.
- Translate the wheel into `Up`/`Down` key presses. That is less code, but it would make the wheel wrap in the menu bar and the node action menu, and the `k`/`j` aliases and Tab handling of some dialogs would make the mapping depend on each dialog's key table.
- Scroll the list without moving the selection, as a viewer does. In mc list boxes the wheel moves the selection; a selection outside the window would also need a separate scroll state in every dialog.

## Consequences

- The scope pickers and the help screen (a windowed modal) now ignore the wheel outside their window, which they did not before.
- Forms with text fields and buttons (drain options, command prompts, transfer and confirm dialogs) are not lists and do not react to the wheel.
- A new list dialog gets the position check from `Modal` for free; it only needs `if step := wheelStep(msg); step != 0 { move(step) }` in its `Update`.
