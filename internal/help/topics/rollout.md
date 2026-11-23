# Deployment rollout

`/rollout` is a row inside a Deployment. It shows where a rollout
stands, which ReplicaSet is which revision, which pods belong to each,
and what changed in the pod template between revisions. Use it when a
rollout hangs or after a change, before reaching for restart or roll
back.

## How to read it

The summary rows come first:

- `rollout`: `complete`, `in progress` with what it waits for,
  `stalled` (the Progressing condition says `ProgressDeadlineExceeded`),
  `paused`, or `waiting for the controller` when the status is for an
  older generation of the spec. This is what `kubectl rollout status`
  checks.
- `replicas`: desired, updated, ready, available and unavailable.
- `strategy`: the update strategy with maxSurge and maxUnavailable,
  progressDeadlineSeconds and revisionHistoryLimit.

A stalled or paused rollout adds a finding with the condition's message
and time. A stalled one also lists the pods of the current revision that
are not ready, with their state; open one and its `/diagnosis` for the
containers, events and logs. A ReplicaSet or pod list you may not read
gives an `incomplete` finding.

Then one group per revision, newest first:

- the ReplicaSet, with its revision number, `current` (its template is
  the Deployment's) or `old`, desired, ready and available replicas,
  images and creation time,
- its pods, with phase, readiness, restarts and waiting reasons,
- a `changes` row: what changed from the revision before, such as
  `container app image web:1 -> web:2`, `annotations changed` (a
  restart) or `other pod spec fields changed`. `F3` on it shows the
  diff of the two pod templates.

ReplicaSets and pods are matched by controller UID, not by name or
labels: a ReplicaSet left from a deleted Deployment of the same name,
or a pod of a replaced ReplicaSet, is not shown.

## Limits

- Only the ReplicaSets the cluster still keeps are shown;
  revisionHistoryLimit removes older ones. Revisions kc recorded while
  it ran are in `/timeline`.
- Resource usage before and after a rollout is not compared here. Use
  the Info panel with the investigation window (`C-x w`) set before and
  after the rollout.
- Read-only: restart and roll back stay in the Command menu with their
  confirmations ([Workload actions](workload-actions.md)).

## See also

- [Diagnosis](diagnosis.md)
- [Object timeline](timeline.md)
- [Workload actions](workload-actions.md)
