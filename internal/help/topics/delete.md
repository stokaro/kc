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

## Namespaces, CRDs, PersistentVolumes and nodes

Deleting one of these deletes other objects with it, so the dialog says
what goes with it and asks you to type its name:

```
Delete namespace shop?
Context: kind-dev

It holds 5 deployments, 9 pods, 1 PVC and 1 Helm release.
Counted: deployments, statefulsets, daemonsets, cronjobs, jobs, pods,
services, ingresses, PVCs, configmaps, secrets, Helm releases. Other
kinds are not counted; everything in it is deleted.

Type "shop" to enable Yes:
[            ]
[ Yes ] [< No >]
```

- The dialog opens at once with `Counting what is in it…` and fills in
  when kc has counted. Only the kinds listed after `Counted:` are
  counted; everything else in the namespace is deleted too. A kind kc
  may not list is named: `Not counted: secrets (forbidden)`.
- For a CustomResourceDefinition kc counts its objects in all
  namespaces, for a PersistentVolume it names the claim bound to it and
  the reclaim policy, and for a node it counts the pods bound to it.
- `Yes` stays grayed until the input line holds the name exactly. Then
  `M-y` deletes, or `Tab` and `Enter`. `Enter` alone runs `No`, which
  keeps the focus while you type.
- kc counts with small requests for object metadata, each with a
  timeout; a pod count stops at 2000 and shows `2000+`.
- Deleting a namespace takes time: its objects go first, and the row
  shows `Terminating` meanwhile.

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
- Namespaces, CRDs, PersistentVolumes and nodes are deleted one at a
  time. With one of them marked, the dialog asks for its name as above;
  with more rows marked, `F8` asks you to unmark all but one.
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
- `M-y` deletes; `M-n` cancels. `y` and `n` alone do nothing; in the
  dialog that asks for a name they go to the input line.
- `Left`, `Right`, `Tab` or `Shift-Tab` move between Yes and No. A
  grayed Yes cannot take the focus.
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
