# 0058. Delete only the confirmed object instance

- Status: Accepted
- Date: 2026-11-22
- Issue: #58

## Context

F8 recorded the selected object's GVR, namespace and name, and on confirmation
sent a DELETE by name through the app's current cluster. Two things could go
wrong while the dialog was open or the request was queued:

- another client deletes the object and creates a new one with the same name,
  and kc deletes the new one;
- the app's current cluster changes (context switch), and kc sends the DELETE
  to a different cluster than the one the row came from.

The issue leaves open what identity to bind to, where the cluster comes from,
and what to do with rows that do not know their UID.

## Decision

1. Bind to the UID only. When the confirmation opens, kc records the row's
   UID, and the DELETE carries `Preconditions{UID: ...}`. The API server
   answers 409 Conflict when the name now belongs to another object and 404
   when nothing has the name. kc never re-reads the object to fill in a UID.
2. Use the row's origin cluster. Object rows record the `*cluster.Cluster`
   and context name of the folder that listed them (`models.ObjectOrigin`).
   The delete runs on that cluster, never on `App.cl`. The confirmation shows
   the context name under the title.
3. Refuse rows without identity. A row that has no UID or no origin cluster
   cannot be deleted; kc says so and asks for a refresh instead of deleting
   by name.
4. Report and refresh. A Conflict reads "... was replaced by a new object with
   the same name; nothing was deleted. Select it again to delete the new one".
   A NotFound reads "... is already gone; nothing was deleted". In both cases
   kc refreshes the panel and does not retry. A new delete needs a new
   selection and confirmation.

## Alternatives considered

- ResourceVersion precondition as well. It would also refuse when the same
  object changed (a Pod status update, a controller touching a label), which
  happens constantly. That blocks ordinary deletes without protecting the
  user from anything the UID does not already cover.
- Fetching the UID when the user confirms. That reads the replacement's UID
  and deletes it, which is exactly the bug. Fetching when the dialog opens
  would also put an API call on the UI goroutine.
- Resolving the cluster again from a stored `cluster.Key` through the pool.
  This works, but the folder's cluster is already the connection the user
  looked at, and the pool could build a fresh entry for a key the user no
  longer browses. Keeping the pointer is simpler. Deletes are direct REST
  calls, so they work even if the pool later stops that cluster's caches.
- Falling back to a delete by name for rows without UID. Rows from the
  Table API carry metadata (`includeObject=Metadata`) and rows from lists
  carry the object, so a missing UID points at a bug or an unusual server.
  Refusing is the safe answer there.

## Consequences

- F8 never deletes an object the user did not see. A user who wants to delete
  the replacement selects it again.
- Rows from servers that return Table rows without metadata cannot be deleted
  from kc until that is fixed.
- kc has no multi-selection delete today. When one is added, it should build a
  `deleteTarget` per row through `deleteTargetFor` and report each Conflict
  and NotFound separately.
- `models.InstanceItem` is the contract for future actions that must hit one
  instance (edit, restart, scale): they can reuse the UID and origin.
