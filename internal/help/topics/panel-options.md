# Panel options

The "View Options" dialog sets how one panel shows its content: its mode
and width, and for lists the columns, the sort and what rows to show.
`M-F1` opens it for the left panel and `M-F2` for the right one, as mc's
listing format dialog. Only the sections that apply to what the panel
shows appear.

## How to read it

Panel section, always first:

- Panel mode: List, Describe, Manifest, Logs, Info, and Command once a
  command ran there. See [Panel mode](panel-mode.md).
- Panel width: 25%, 33%, 50%, 66%, 75% or 100% of the screen.

Resources section, on the root and other lists of resource types:

- Include empty: Yes also lists types that have no objects. No is the
  "non-empty only" setting (`resources.showNonEmptyOnly`). A hidden type
  still opens when you name it: `cd jobs`, a hotlist entry, a history
  entry or a namespace change that keeps the open list.
- Order: Alphabetic, Group (by API group) or Favorites.
- Table mode: Scroll pans across all columns with `Left` and `Right`;
  Fit squeezes the columns into the panel width.

Objects section, on object lists in List mode:

- Table mode: as above.
- Columns: Normal or Wide, the extra columns of `kubectl get -o wide`.
- Sort by: one of the shown columns. Reverse: Yes sorts descending.
- Group by namespace: for lists across namespaces, keeps rows grouped
  by namespace with the sort inside each group.
- Faults only: Yes keeps only rows in a fault state, like `C-z`.

Command section, in Command mode: Watch, the interval at which the
command runs again (Off, 1s to 10s, then 20s to 1m).

In Logs mode the dialog adds the viewer and log options, see
[Viewer options](viewer-options.md).

## Keys

- `Up`, `Down` move between rows.
- `Left`, `Right` or `Space` change the value of the row.
- `Enter` applies to this panel for this session and closes.
- `C-s` saves the values as defaults in `~/.kc/config.yaml`.
- `Esc` closes without changes.

## Limits

- Faults only belongs to the folder, like the row filter; `C-s` does
  not save it.
- `C-s` saves Table mode, Columns, the sort, Group by namespace,
  Include empty, Order and the panel widths as the defaults kc starts
  with.
- `M-o`, `M-r` and `C-w` change the sort and columns without the dialog.
- The panel mode is not saved; use `layout.leftPanelMode` and
  `layout.rightPanelMode` for the start mode.

## See also

- [Panels](panels.md)
- [Row filter](row-filter.md)
- [Panel mode](panel-mode.md)
- [Hotlist and saved views](hotlist.md)
- [Menu bar](menu-bar.md)
