# 0034. Cordon, uncordon and drain for nodes

- Status: Accepted
- Date: 2026-11-22
- Issue: #34

## Context

k9s cordons or uncordons a node with `u` and drains it with `r`, after a
dialog with the drain options. Issue #34 asks for the same in kc: the
actions in "the built-in action section proposed in #33", a drain dialog
with the grace period, "ignore DaemonSets" and "delete emptyDir data",
work on several selected nodes, the red confirmation, the result on the
command line, and cordoned nodes in the unhealthy row style.

Constraints and open questions:

1. The built-in action section does not exist on main. PR #80 (not merged)
   adds it: `internal/objectaction`, a registry keyed by GVR whose actions
   have `Prepare(ctx, env, obj)` returning a `models.ClusterAction` with
   `Confirmation()` and `Run(ctx)`, shown in the F9 menu. It works on one
   object and has no input step and no progress. #33 (scale, restart) is
   being built the same way at the same time. Stacked PRs are not allowed,
   so this change cannot use #80's code.
2. Where the actions are offered on main until then.
3. Whether to use `k8s.io/kubectl/pkg/drain` (already in `go.mod`, v0.37.2)
   or kc's own eviction loop.
4. Where the drain reads the pods of a node from.
5. The dialog defaults, and what progress and cancel look like.
6. Several nodes: order of cordon and drain.

## Decision

### 1. A package with #80's shape

`internal/nodeaction` holds the logic, with no UI code:

- `PrepareCordon(ctx, env, nodes, cordon)` and
  `PrepareDrain(ctx, env, nodes, opts)` read from the shared cache and
  return an action with `Confirmation() models.Confirmation`,
  `Command() string` (the kubectl equivalent) and `Run`.
- `Env` mirrors #80's `models.ActionEnv` (`Client`, `Context`,
  `Kubectl`) and adds `Pods`, the pod lister of a node.
- Actions take a list of nodes, because the issue asks for several.
- `CordonAction.Run(ctx) (Result, error)` has #80's `Run` shape.
  `DrainAction.Run(ctx, report func(Progress))` adds a progress callback,
  which #80's `ClusterAction` does not have.

### 2. On main: `M-n` on the node list

`M-n` (fallback `Esc n`) in a list of `v1/nodes` opens a small "Node
actions" menu: Cordon..., Uncordon..., Drain.... It acts on the marked
node rows, or the node under the cursor. On any other list the key goes
to the shell line as before. `M-n` is free on main and in every open PR
(checked against the taken key list and the PR diffs).

The hook into existing code is small: one key check and the `Esc n`
fallback in `app_input.go`, one `case nodeActionMsg` in `Update` and in
`passesModal`, `ForNodePods` and the `NodeActions()` target in
`internal/models`, and one row styler. All dialogs live in
`internal/ui/node_actions.go` and `internal/ui/node_action_dialogs.go`.
The red dialog reuses `ConfirmModel` through a wrapper that carries the
action, so `App.pendingConfirm` and `confirm_modal.go` (which #80
changes) stay untouched. Main's `models.Confirmation` has no `Command`
field yet, so the kubectl command is appended to the notes under
"Same as:".

The point where #107 (read-only) and #102 (access) gate the actions is
marked in `openNodeActions`. The actions are hidden when the node rows do
not offer the `patch` verb.

### 3. kc's own eviction loop, with kubectl's rules

`k8s.io/kubectl/pkg/drain.Helper` is not used, although it is in
`go.mod`:

- It needs a `kubernetes.Interface` and lists pods with a direct LIST on
  every call; AGENTS.md wants reads through the shared cache.
- It issues its requests internally, so kc cannot log an INFO line before
  each one, as AGENTS.md requires.
- On a 429 it sleeps with `time.Sleep`, so a cancel waits up to five
  seconds, and it reports refusals only as text on a writer, so "blocked
  by PDB" cannot be shown per pod.
- It cannot be tested with the controller-runtime fake client and
  interceptors that kc's other tests use.

`nodeaction.SelectPods` applies kubectl's filters in kubectl's order and
with kubectl's messages (`k8s.io/kubectl/pkg/drain/filters.go`): a
DaemonSet pod is skipped with `IgnoreDaemonSets` and refused without it
(an orphaned one needs `Force`); a mirror pod is skipped; a running pod
with an emptyDir volume needs `DeleteEmptyDirData`; a running pod without
a controller needs `Force`; finished pods are always evicted.
`SkipWaitForDeleteTimeoutSeconds`, `--disable-eviction`, `--selector`,
`--pod-selector` and dry run are not offered.

The eviction loop, per node: list the pods, select, evict every selected
pod that has no accepted eviction yet, then wait (one second, or five
after a refusal) and list again. A pod counts as evicted when it is gone
from the list. 429 (a PodDisruptionBudget) and "namespace is terminating"
are retried, as kubectl does; any other error stops the drain. Because
every round lists again, a pod that started before the cordon reached
the scheduler, or that the cache did not show yet, is evicted too.

### 4. Pods come from the node-scoped shared cache

