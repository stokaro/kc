# 0033. Scale, rollout restart and rollback for workloads

- Status: Accepted
- Date: 2026-11-22
- Issue: #33

## Context

kc could change the cluster only through F4 edit, F7 create namespace and
F8 delete. Issue #33 asks for the k9s workload actions: scale, rollout
restart, and rollback of a Deployment to an earlier ReplicaSet. It
proposes a built-in section in the F9 menu or the unused F6, a number
dialog for scale, the red confirmation dialog from #21 for the rest, and
graying out an action when the resource does not allow `patch`.

Constraints and open questions:

1. PR #80 (not merged) adds the F9 "Object Actions" section, a registry
   keyed by GVR (`internal/objectaction`), `models.ClusterAction`,
   `models.ActionEnv`, `models.ObjectRef` and a `Command` field on
   `models.Confirmation`. PR #97 (not merged) turns F9 into an mc menu
   bar. Nothing on main can show a built-in action in a menu yet.
2. Stacking on #80 is not allowed, and declaring the same names in
   `models` would break the build once #80 merges.
3. `models.ConfirmedAction.Confirm` runs on the Bubble Tea update
   goroutine. Scale, restart and rollback read the object and write to
   the API server, so both steps must run in a `tea.Cmd`.
4. Scale needs a value from the user and rollback a choice from a list;
   #80's ADR names "an input step before Prepare" as missing.
5. #107 (read-only mode) and #102 (access checks) will gate writes.

## Decision

### 1. Entry rows inside the workload

Entering a workload object shows, below its child types, one row per
action:

| Resource                 | Rows                                     |
|--------------------------|------------------------------------------|
| `apps/v1/deployments`    | `Scale...`, `Restart...`, `Roll back...` |
| `apps/v1/statefulsets`   | `Scale...`, `Restart...`                 |
| `apps/v1/daemonsets`     | `Restart...`                             |
| `apps/v1/replicasets`    | `Scale...`                               |

Enter on a row starts the action. This is how the podfs "Start a debug
helper..." row (#21) and the node shell row (#96) offer a change. It needs
no new key and no menu, so it does not collide with #80 or #97. Rows are
keyed by full GVR, so a CRD named `deployments` in another group gets none.

The hook into main is small: `models.RegisterObjectRows` lets a package
add rows to `ChildResourceTypesFolder`, `models.MessageRow` lets a row
send a message to the UI on Enter (one branch in the list widget), and
`models.KubectlCommand` exports the existing kubectl formatter.

### 2. A package shaped like #80's registry

The logic lives in `internal/workloadaction`. Its types mirror #80:
`Env` = `models.ActionEnv`, `Ref` = `models.ObjectRef`, `Change`
(`Confirmation()`, `Run(ctx) (Result, error)`) = `models.ClusterAction`,
`Action` (ID, Label, Verb, Prepare) = `objectaction.Action`. Prepare reads
the object and returns the change; it may block on the cache, so the UI
calls it in a `tea.Cmd`.

Two optional interfaces on the prepared change add the input step:

- `Asker` (`Question()`, `Answer(string) (Change, error)`): scale. The UI
  shows a text dialog prefilled with `Question().Initial`; `Answer`
  validates and returns the change for the typed value.
- `Chooser` (`Choices()`, `Choose(string) (Change, error)`): rollback. The
  UI shows the existing pick list (`ScopePickerModel`).

A prepared change is complete on its own (scale to the current count,
roll back to the previous revision), so a menu that skips the input step
still gets a valid change.

### 3. The UI flow

`internal/ui/workload_actions.go`: Prepare in a `tea.Cmd` with the busy
indicator, then the value dialog or the pick list, then the red dialog,
then Run in a `tea.Cmd`, then the result or the error on the command line.
The red dialog reuses `ConfirmModel` through a wrapper that answers with
its own message, so `confirm_modal.go` and the row confirmations stay
unchanged. One `Esc` cancels each step. Since `models.Confirmation` has no
`Command` field on main, the kubectl equivalent goes at the end of the
notes under "Same as:".

The single gate point for #107 and #102 is `App.runWorkloadChange`, before
`Run` is called.

### 4. Scale

Prepare reads the scale subresource (`GET .../scale`, a live request) for
the current count. Run sends a merge patch `{"spec":{"replicas":N}}` to
the scale subresource, which is what `kubectl scale` sends. There is no
precondition on the old count, as in kubectl without `--current-replicas`.
The dialog shows `old -> new`, warns when scaling to 0, says that an HPA
may change the count again, and, for a ReplicaSet with a controller, that
the controller sets the count back. Sending the patch twice changes
nothing.

### 5. Rollout restart

