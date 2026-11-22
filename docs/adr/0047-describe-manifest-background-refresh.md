# 0047. Background loading and refresh for Describe and Manifest panels

- Status: Accepted
- Date: 2026-11-22
- Issue: #47

## Context

The Describe and Manifest panel modes filled their content only when the
selection moved. Describe ran `kubectl describe` on the Bubble Tea update
goroutine: every arrow key in the other panel waited for several API
requests. Nothing refreshed either mode when the shown object changed.

The issue asked for background loads that drop stale results, a loading
mark, refresh from the shared cache at most once a second, a pause while a
dialog is open, and one describe renderer per cluster. It left open how a
widget learns that its object changed, how results reach a widget, and what
happens to scroll position on a refresh.

Constraints: no ad-hoc watches (AGENTS.md), no I/O on the update goroutine,
`kubectl describe` takes no context.

## Decision

1. **One loader per widget** (`internal/ui/panelcontent/reload`). A new
   target starts a load at once. At most one load runs per widget; a newer
   selection waits for it and the older result is dropped by a generation
   counter. A refresh of the same target waits until one second after the
   previous load started. Refresh ticks re-arm while `models.Activity` is
   paused (a dialog is open). An inactive widget (another panel mode is shown)
   starts nothing and catches up when it is activated.
2. **Trigger: `FolderDirtyMsg`.** When either panel's folder reports a change
   from the shared informer cache, the app asks the active widget of both
   panels to refresh. Both, because the other panel follows this panel's
   selection, and a panel's own content may come from its hidden list.
3. **Skip unchanged objects.** Describe reads the object's `resourceVersion`
   from the shared cache first and skips the describe requests when it
   matches the shown output. Manifest reads from the cache anyway and
   compares a hash of the text. Unchanged content leaves the viewer alone.
4. **Results bypass routing.** Loader messages implement
   `panelcontent.BackgroundMsg`. The app hands them to every widget of both
   panels, also behind a dialog and with the full screen terminal shown, so
   a widget never waits for a result that was dropped on the way.
5. **Loading mark.** Until the first result for a new target arrives, the
   old content stays and the panel header shows `loading...`. Refreshes show
   no mark, so the header does not flicker once a second.
6. **Refresh keeps scroll.** A refresh of the same target keeps the scroll
   position and a committed search (`viewer.UpdateContent`). A new target
   starts at the top as before.
7. **Selection flow.** `SelectionListener.OnSelectionChanged` returns a
   `tea.Cmd`. Only the active widget is notified, and only through
   `Panel.NotifySelection` and `SetMode`. Selection changes of a panel's own
   hidden list no longer reach its Describe or Manifest widget; before, a
   refresh of that list replaced what the panel showed with its own row.
8. **Renderer per cluster.** The app keeps one describe renderer per cluster
   connection. `DescribeFunc` now runs on the update goroutine, takes the
   cluster and renderer, and returns functions that run in the background,
   so background code never reads `App` fields a context switch replaces.
   The REST config gets a 10 second timeout, because the describer takes no
   context and a hung request would block the widget's only load.

## Alternatives considered

- **Start a load for every selection without a limit.** It is simpler, but
  holding an arrow key would run dozens of describes at once, each with
  several API requests.
- **Describe on every folder change, without the version check.** A busy
  namespace changes its pod list all the time; a quiet pod would be described
  every second for nothing.
- **A watch per shown object.** This is forbidden by AGENTS.md and duplicates
  the informers the lists already run.
- **Route results through the normal panel update path.** That path skips
  inactive widgets, dialogs and the full screen terminal; a lost result would
  leave the loader waiting forever.
- **Show the mark on every refresh.** That would blink once a second.

## Consequences

- Describe output refreshes when the object itself changes. New Events alone
  do not change the object's `resourceVersion`, so they show on the next
  object change or selection. A follow-up could also watch the Events of the
  shown object through the shared cache.
- A failed refresh replaces good output with the error, for example when the
  object was deleted. This is honest but can flash a transient timeout.
- F3 still reads the manifest on the update goroutine; it opens once per key
  press and is out of scope here.
- Ctrl+1 / Ctrl+2 still show the panel's own selected row until the other
  panel's selection moves; only the mode dialog replays the other panel's
  selection.
