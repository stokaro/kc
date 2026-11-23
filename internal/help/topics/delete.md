# Delete

`F8` deletes the object under the cursor, after a confirmation. kc
deletes exactly the object you saw in the list, on the cluster the list
came from. If the object was replaced in the meantime, nothing is
deleted.

In `/port-forwards`, `F8` stops the selected port forward instead.

## How it works

The dialog asks `Delete pods/web-1 in namespace "shop"?` and shows the
kubeconfig context under the title, so you see which cluster you are
about to change. `No` has the focus.

When the dialog opens, kc records the object's UID and the cluster the
row was read from. The delete request carries the UID as a
precondition:

- If another client deleted the object and created a new one with the
  same name, the API server refuses. kc reports "was replaced by a new
  object with the same name; nothing was deleted". Select it again to
  delete the new one.
- If the object is already gone, kc reports "is already gone; nothing
  was deleted".
- A context switch while the dialog is open does not send the delete to
  the new cluster.

In both refused cases kc refreshes the panel and does not retry.

## Stopping a port forward

In `/port-forwards`, `F8` opens a red confirmation dialog, "Stop port
forward?", with the local address, target, pod, namespace and context.
Open connections through the forward are closed. Stopping a forward
does not change the cluster.

## Keys

- `F8` (or `Esc 8`) opens the dialog for the row under the cursor.
- `y` deletes at once; `n` cancels.
- `Left`, `Right`, `Tab` or `Shift-Tab` move between Yes and No.
- `Enter` runs the focused button.
- `Esc` cancels.

## Limits

- `F8` deletes one object, the one under the cursor. Marked rows are not
  deleted together.
- `F8` is grayed out when the API server says you may not delete that
  object, and in read-only mode.
- While the current context is read-only, stopping a port forward is
  refused too, even one started in another context.
- A row without a UID cannot be deleted; kc asks you to refresh the
  panel instead of deleting by name.
- Rows in `/owners`, `/used-by` and `/requires` are read-only; delete
  the object from its own list.

## See also

- [Confirmation dialog](confirm.md)
- [Read-only mode](read-only.md)
- [Port forward](port-forward.md)
- [Access](access.md)
