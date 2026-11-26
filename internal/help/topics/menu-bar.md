# Menu bar

The menu bar on the top row, as in mc, holds every panel action in five
menus: Left, File, Command, Options and Right. Each entry shows its key
on the right, so the menus also work as a key reference when you forget
one.

## How to read it

- Left and Right: the mode of that panel (List, Describe, Manifest,
  Logs, Info, Local files; the current one is checked), Pin target,
  Freeze display, Panel options, Columns normal/wide, the sort, Group by
  namespace, Filter and Faults only, Namespace..., Context...,
  Browse all types... (see [Resource types](resource-types.md)) and
  Resource visibility... (see
  [Resource visibility](resource-visibility.md)).
- File: View, Edit, Copy, Create namespace, Delete, Port forward,
  Owners, Used by, Requires, Explain, the marks (Toggle mark, Select
  group, Unselect group, Invert selection, Select all), Quick search,
  Exit.
- Command: the User menu (`F2`), Context for both panels..., Swap
  panels, Switch panels on/off,
  Copy command, the compare commands, Investigation window, Hotlist,
  History back, forward and list, Help.
- Options: Panel width, Left and Right panel options, Menu bar visible.

Entries that do not apply to the current selection are gray. The left
and right menus act on their own panel; File acts on the active one.

## Keys

- `F9` (or `Esc 9`, or a click on `9 PullDn`) opens the bar.
- A click on a title opens that menu at any time.
- `Left`, `Right` (or `Tab`, `Shift-Tab`) move between menus.
- `Up`, `Down` move between entries; `Home`, `End` go to the first or
  last one.
- `Enter` or `Space` runs the entry.
- The highlighted letter of a title opens that menu; the highlighted
  letter of an entry runs it. `M-<letter>` always picks a title.
- `Esc`, `F9` or `F10` closes the bar, as does a click outside it.
- The mouse wheel over an open menu moves the highlight. It skips
  separators and gray entries and never runs one.

## Limits

- Options > Menu bar visible, or `layout.menuBarVisible: false`, hides
  the bar and gives its row to the panels. `F9` then draws the bar over
  the top row only while it is open. The entry saves the setting to
  `~/.kc/config.yaml`.
- The full-screen terminal (`C-o`) and full-screen viewers cover the
  bar. In the full-screen terminal `F9` goes to the shell.
- The colors follow mc's default skin: black on cyan while closed,
  white with yellow hotkeys while open.

## See also

- [Panels](panels.md)
- [Panel mode](panel-mode.md)
- [Panel options](panel-options.md)
- [Custom commands](custom-commands.md)
- [Help](help.md)
