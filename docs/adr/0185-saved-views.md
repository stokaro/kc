# 0185. Saved views

- Status: Accepted
- Date: 2026-11-23
- Issue: #185

## Context

The hotlist (`C-\`, `~/.kc/hotlist.yaml`) saves panel locations: context,
namespace scope, the row IDs entered from the root, and a path label. A jump
replays the steps and retries while informers sync. Recurring investigations
also depend on the list settings: columns mode, sort, namespace grouping
(ADR 0186), and the row filter (ADR 0184). The issue asks for named views that
restore these settings, plus panel modes and log options. Views should be
editable from the UI, be documented, keep the context explicit, and never store
captured data.

## Decision

1. **A view is a hotlist entry with a name and a `view` block.** `hotlist.Entry`
   embeds `navigation.Location`, so the location fields stay at the top level
   of each entry. Files written before this change load as plain bookmarks,
   and unknown fields are ignored. There is no second store and no second
   dialog.
2. **What a view stores:** `columns` (normal/wide), `order` (objects order
   syntax), `groupByNamespace` (only for lists that span namespaces), `filter`
   (the canonical filter text) and `faultsOnly`. These are settings only. An
   empty field leaves the panel's setting alone.
3. **Keys in the hotlist dialog:**
   - `Enter` goes to the entry and applies its view.
   - `Ins` adds a bookmark, as before.
   - `F2` asks for a name and saves the active panel as a view.
   - `F4` replaces the highlighted view's location and settings with the
     active panel's. It refuses a bookmark.
   - `F6` renames the entry.
   - `Del`/`F8` removes the entry.

   Names are unique, ignoring case. Browsing never writes to a view; only `F4`
   does.
4. **Restoring:**
   - The jump runs first. A missing context is reported, and no similar
     context is chosen.
   - If the path stops early (a resource type, namespace or object is gone),
     the panel stays at the last reached step, and the toast says the view
     settings were not applied.
   - Columns, sort and grouping become the panel's settings, the same as the
     panel options would set them. A sort column the list lacks falls back to
     name (ADR 0037).
   - The filter is parsed again. An invalid filter is left out with a warning.
     A filter that names a column this list lacks is kept and reported, so its
     meaning does not change silently.

## Deviations from the issue

- **No panel modes.** Describe, Manifest, Logs and Info show the other panel's
  selection. Restoring them without the cursor position (which a view
  should not store) would show some other object. A view restores the list.
- **No log options or time windows.** Log options belong to the Logs mode,
  which a view does not restore (see above). The log streams kc opens take
  `sinceSeconds`, which is relative already, so a later log view can store it
  as is and get a recalculated window on reopening. Absolute times are not
  stored anywhere.
- **No column order.** kc shows the server's column order. Hiding and adding
  columns comes from the columns mode and the configured custom columns. There
  is no per-list column order to save.
- **Settings apply to the panel, not only to the folder.** Columns, sort and
  grouping are per-panel settings in kc. A view sets them the way the panel
  options do, and they stay after leaving the view's list. The filter belongs
  to the folder, as before.

## Alternatives considered

- **A separate `views.yaml` and dialog.** It would duplicate the location
  format, the jump and the dialog, for entries that differ only by their
  settings.
- **Saving automatically on change.** Convenient, but a view would drift while
  someone browses. The issue asks for explicit edits.
- **Restoring the last cursor row.** It is useful for bookmarks of one object,
  but objects come and go. A cursor on a deleted pod is a stale snapshot, which
  the issue rules out.

## Consequences

- Hotlist entries now hold more than a location, so `hotlist.Load` and `Save`
  work on `hotlist.Entry`.
- Follow-ups: restoring the panel mode of the other panel together with a
  view, once there is a clear rule for which object it shows; and log options
  in views, once the Logs mode keeps its own settings per source.
