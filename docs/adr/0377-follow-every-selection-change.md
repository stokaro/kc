# 0377. A follower hears every move of the other panel's cursor

- Status: Accepted; decision 3 and the first consequence are amended by [0403](0403-logs-info-keep-their-object.md) (Logs and Info keep the object they show, and a follower's frame names where its object was read)
- Date: 2026-11-28
- Issue: #377
- Amends: decision 5 of
  [0047](0047-describe-manifest-background-refresh.md) (the old content
  stays while the next object loads)

## Context

A panel in Describe, Manifest, Logs or Info mode follows the row under
the other panel's cursor. The list widget reported a move through
`WidgetDeps.SelectionChanged`, which returned a command that posted
`PanelSelectionChangedMsg`. The cursor keys returned that command to
Bubble Tea. The other ways to move the cursor or replace the list went
through `Panel.SetFolder`, `RefreshFolder`, `ResetSelectionTop`,
`UseFolder` or `SelectByRowID`, which dropped the command or never built
one: a `cd` on the shell line, a namespace or context switch, the
history, the hotlist, a path jump, and a refresh that removed the row
under the cursor. The follower kept the old object, while its frame,
which `syncPanelScopeLabels` sets from the other panel's connection and
namespace, named the new location.

Three more things kept the old object on screen:

- A report went out only when the row ID changed, and `..` or an
  object's name repeats in another namespace or context.
- A follower told about a row without an object filled the gap:
  `Panel.completeSelection` took the row under the follower's own hidden
  list's cursor, or the last object it followed, and the Describe and
  Manifest widgets asked `WidgetDeps.SelectedItem`, the panel's own list,
  again.
- A describe without a connection read the active panel's connection, so
  after a context switch the content could come from one context while
  the frame named another.

The issue asks that every change that moves the cursor or replaces the
list reports the selection as a cursor key does, that a row without an
object clears the follower, and that the follower's frame and content
come from the same connection.

## Decision

1. **One place reports.** `App.Update` runs `App.reportMovedSelections`
   after every message. For each panel in List mode it compares a
   `selectionKey` (the panel's connection, namespace scope, path and the
   row ID under its cursor) with the key it reported last. When they
   differ it tells both panels through `Panel.NotifySelection`, as the
   old `PanelSelectionChangedMsg` handler did, and sets the scope labels
   in the same Update. Code that moves a cursor or replaces a list
   reports nothing itself; a new way to move the cursor is followed
   without more code.
2. **The key is more than the row ID.** The connection, scope and path
   tell `..` of one namespace from `..` of another, and `web` of one
   context from `web` of another.
3. **A row without an object clears a follower.** The report carries the
   item under the cursor: nil on `..`, on a folder row and in an empty
   list. A follower keeps what it is told, nil included, and never takes
   the row under its own hidden list's cursor, for what it shows or for
   what it acts on (`GetCurrentItem`, `selectedItemSource`). Describe and
   Manifest lose `WidgetDeps.SelectedItem` and show their hint; their
   header then shows the path of the followed panel.
4. **Content read from another connection does not stay.** Decision 5 of
   ADR 0047 keeps the old content while the next object loads. Describe
   and Manifest keep it only when the next object comes from the same
   connection. Otherwise they show `Loading...` at once, because the
   frame already names the new connection. Their content keys carry the
   connection (`panelcontent.SourceKey`), so results for the old
   connection or the old selection are dropped as before.
5. **`PanelSelectionChangedMsg` stays for explicit reports.** The list
   no longer posts it; `WidgetDeps.SelectionChanged` records the row and
   returns nil. A message that reports a selection other than the list's
   row stands until the list moves.
6. **Leaving the follower modes forgets the followed row.** A panel that
   returns to List or Command drops it, so the next follower mode starts
   from what the other panel reports then.

## Alternatives considered

- **Return or post the command from each caller.** About a dozen Panel
  and App paths move a cursor. Each would have to return the command,
  and the next new path would forget it, as these did.
- **Post the report from the Panel through its message poster.** Posted
  commands run at the start of the next Update, so the frame would
  change one message before the content.
- **Keep the row ID as the key.** It misses `..` and objects of the same
  name in another namespace or context, cases 1 and 2 of the issue.
- **Compare the item under the cursor instead of the key.** It catches
  the same moves but resolves the item after every message; the row ID
  and the key's other fields are at hand.
- **Keep the fallback to the follower's own list.** The hidden list is
  not what the panel follows; this fallback showed an object of one
  context under the label of another.

## Consequences

- `Enter` into a folder and a `cd` put the cursor on `..`: Describe and
  Manifest show their hint and Logs stops its stream until the cursor
  reaches an object.
- The hook reads the row under the cursor of each List panel after every
  message, as the access prefetch reads the active panel's row. It skips
  the reads while the full-screen terminal is shown; the first message
  after it closes reports what moved meanwhile.
- A report is not forced: a refresh that keeps the row under the cursor
  does not reload the follower. The refresh of ADR 0047 does that.
- A test that feeds a selection to a follower must put the row under the
  other panel's cursor, or send `PanelSelectionChangedMsg`; changing the
  list directly is reported by the next message.
