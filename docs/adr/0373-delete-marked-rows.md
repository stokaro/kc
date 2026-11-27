# 0373. F8 deletes the marked rows

- Status: Accepted
- Date: 2026-11-27
- Issue: #373

## Context

mc users mark rows and press F8 to delete the marked set. kc's F8 deleted
only the row under the cursor, its dialog did not mention the marks, and a
plain `y` confirmed it. `Ins` moves the cursor down after marking, so the
cursor is usually on a row the user did not mark: marking `ux-a` and `ux-b`
and pressing F8 offered `ux-c`. Nothing on screen said how many rows were
marked.

The red-dialog rule of `docs/features.md` says a plain letter does not act
in a dialog that changes the cluster. The other red dialogs follow it;
`DeleteConfirmModel` did not.

The issue offers two fixes: delete the marked set, as mc does, or refuse F8
while rows are marked. It leaves open how the dialog names a set, what
happens when one object of the set cannot be deleted or fails, and what
happens to the marks afterwards. ADR 0058 binds a delete to the instance
the row showed, and asks a set delete to build one `deleteTarget` per row
and report each Conflict and NotFound. A stronger confirmation for
namespaces is #374.

## Decision

1. **What F8 takes.** In a list with marked rows, F8 works on the marked
   rows (`Panel.GetSelectedItems`, in list order) and never on the cursor
   row. With no marks it works on the cursor row as before. The function
   key, the Esc 8 fallback and the menu item all go through the same
   handler. In `/port-forwards`, marked rows make F8 refuse with "3 rows
   are marked; F8 stops one port forward. Unmark them first (-)".
2. **The count on screen.** While rows are marked, the status in the
   list's bottom border starts with their count: `3 marked • 4/9 • 100% •
   List`. It comes first so that a status cut in a narrow panel keeps it.
3. **The dialog.** One object keeps `Delete configmap shop/ux-c?`. A
   marked set asks `Delete 3 marked configmaps?`: the count, the resource
   as kubectl names it (`deployments.apps` for a group; the kind for one
   object; `objects` when the set holds several resources), the context,
   then the first five names (namespace/name, or with the kind for mixed
   sets) and `and N more`. A sixth name is listed rather than counted.
   Names wrap; they are never cut. Even one marked row uses this form, so
   the dialog says that the marks, not the cursor, decide.
4. **Keys.** The dialog follows the red-dialog rule: `y` and `n` alone do
   nothing; `M-y` and `M-n` pick the button whose letter is highlighted;
   `Enter` runs the focused button, which is `No` when the dialog opens;
   `Left`, `Right`, `Tab` and `Shift-Tab` move the focus; `Esc` cancels.
   The keys are those of `ConfirmModel`, the other red dialog, plus the
   Meta letters. The key bar drops `Enter Confirm`: Enter answers `No`
   until the focus moves.
5. **All or nothing before the dialog.** F8 refuses the whole set, before
   the dialog opens, when one marked row cannot be deleted, and names the
   rows in the way (at most three, then "and N more"):
   - a row that is not an object whose resource offers `delete` (a
     `Scale...` row, a `logs` row);
   - a row without UID or origin cluster (ADR 0058);
   - an object the cached access review says the identity may not delete
     ("M-k shows why").
   Read-only mode is checked for the context of every object, before the
   dialog and again on Yes. F8 stays grayed only when no marked row could
   be deleted: then there is no row to name.
6. **The delete.** On Yes, kc deletes the objects one after the other in
   one background command, each on the cluster its row was read from, with
   its own UID precondition and its own request timeout. A failure does
   not stop the others. When kc quits, the objects not tried yet report the
   canceled context.
7. **The result.** One line reports the set: an info line `Deleted 3
   configmaps`, or a red one, shown for 10 seconds, `Deleted 1 of 3
   configmaps; not deleted: shop/ux-b (already gone), shop/ux-c (replaced
   by a new object)`, naming at most three failures. Forbidden reads
   "forbidden"; other errors show their message. Each failure is also
   logged in full. The panel is refreshed.
8. **Marks afterwards.** The rows of objects that are gone lose their
   marks: deleted, already gone (404) and replaced (409). A replacement
   carries the name of a marked row, but it is not the object the user
   marked, and a second F8 must not take it. Rows that failed otherwise
   (forbidden, refused by a webhook, timed out) stay marked, so the list
   shows what is left. kc unmarks only while the list still holds the
   marks the dialog was opened on (`Widget.MarksGeneration`); after a
   change of folder, rows that only share an ID keep their marks.

## Alternatives considered

- Refuse F8 while rows are marked. Smaller, and safe, but it leaves the
  mc habit broken and makes the user unmark and delete one row at a time.
  The delete flow already binds each target to its instance and cluster
  (ADR 0058), so a set is a loop over the same targets.
- Skip the rows that cannot be deleted and delete the rest, listing the
  skipped ones in the dialog. The dialog would then name a set other than
  the one marked, and a partial delete reads like the bug this issue
  reports. Unmarking a row takes one key.
- Gray F8 when any marked object is denied, as for the cursor row. A
  grayed key does not say which of 40 marked rows is in the way.
- Delete in parallel. Faster for hundreds of objects, but it sends bursts
  to the API server and makes the result order depend on timing. A
  sequential delete of a few dozen objects takes well under a second on a
  healthy server; a slow admission webhook makes each object wait up to
  the request timeout.
- A result dialog listing every failure. More code for a case the marks
  already show: the rows that failed for a reason worth retrying stay
  marked, and the log has the full errors.
- Unmark every confirmed row at once. Simpler, but the rows that failed
  for a reason worth a retry would lose their marks.
- Keep `y` as a shortcut in the delete dialog only. It is the plain-letter
  confirmation the red-dialog rule exists to prevent; a `y` typed for the
  shell line deleted the cursor row.

## Consequences

- F8 with marks deletes what the user marked, and the dialog says what that
  is. Users who relied on F8 taking the cursor row while rows are marked
  must unmark first (`-` and `Enter`).
- Users who confirmed with `y` press `M-y`, or `Left` and `Enter`.
- The text of a set delete names objects by namespace/name. The stronger
  confirmation for namespaces (#374) can extend `DeleteConfirmModel`, for
  example with an input line, without changing how the set is built.
- A partial failure is reported in one line, which a narrow terminal cuts;
  the rows still marked and the log carry the rest.
