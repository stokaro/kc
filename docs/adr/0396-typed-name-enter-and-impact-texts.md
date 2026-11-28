# 0396. Enter after a typed name, Helm's Secrets, and what goes with a volume or node

- Status: Accepted
- Date: 2026-11-28
- PR: #396 (follow-up to #395, issue #374)
- Amends: decision 2 and, in decision 3, the secret count and the
  PersistentVolume and node lines of
  [0374](0374-delete-what-goes-with-it.md)

## Context

0374 shipped with #395. Its review found three problems in the delete
dialog that asks for an object's name:

- `No` has the focus, so typing `shop` and pressing `Enter` closed the
  dialog and kept the namespace. The typed name was lost, and the user had
  to open the dialog and type it again. Users expect `Enter` after the name
  to go on, as in web consoles.
- Helm keeps each revision of a release in a Secret labeled `owner=helm`.
  The namespace count listed those Secrets under `secrets` and again under
  `Helm releases`: one release with two revisions and one other Secret
  showed `3 secrets and 1 Helm release`.
- Two texts said more than Kubernetes does. A PersistentVolume did not
  "take the volume from the claim bound to it": the
  `kubernetes.io/pv-protection` finalizer keeps a bound volume until its
  claim is deleted. A node's pods are not "deleted with it": the pod
  garbage collector deletes them only if the node stays gone. The
  reclaim-policy line did not say when the storage goes.

The rule of 0373 stays: `Enter` runs the focused button and never deletes
unless `Yes` has the focus.

## Decision

1. **`Enter` after the typed name.** `No` has the focus when the dialog
   opens, and before the name matches `Enter` keeps the object, as before.
   - Once the input holds the name, the first `Enter` on `No` moves the
     focus to `Yes` and deletes nothing. A second `Enter` deletes.
   - If the user moved the focus since the name last matched (`Tab`,
     `Shift-Tab`, an arrow key, a click on a button), `Enter` runs the
     focused button, so `No` chosen on purpose keeps the object.
   - An edit that breaks the name moves the focus back to `No`, and the
     next match arms the first `Enter` again.
   - `M-y` deletes once the name matches, as before.

   The dialog keeps one flag, `focusChosen`: set by `Tab`, `Shift-Tab`,
   the arrow keys and a click on a button, cleared when the dialog opens
   and when the name stops matching. `Enter` moves the focus only while the
   name matches, `No` has the focus and the flag is clear. In every other
   case it runs the focused button, so it deletes only while `Yes` has the
   focus, as 0373 says.
2. **Helm's Secrets count once.** The secret count is the total minus the
   `owner=helm` Secrets (objects, not releases), never below zero: a
   revision written between the two lists could make it negative. The
   Helm release count is unchanged.

   When the difference would be a guess, the secrets keep their total and
   say so with `(including any of Helm's)`:
   - the Helm count failed: `7 secrets (including any of Helm's)`, with
     `Not counted: Helm releases (forbidden)`;
   - the Helm count stopped at 2000: `2600 secrets (including any of
     Helm's) and 300+ Helm releases`;
   - the secret count stopped at 2000 while Helm Secrets exist: `2000+
     secrets (including any of Helm's)`.

   With no Helm Secrets a stopped count stays `2000+ secrets`.
3. **A PersistentVolume.** The line about the claim follows the phase:
   - Bound: `Claim reports/data is bound to it (phase Bound); the volume
     stays until the claim is deleted.` The `kubernetes.io/pv-protection`
     finalizer keeps a bound volume, `Terminating`, until its claim is
     deleted.
   - Released: `It was bound to claim reports/data, which is gone (phase
     Released).`
   - Another phase with a claim: `It is reserved for claim shop/cache
     (phase Pending).`
   - No claim: `No claim is bound to it (phase Available).`

   The reclaim policy says when the storage goes:
   - Delete: `the storage is deleted once the volume is gone (clusters
     before Kubernetes 1.31 may leave it behind)`. Since 1.31
     `HonorPVReclaimPolicy` is on by default (GA in 1.33); before, a volume
     deleted before its claim could leave its storage behind, and F8 on a
     bound volume deletes it before its claim.
   - Retain: `the storage and its data stay after the volume is gone`.
   - Another policy is named without a claim about it.
4. **A node.** `3 pods are bound to it. If the node stays gone, Kubernetes
   deletes them; its kubelet registers the node again when it restarts.`
   - The pod garbage collector deletes the pods of a node that is gone for
     40 seconds (`quarantineTime` in `pkg/controller/podgc`).
   - A kubelet registers its node once, when it starts: nothing resets
     `registrationCompleted` in `pkg/kubelet/kubelet_node_status.go`. A
     kubelet that keeps running does not bring the node back; a restart
     does.
   - A failed pod count says only `kc could not count the pods on it
     (...)`, without promising that they go.

## Alternatives considered

- Keep `Enter` on `No` after the name is typed, so that only `M-y`, or
  `Tab` and `Enter`, deletes. That is 0374 as written: the dialog closed,
  kept the object, and the name had to be typed again.
- Let `Enter` delete as soon as the name matches. That breaks the rule of
  0373 that `Enter` runs the focused button: it would delete with `No`
  focused.
- Move the focus to `Yes` as soon as the name matches. Typing would move
  the focus as a side effect, and a stray `Enter` right after the last
  letter would delete with one key. With the chosen rule the first `Enter`
  only shows `Yes` focused.
- Move the focus to `Yes` on every first `Enter` on `No` after a match,
  however the focus got there. That needs no flag, but a user who moved
  the focus to `No` on purpose would see `Enter` move it to `Yes` instead
  of closing the dialog.
- Subtract the number of Helm releases instead of their Secrets. A
  release with five revisions would leave four of its Secrets in the
  secret count.
- Show a range (`at most 600 secrets`) when a count is incomplete. Two
  uncertain numbers are harder to read than the total with a note.
- Say that a deleted node comes back while its kubelet runs. The kubelet
  source and a run on kind (1.37) show that it does not: the node stayed
  gone for two minutes with its kubelet active, and came back seconds
  after a kubelet restart.

## Consequences

- Deleting a namespace, a CRD, a PersistentVolume or a node takes its name
  and two `Enter`s, or `M-y`.
- The secret count of a namespace does not match `kubectl get secrets`
  when it holds Helm releases; each revision is counted once, under Helm
  releases.
- The PersistentVolume and node lines describe what Kubernetes does after
  the delete, not only what is deleted at once, and are longer.
