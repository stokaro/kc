# 0354. Resource type lists under the Work profile

- Status: Accepted; decision 5 and the note that quick search finds listed rows only are superseded by [0357](0357-resource-visibility-editor.md)
- Date: 2026-11-24
- Issue: #319

## Context

ADR 0319 decides which types a profile lists. This record decides how
kc's type lists apply it: the root, the resources of a namespace and
the cluster resources (`ResourcesFolder`). Before this change a type
list built a row for every discovered type and started a change
handler, a count and an emptiness peek for each one, and only then
hid rows (the non-empty filter). Filtering the rows after that point
would hide types but keep their watches.

Issue #319 also asks for a way to reach the types a profile leaves
out ("Other types (N)", "Browse all types") that is "UI navigation,
not a fabricated Kubernetes resource/path", and for a mark when the
user is inside such a type. Pull request #345 (issue #318) already
made every explicit jump reach a hidden type.

## Decision

1. **Membership before work.** `ResourcesFolder.finalize` evaluates
   the profile over the discovered types before it starts anything. A
   listed type gets its change handler, count and peek as before. A
   type left out gets a passive row item: no handler, no count, no
   peek, but explicit jumps (`ExplicitItemByID`, `TypeItem`) still
   enter it. The non-empty filter then applies to the listed types
   only.

2. **Row items stay one-way.** `Close` remains terminal. When a started
   type leaves the list, for example when the panel switches from All
   to Work, its item is closed and a new passive item takes its place;
   when a type comes back, it gets a new item too. No item is stopped
   and restarted.

3. **Other types is a row that opens a dialog.** The last row of a type
   list, `/other types (N)`, counts the types left out; N is a number
   of types. `Enter` on it opens a dialog (the scope picker) listing
   those types, filtered by name, kind, short name or group. "Browse
   all types..." in the Left and Right menu opens the same dialog with
   every discovered type and a note for each one, `Work` or `other`.
   Choosing a type is an explicit jump to the type list's steps plus
   the type's own row ID, so the path, the history and the hotlist
   name the type, never the dialog.

4. **The outside mark.** Inside a type the nearest type list above it
   leaves out, the top border of a List panel shows `outside Work`
   after the path, in the skin's namespace color. It takes at most
   half of the top border and is left out rather than cut.

5. **Work or All per panel.** `resources.visibility.defaultProfile`
   (`work`, the default, or `all`) is the profile of new panels. The
   panel options have a "Types" row that switches one panel for the
   session; `C-s` saves it as the default. A panel's folders read the
   profile from the panel's configuration, so a folder's first rows use
   it, and a panel swap carries it. Named profiles, Show/Hide/Only
   lists, context bindings and the editor are left for a later pull
   request; the key name is the one #319 proposes, so they extend it.

## Alternatives considered

- **Hide rows after counts start.** Smallest change, but every hidden
  type keeps an informer handler and a peek, which is the cost #319
  wants gone.
- **Restartable row items.** Stopping and restarting counts on the same
  item would need a reversible lifecycle and careful handling of
  callbacks in flight. A new item is simpler and is already what a
  rediscovered type gets.
- **Other types as a folder.** A folder would put a fake segment into
  paths, history and hotlist entries, and Back from a type would land
  in it. The dialog leaves the panel as it was.
- **Mark in the bottom border.** The bottom border already holds the
  context, namespace, filter and position; at 80 columns the mark did
  not fit. The top border holds only the path, which the mark
  qualifies.
- **Ship full profiles now.** The editor, named profiles and bindings
  are a large separate piece. Work as the new default needs only a way
  back to the full list, which All gives.

## Consequences

- Visible default change: without the key, kc lists Work. Users who
  want every type set `defaultProfile: all` or switch "Types" to All.
- A type outside Work costs nothing in a type list. Counts for a type
  start when it is listed, so switching to All starts them then.
- The dialog shows `other` rather than a reason per type: under Work
  every left-out type has the same reason. Per-type reasons come with
  Show and Hide.
- Quick search in a type list still searches the listed rows only.
