# Hotlist and saved views

The hotlist (`C-\`, as in mc) keeps places you return to often: a pod
list in one namespace, a node, the Deployments of a team in another
cluster. A saved view is a named hotlist entry that also restores how
the list looks, such as "payments failures" with a filter and a sort.

## How to read it

Each entry shows its path and, in gray, `context : namespace`. A saved
view shows its name, then `view`, its path and the scope. An entry
remembers the context, the panel's namespace scope and the rows entered
from the panel root, keyed by group, version and resource.

A saved view also stores:

- the columns mode (normal or wide);
- the sort, for example `-mem`;
- "Group by namespace" for lists across namespaces;
- the row filter text and faults only.

## Keys

- `C-\` opens the hotlist. The title lists its keys.
- `Enter` or a click goes to the entry. An entry in another context
  switches the active panel there first, the other panel stays; a saved
  view then applies its settings.
- `Ins` adds the active panel's place as a bookmark.
- `F2` saves the active panel's place and settings as a named view. A
  dialog asks for the name ("Save view as"); `Enter` saves, `Esc`
  cancels. The new view shows in the hotlist at once, under the cursor.
- `F4` stores the active panel's place and settings in the highlighted
  view. Nothing else changes a view.
- `F6` renames the highlighted entry ("Rename to").
- `Del` or `F8` removes the highlighted entry.
- Typing filters the list; `Esc` clears the filter, then closes.

## How it works

- The hotlist is saved in `~/.kc/hotlist.yaml` (mode 0600) after every
  change. Your config file is not touched.
- A jump waits up to ten seconds for rows that are still loading.
- A jump opens a resource type the root hides as empty, at the exact
  group, version and resource it saved. A version or type the cluster
  no longer serves is reported; kc never opens another group or
  version of the same name instead.
- A view stores settings, never objects, so it shows the cluster as it
  is now. Columns, sort and grouping become the panel's settings.
- View names are unique, in any case.

## Limits

- kc does not guess. A missing context is reported and the panel stays.
  When the path stops early, the panel stays at the last step it reached
  and the view's settings are not applied.
- A saved filter is parsed again. An invalid one is left out with a
  warning; a term on a column the list lacks is kept, hides every row,
  and is reported.
- Views save object lists only; the panel must be in List mode.
- With the full-screen terminal shown (`C-o`), or while the shell line
  holds typed text, `C-\` goes to the shell.

## See also

- [Panel history](history.md)
- [Row filter](row-filter.md)
- [Panel options](panel-options.md)
- [Context and namespace](context-namespace.md)
- [Panels](panels.md)
