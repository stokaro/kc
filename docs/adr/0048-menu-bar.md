# 0048. mc-style pull-down menu bar

- Status: Accepted
- Date: 2026-11-22
- Issue: #48

## Context

Midnight Commander opens a menu bar with F9: Left, File, Command, Options, Right. Each entry shows its key, so the menus double as the key reference. kc had no such place: F9 opened the custom commands menu, panel options were on F2 and M-F1/M-F2, and several keys (C-w, C-r, `+`, `-`, `*`) were not listed anywhere.

The issue left these questions open: which key opens the bar and where today's F9 menu goes, what the menus contain, how they look, how the mouse works, and whether the custom commands menu keeps a direct key. Open pull requests add more actions (object actions in the F9 menu, sort keys, history, hotlist, row filter, port-forward), so adding an entry must be cheap.

## Decision

### 1. Keys: F9 opens the menu bar, F2 is the user menu

F9 (and `Esc 9`, and a click on the `9 PullDn` slot of the function key bar) opens the menu bar, as in mc. The custom commands menu that was on F9 moves to F2, which is mc's user menu key; it is also Command > User menu. The function key bar reads `2 Menu` and `9 PullDn`, as in mc.

F2 no longer opens panel options. They stay on M-F1 (left panel) and M-F2 (right panel), and in the Left/Right and Options menus. Inside viewers F2 still opens the viewer theme dialog; viewers handle their own keys.

No new global key is bound. The hotkey letters work only while the bar is open.

### 2. Behavior

F9 shows the bar with the title used last time highlighted (the first title initially) and no menu dropped down, like mc with its default `drop_menus=false`. A title hotkey, Enter or Down drops the menu down. In a dropped menu, `Up`/`Down`/`Home`/`End` move between entries, skipping separators and dim entries; `Left`/`Right` (and `Tab`) move to the neighboring menu; `Enter` runs the entry; an entry's hotkey letter runs it; a letter that matches no entry, or `M-<letter>`, switches to the menu with that title hotkey. `Esc`, `F9`, `F10`, `C-c` and `C-g` close the bar. `Esc` closes at once; the Esc-digit timeout does not apply while the bar is open.

The bar is drawn over the top row only while it is open, like mc with "menubar visible" off. A permanent bar would cost a panel row and shift every mouse hit test and dialog position by one row.

### 3. Mouse

While the bar is open it takes every mouse event. A click on a title drops that menu down (a click on the open title closes it). Pressing on an entry highlights it; releasing over the same entry runs it. A click on the frame or a separator keeps the menu; a click anywhere else closes the bar and does not reach the panels. With the bar closed, the `9 PullDn` slot opens it.

### 4. Contents

Every entry runs an existing action; nothing is shown that does not work.

- Left / Right (that panel): List, Describe, Manifest (current one marked `*`), Panel options (M-F1/M-F2), Columns normal/wide (C-w), Reread (C-r), Namespace..., Context....
- File (active panel): View F3, Edit F4, Copy F5, Create namespace F7, Delete F8, Toggle mark C-t, Select group `+`, Unselect group `-`, Invert selection `*`, Select all C-a, Quick search C-s, Exit F10.
- Command: User menu F2, Swap panels C-u, Switch panels on/off C-o, Copy command M-c, Help F1.
- Options: Panel width M-w, Left panel options M-F1, Right panel options M-F2.

Entries that use a list key (C-w, C-r, C-t, `+`, `-`, `*`, C-a) deliver that key to the panel, so the entry does exactly what the key does. File and Command entries use the same capability checks as the function key bar; entries that do not apply are dim and cannot be chosen.

Left out because kc has no such action yet: a configuration dialog, terminal mode settings, the viewer theme outside a viewer (it is F2 inside viewers), the row filter (#30), object actions (#33).

### 5. Look

Colors follow the reference mc screenshot: a cyan bar and cyan drop-downs with black text, yellow hotkey letters, a black highlight bar, dim grey for entries that cannot be chosen. Drop-downs have a single-line frame and the same shadow as kc dialogs. The issue suggested grey dialog-style drop-downs; the mc look was chosen since kc imitates mc. Shortcuts use mc notation (`F3`, `C-u`, `M-c`) and are right-aligned in one column.

### 6. Structure

`internal/ui/menubar` holds the bar as data plus a state machine: `Menu{Title, Items}` and `Item{Label, Shortcut, Checked, Disabled, Run}`, with `&` marking the hotkey letter as in mc. It knows nothing about kc and is tested black-box. `internal/ui/menu_bar.go` builds the menus from a table, one line per entry, and is rebuilt on every open so enabled states and marks are current.

## Alternatives considered

- Keep F2 as panel options and reach the custom commands only from the menu bar. Less churn for F2 users, but it drops the direct key for custom commands that the issue asks about, and differs from mc.
- Move the custom commands to another free key. Any letter key conflicts with keys taken by open pull requests; F2 is what mc uses.
- A permanent menu bar row. More discoverable, but it shifts the whole layout and every mouse offset.
- Drop the first menu down on F9. Fewer keys to reach the first menu, but title hotkeys would then clash with entry hotkeys (for example `c` in Left is Columns), so `F9 c s` could not reach Command > Swap panels.
- Grey, dialog-style drop-downs as written in the issue. Rejected in favor of the mc colors the user showed.

## Consequences

- F2 users who opened panel options must use M-F1/M-F2 or the menus. The README says so.
- New actions get a menu entry by adding one line to the matching items function in `internal/ui/menu_bar.go`. Entries to add when the open pull requests land: Object actions (#80, Command menu), sort column M-o and reverse M-r (#82, Left/Right), history back M-y, forward M-u, list M-H and hotlist C-\ (#90, Command), row filter M-f and faults only C-z (#93, Left/Right or File), port-forward M-p (#91, File).
- The menu bar has no mouse access while closed other than the `9 PullDn` slot.
