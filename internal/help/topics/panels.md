# Panels and object lists

kc shows two panels side by side, as Midnight Commander does. Each panel
browses the cluster like a file system: folders are lists, rows are
objects. One panel is active; most keys act on it. The other panel often
shows details of the row under your cursor, see
[Panel mode](panel-mode.md).

## How it works

A panel in List mode starts at its root and goes deeper with `Enter`:

- the root lists `/overview`, `/helm`, the resource types of the panel's
  namespace scope, then namespaces and the cluster-scoped types;
- a resource type lists its objects, read live from the cluster;
- an object lists its sub-resources: a pod's containers, a Deployment's
  ReplicaSets, `/owners`, `/used-by`, `/requires`, `/diagnosis`,
  `/timeline`, a container's `logs` and `/root`, and so on.

The first row of a folder is `/..`; `Enter` on it goes back up. The frame
title shows the path, the bottom border the context and namespace scope
(`prod : shop`). A list across all namespaces gets a Namespace column.
Rows kc considers faults are red: a crash looping pod, a Deployment with
missing replicas, a node that is not Ready.

Above the shell line, the command line shows the kubectl command that
lists the active panel, for example `kubectl get pods -n shop`. `M-c` or
the copy button at its right copies it.

## Keys

- `Enter` enters the row; on `/..` it goes up. On a row that cannot be
  entered, such as a `logs` row, a key of a ConfigMap or a file, it does
  what `F3` does.
- `Up`, `Down`, `PgUp`, `PgDn`, `Home`, `End` move the cursor.
- `Tab` switches to the other panel.
- `F1` help, `F2` user menu, `F3` view, `F4` edit, `F5` copy,
  `F7` create namespace (in `/namespaces`), `F8` delete,
  `F9` menu bar, `F10` quit. `Esc` then a digit works as the F-key.
- `C-s` (or `M-s`) starts quick search: type to jump to the first row
  whose name starts with the text. `C-s` again finds the next match,
  `Backspace` removes a character, `Esc` ends it, and any other key ends
  it and does its usual job.
- `M-o` sorts by the next column, `M-r` reverses the sort. The sorted
  column title ends in an up or down arrow.
- `M-f` filters rows; `C-z` keeps only rows in a fault state.
- `C-w` switches the columns between normal and wide.
- `C-r` reads the list again ("Reread" in the Left and Right menus).
- `Ins` or `C-t` marks the row, `*` inverts the marks, `C-a` marks all.
  `+` (Select group) and `-` (Unselect group) ask for a shell pattern and
  mark or unmark the rows whose name matches it: `*` any text, `?` one
  character, `[a-z]` a set. The pattern matches the whole name and
  ignores case, so `web-*` takes `web-0` and `web-1`. The `..` row is
  never marked.
  `F2` commands, node actions (`M-n`) and `F5` between a container and
  a local panel work on the marked rows instead of the cursor row.
  `F8` always deletes the row under the cursor.
- `M-c` copies the kubectl command shown above the shell line.
- `M-y` and `M-u` go back and forward in the panel's history.
- `C-u` swaps the two panels.
- `M-w` changes the width of the active panel.
- `C-1` and `C-2` step the left or right panel to its next mode.

## Limits

- Letters, digits and `Space` go to the shell line below the panels,
  as in mc. So do `+`, `-` and `*` while the shell line holds text.
  Use `Esc` plus a letter where Option does not send Meta.
- Rows that a filter hides are unmarked.
- The sort belongs to the panel and follows it through navigation. The
  row filter belongs to the folder: it stays while you look at its
  objects and come back, and a new listing starts unfiltered.
- Marked rows are yellow in the default skin; `panel.skin` can change
  the colors.

## See also

- [Panel mode](panel-mode.md)
- [Panel options](panel-options.md)
- [Row filter](row-filter.md)
- [Context and namespace](context-namespace.md)
- [Hotlist](hotlist.md)
- [Panel history](history.md)
- [Menu bar](menu-bar.md)
- [Shell and terminal](shell.md)
- [Delete](delete.md)
- [Overview](overview.md)