As `kubectl rollout restart`: the pod template annotation
`kubectl.kubernetes.io/restartedAt` set to the time in RFC 3339, as a
merge patch. The time is taken when the action is prepared, so the dialog
shows the value that is written, and a repeated Run writes the same
template and starts no second rollout. A paused Deployment is refused
with kubectl's message. A StatefulSet or DaemonSet with the `OnDelete`
strategy gets a note that its Pods restart only when deleted (kubectl
patches it without a warning).

### 6. Rollback

Prepare reads the Deployment and the ReplicaSets of its namespace from
the shared cache and keeps those the Deployment controls (owner UID) and
that carry a valid `deployment.kubernetes.io/revision`. The list shows
them newest first, the one whose template matches the Deployment's
(ignoring `pod-template-hash`) marked "current", the others with their
images. The newest non-current revision is preselected, which is what
`kubectl rollout undo` picks. Choosing the current revision is refused.

Run sends the JSON patch kubectl sends
(`k8s.io/kubectl/pkg/polymorphichelpers/rollback.go`): replace
`/spec/template` with the ReplicaSet's template without the
`pod-template-hash` label, and set `/metadata/annotations` to the
Deployment's revision, revision-history, desired/max-replicas and
last-applied annotations plus the ReplicaSet's other annotations. kc uses
`add` instead of `replace` for the annotations, which sets an existing
member the same way and also works on a Deployment without annotations. A
paused Deployment is refused, as in kubectl. The dialog shows from and to
revision, the target images and `kubectl rollout undo
deployments.apps/<name> --to-revision=<n>`.

### 7. No verb gating yet

The rows are always shown for the four built-in resources. Their
discovery verbs always include `patch`, so a discovery check, the way F4
checks `update`, would never hide anything. Whether the user may patch is
RBAC, which #102 checks. A missing permission shows as the API error
after confirming.

## Alternatives considered

- **F9 "Object Actions" section.** The right home, but it exists only in
  #80. Building a second menu section on main would conflict with #80
  and #97.
- **F6.** mc uses F6 for rename/move; kc keeps it for that, and one key
  cannot hold three actions without a menu.
- **Letter keys on a workload row (k9s `s`, `r`).** Typed letters go to
  the shell line, as in mc. Some Meta keys are still free, but three new
  chords for actions that move to F9 after #80 would be keys to unlearn,
  and the list of taken keys grows with every open PR. This change adds
  no key.
- **A revisions folder** (`/revisions` with one row per ReplicaSet). It
  would read the cache while the folder populates and needs a row type
  per revision; the pick list does the same with less code and keeps the
  flow in one place.
- **Update instead of patch for scale**, with the Scale's
  `resourceVersion`. It would fail whenever the workload's status changed
  between dialog and confirmation, which is common during a rollout.
- **A `test` operation in the rollback patch** that checks the template
  the user saw. Safer against a concurrent change, but kubectl does not
  do it and the dialog already shows the state kc read.
- **Typed reads through the client.** They would start typed informers
  next to the unstructured ones the panels use. kc reads unstructured
  from the shared cache and converts.

## Consequences

- Workloads can be scaled, restarted and rolled back without leaving kc,
  each change confirmed with its kubectl equivalent.
- The scale prefill is a live request; the restart and rollback reads
  come from the cache and may be a moment behind. The dialog shows what kc
  read, and that is what runs.
- The rows sit in the object's child folder, so they are one Enter away
  from the list, not on the list row itself.

Follow-up after #80 merges:

- Register the actions in `objectaction.Default()` through an adapter
  from `workloadaction.Action`, replace `Env`, `Ref` and `Result` with
  `models.ActionEnv`, `models.ObjectRef` and `models.ActionResult`, and
  move the kubectl line from the notes to `Confirmation.Command`.
- Give #80's UI the `Asker`/`Chooser` step, then delete
  `internal/ui/workload_actions.go`, `workloadaction.RegisterRows` and the
  rows. Keep or drop `models.RegisterObjectRows` and `models.MessageRow`
  depending on other users (the node shell row of #96 could use them).

Follow-up after #107 and #102 merge: call their write gate in
`App.runWorkloadChange` (and hide or gray the rows when the gate says no).
The read-only gate is in place since this change was merged onto main:
`runWorkloadChange` calls `refuseReadOnly` with a patch check on approval.
Hiding or graying the rows, and an RBAC decision from #102's cache, are
still open. The #80 registry move above is still open too: #80's UI has no
input step for `Asker` and `Chooser` yet.

Follow-up after #97: the actions appear in the menu bar through #80's
section; nothing extra.

Left out: a "Roll back the Deployment to this ReplicaSet" row inside a
ReplicaSet. The Deployment's revision list covers the same choice.