`Deps.ForNodePods(node)` returns the selector-scoped cluster
(`spec.nodeName=<node>`) that the node's `/pods` folder already uses
(`docs/designs/node-pods-folder.md`); `NewNodePodsFolder` now calls it
too. The drain lists that cache as unstructured, the form the folder's
informer holds, so draining a node starts no cluster-wide pod watch and
shares the informer with the folder. With #85's reference-counted pool,
`ForNodePods` also returns a release; each pod list of the drain holds the
cluster only for that list, and the pool keeps it running for
`kubernetes.clusters.ttl` between rounds. The DaemonSet existence check and
the node reads go through the shared cache as well. Writes (node patch,
evictions) go to the API server and log an INFO line first.

### 5. Dialog defaults, refusal, progress and cancel

- Defaults: grace period -1 (each pod's own), timeout 5m per node,
  "Ignore DaemonSet pods" on, "Delete emptyDir data" and "Force" off.
  kubectl's defaults are no timeout and DaemonSets not ignored; the
  latter refuses nearly every real node (kube-proxy, CNI). The `kubectl
  drain` command in the red dialog names every flag that differs from
  kubectl's defaults, so it does the same as kc.
- `PrepareDrain` refuses up front, before anything changes, when a node
  has a pod the rules refuse; kubectl cordons first and fails after. The
  error names the first pod and the flag that would allow it.
- Uncordon removes `spec.unschedulable` (merge patch with `null`), as
  kubectl uncordon does; cordon sets it to `true`. Both name the value,
  so a repeat changes nothing. Nodes already in the target state are not
  patched; if all are, kc says so on the command line without a dialog.
- While a drain runs, a dialog shows per node its phase, pods evicted,
  pods remaining and pods refused, and lists up to five refused pods with
  the server's reason (the budget's message). `Esc` or Enter cancels it:
  the context is canceled, the loop stops before its next request, and
  the dialog stays until the drain has stopped and then shows the
  outcome. The nodes stay cordoned, as with kubectl. The drain runs on
  its own goroutine and reaches the UI through a channel that a `tea.Cmd`
  reads.

### 6. Several nodes

All nodes are cordoned first, then drained one after another, as
`kubectl drain a b` does. The timeout applies per node. The drain stops
at the first node that fails; later nodes stay cordoned and undrained.

### 7. Cordoned rows

A styler for `v1/nodes` shows a node with `spec.unschedulable: true` in
`NotReadyStyle`, the unhealthy style, as the issue asks.

## Alternatives considered

- **Wait for #80 and add the actions to the F9 menu.** Blocks the issue
  on another PR. The package is ready for it; see Consequences.
- **Add a node section to the F9 menu on main.** #80 rewrites the menu
  and the confirmation code; both PRs would conflict on the same lines.
- **Rows inside a node** (next to `/pods`, as #96 does for the node
  shell). They cannot act on several marked nodes, which the issue asks
  for, and drain needs a dialog before the confirmation, which a
  `ConfirmedAction` row does not have.
- **k9s letters (`u`, `r`).** Typed letters go to the shell line, as in
  mc.
- **`k8s.io/kubectl/pkg/drain`.** See decision 3.
- **A cluster-wide pod informer filtered by node, or a direct LIST.**
  The first watches every pod of the cluster; the second bypasses the
  shared cache.
- **kubectl's defaults in the dialog.** A drain refused for kube-proxy on
  every node is not a useful default; the dialog shows the switch.
- **A drain that keeps running in the background with the dialog
  closed.** Needs a place to show progress outside a dialog (a status
  line or the command line) and a way to reopen it; left for later.

## Consequences

Follow-up after #80 merges:

- In `internal/objectaction`, register three actions for
  `v1/nodes` (`nodeaction.NodesGVR`): `node.cordon`, `node.uncordon`
  (verb `patch`) and `node.drain`. Each `Prepare` builds a
  `nodeaction.Env` from `models.ActionEnv` and calls
  `nodeaction.PrepareCordon` or `PrepareDrain` with `[]string{obj.Name}`.
  The cordon action already has `Confirmation()` and
  `Run(ctx) (nodeaction.Result, error)`; wrap it to return
  `models.ActionResult{Message: res.Message}`.
- Set `Confirmation.Command` from `Command()` and drop the "Same as:"
  notes in `showNodeConfirm`.
- Drain needs two things the registry does not have: an input step
  before `Prepare` (the options dialog) and progress while `Run` runs.
  Until the registry grows them, the F9 entry for drain can open the
  existing `showDrainOptions` flow.
- Multi-node targets: #80's menu works on one object. Keep `M-n` for
  marked nodes, or extend the registry to several objects; then `M-n`
  can go.

Follow-up after #107 / #102: refuse or gray out the menu in
`openNodeActions` (marked "Gate point").
The read-only part is done since this change was merged onto main:
`openNodeActions` calls `refuseReadOnly` with a patch check on nodes before
the menu opens. Graying out by RBAC (#102's cache) is still open.

Follow-up after #96: the node shell row and these actions are unrelated
in code; nothing to change.

Other consequences:

- The drain loop is kc's code, so kubectl changes to the drain rules do
  not reach kc by a dependency bump. The rules and messages are copied
  with a pointer to kubectl's file, and the tests pin each rule.
- The nodes and pods are read from the cache; a change made a moment
  before may not be visible to `Prepare`. Run lists again every round,
  so the eviction loop itself does not rely on the first read.
- The drain dialog is modal: while a drain runs, kc's other keys wait
  until it is canceled or done.
- An API server can hold an eviction request for a while (seen on
  envtest when a budget has no status yet). Cancel aborts the request.
