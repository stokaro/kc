# 0374. Show what goes with a namespace and ask for its name

- Status: Accepted
- Date: 2026-11-28
- Issue: #374
- Extends: decisions 1, 3 and 4 of [0373](0373-delete-marked-rows.md) for
  namespaces, CustomResourceDefinitions, PersistentVolumes and nodes

## Context

Deleting a namespace deletes everything in it, but F8 asked with the same
Yes/No dialog as for a ConfigMap: `Delete namespace shop? / Context:
kind-dev`. `M-y` (since 0373) deleted five Deployments, nine pods, a PVC
and a Helm release. Deleting a CustomResourceDefinition deletes every object
of its kind, a PersistentVolume takes the volume from the claim bound to it,
and Kubernetes deletes the pods of a deleted node.

Create Namespace (F7 in `/namespaces`) ran at once from a gray input dialog
that did not name the context, although every other change to the cluster
asks in a red dialog that does.

The issue leaves open where the counts come from, what the dialog says while
kc counts, how the name is typed with the red-dialog keys of 0373, and what
F8 does with a marked set that holds a namespace.

## Decision

1. **Which objects.** Namespaces, CustomResourceDefinitions,
   PersistentVolumes and nodes, by group and resource (any version). For
   every other object the dialog stays as in 0373.
2. **The dialog.** Under the question and the context it says what goes
   with the object, then `Type "shop" to enable Yes:` and an input line.
   `Yes` is grayed until the input holds the name exactly: the same case,
   nothing before or after it. The keys follow 0373:
   - Letters, digits, `Backspace`, `Home`, `End`, `C-u` and a paste go to
     the input line. No letter answers the dialog.
   - `Tab`, `Shift-Tab`, `Left` and `Right` move the focus between the
     buttons; while `Yes` is grayed the focus stays on `No`. An edit that
     breaks the name moves the focus back to `No`.
   - `No` has the focus when the dialog opens; `Enter` runs the focused
     button. Typing the name and pressing `Enter` therefore keeps the
     object: `M-y`, or `Tab` and `Enter`, deletes it.
   - `M-y` does nothing until the name is typed. `M-n` and `Esc` cancel. A
     click on the grayed `Yes` does nothing.
3. **What kc counts.** The dialog opens at once with `Counting what is in
   it…` and fills in when the count arrives. The count runs in a background
   command, at most 15 seconds in all and 5 seconds per request, on the
   cluster the row was read from:
   - A namespace: deployments, statefulsets, daemonsets, cronjobs, jobs,
     pods, services, ingresses, PVCs, configmaps, secrets and Helm releases,
     a dozen requests sent together. The dialog lists the kinds with objects
     (`It holds 5 deployments, 9 pods, 1 PVC and 1 Helm release.`), then
     every kind it counted and that other kinds are deleted too, uncounted.
     A kind kc could not count is named with the reason: `Not counted:
     secrets, Helm releases (forbidden).`
   - A CustomResourceDefinition: its objects in all namespaces, listed in
     the storage version (or another served one).
   - A PersistentVolume: the claim bound to it, its phase and the reclaim
     policy, which decides whether the storage goes too.
   - A node: the pods bound to it in all namespaces (`spec.nodeName`).
4. **How kc counts.** Each count is a direct request for object metadata
   only (`PartialObjectMetadataList`), so the API server does not send the
   data of Secrets (an annotation such as kubectl's
   `last-applied-configuration` can still hold a copy; kc keeps only the
   count and Helm's `name` label). Each request is logged first. Without a selector kc asks for one object
   and adds the API server's `remainingItemCount`: one small request gives
   the exact count. Helm releases are Secrets labeled `owner=helm`, one per
   revision, counted by their `name` label. kc reads them, and the pods of
   a node, 500 at a time and at most 2000; a count that stops there shows
   a `+` (`2000+ pods`).
5. **Stale counts.** Each count carries a sequence number, and the dialog
   takes only the result of the count it waits for. `Yes`, `No` and `Esc`
   cancel the requests of a running count. A result that arrives after the
   dialog closed, or opened again for another object, is dropped. The
   window grows to the lines of the result.
6. **Marked rows.** One marked row of these kinds gets the same dialog, with
   the set's title of 0373 (`Delete 1 marked namespace?`); its mark goes
   after the delete as for any set. Two or more marked rows of which one is
   of these kinds are refused before the dialog opens: `Cannot delete 3
   marked namespaces together: each asks for its name to be typed. Unmark
   all but one`, or for a set that mixes kinds `Cannot delete the marked
   rows together: namespace shop asks for its name to be typed. Delete it
   alone`.
7. **Create Namespace.** The dialog is red and starts with `Context:
   kind-dev` and `Read-only: off`, the panel's context and its read-only
   mode. A read-only context still refuses F7 before the dialog opens. It
   stays one step: `Enter` in the input line creates the namespace, which
   is easy to delete again. As in the other red dialogs a button's letter
   needs Meta (`M-c`), also when a button has the focus.

## Alternatives considered

- Count from the informer cache, as the issue suggests "where kc has it".
  controller-runtime's cache has no read-only peek: `GetInformer` starts an
  informer for the kind that lists and watches every object of it in the
  cluster for the rest of the session, and kc's table cache works the same
  way. A count limited to the kinds some panel happened to open would vary
  with the session. A metadata list with `limit=1` costs one small request
  and is exact.
- Count every namespaced kind discovery lists. With CRDs that is dozens to
  hundreds of requests per dialog. The fixed set holds what users check;
  the dialog says that the rest goes too.
- Type the number of namespaces, or each name, to confirm a marked set. A
  number is a short string a stray key can match; a list of names to type
  makes a long dialog for a rare operation. Deleting several namespaces
  takes one F8 each, or kubectl.
- Let `Enter` delete once the name is typed, as some web consoles do. That
  breaks the rule of 0373 that `Enter` runs the focused button, `No` when
  the dialog opens, and makes the focus jump as a side effect of typing.
- Ask for the name only when the count found objects. The count can fail
  or arrive late, and an empty count of a dozen kinds does not mean an
  empty namespace.
- A two-step Create Namespace (input, then a red confirmation). Creating a
  namespace is easy to undo; naming the context in the input dialog shows
  where it goes without a second dialog.

## Consequences

- Deleting a namespace, a CRD, a PersistentVolume or a node takes typing
  its name; a PersistentVolume's name is often 40 characters long. Pasting
  it works.
- A namespace dialog sends about a dozen small list requests to the API
  server.
- Objects of kinds outside the fixed set (Flux HelmReleases, Argo CD
  Applications, Helm releases stored as ConfigMaps) are not counted; the
  dialog says so in general terms.
- The input line of the delete dialog is reusable for other objects whose
  delete takes others with it.
