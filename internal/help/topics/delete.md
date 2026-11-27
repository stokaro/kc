# Delete

`F8` deletes the marked rows, or the object under the cursor when no
row is marked, after a confirmation. kc deletes exactly the objects you
saw in the list, on the cluster the list came from. If an object was
replaced in the meantime, it is not deleted.

In `/port-forwards`, `F8` stops the selected port forward instead.

## How it works

For the row under the cursor the dialog asks `Delete pod shop/web-1?`
and shows the kubeconfig context under the title, so you see which
cluster you are about to change. It names the object as kubectl's
output does: the kind, with its API group unless it is a core kind
(`deployment.apps shop/web`), then namespace/name.

With marked rows (`Ins`, `+`, `*`, `C-a`; the bottom border shows
`3 marked`), `F8` works on them and never on the row under the cursor,
as in mc. `Ins` moves the cursor down after marking, so the cursor is
usually on a row you did not mark. The dialog asks `Delete 3 marked
configmaps?`, shows the context, and lists the first five objects, then
`and 2 more`. Objects of several kinds are listed with their kind.

The dialog is red, as every dialog that changes the cluster is. `No`
has the focus, so `Enter` right away keeps the objects. A letter alone
does not answer it: `y` typed by mistake does nothing.

When the dialog opens, kc records the UID of each object and the cluster
its row was read from. Each delete request carries its object's UID as
a precondition:

- If another client deleted the object and created a new one with the
  same name, the API server refuses. kc reports "was replaced by a new
  object with the same name; nothing was deleted". Select it again to
  delete the new one.
- If the object is already gone, kc reports "is already gone; nothing
  was deleted".
- A context switch while the dialog is open does not send the delete to
  the new cluster.

In both refused cases kc refreshes the panel and does not retry.

## Marked rows

- kc deletes the marked objects one after the other. A failed delete
  does not stop the others.
- A line at the bottom says what happened: `Deleted 3 configmaps`, or
  `Deleted 1 of 3 configmaps; not deleted: shop/ux-b (already gone),
  shop/ux-c (replaced by a new object)`. With `--v=1` or higher, the log
  in `~/.kc/debug.log` has each error in full.
- The rows of deleted objects lose their marks, and so do rows whose
  object was already gone or replaced: the new object of that name is
  not the one you marked. A row whose delete failed for another reason
  (forbidden, refused by an admission webhook, timed out) stays marked,
  so the list shows what is left.
- If one marked row cannot be deleted, `F8` deletes none of them and
  says which row is in the way: a row that is not an object (a `Scale...`
  or `logs` row), a row without a UID, or an object you may not delete.
  Unmark it (`Ins` on the row, or `-` with its name) and press `F8`
  again.
- To delete the row under the cursor instead, unmark all rows first:
  `-` and `Enter` (the pattern `*` is already filled in).

## Stopping a port forward

In `/port-forwards`, `F8` opens a red confirmation dialog, "Stop port
forward?", with the local address, target, pod, namespace and context.
Open connections through the forward are closed. Stopping a forward
does not change the cluster. With marked rows `F8` stops nothing and
asks you to unmark them first.

## Keys

- `F8` (or `Esc 8`) opens the dialog for the marked rows, or for the
  row under the cursor when none is marked.
- `M-y` deletes; `M-n` cancels. `y` and `n` alone do nothing.
- `Left`, `Right`, `Tab` or `Shift-Tab` move between Yes and No.
- `Enter` runs the focused button.
- `Esc` cancels.

## Limits

- `F8` is grayed out when the API server says you may not delete the
  object under the cursor, or none of the marked objects, and in
  read-only mode. When you may delete only some of the marked objects,
  `F8` names the others.
- While the current context is read-only, stopping a port forward is
  refused too, even one started in another context.
- A row without a UID cannot be deleted; kc asks you to refresh the
  panel instead of deleting by name.
- Rows in `/owners`, `/used-by` and `/requires` are read-only; delete
  the object from its own list.

## See also

- [Confirmation dialog](confirm.md)
- [Panels](panels.md)
- [Read-only mode](read-only.md)
- [Port forward](port-forward.md)
- [Access](access.md)
