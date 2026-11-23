# 0264. Deployment rollout view

- Status: Accepted
- Date: 2026-11-23
- Issue: #264

## Context

kc can restart and roll back Deployments, record revisions in
`/timeline` and diagnose failing pods, but the pieces of a rollout are in
separate places: which ReplicaSet is which revision, which pods belong to
it, what changed in the template and whether the rollout is complete or
stalled. The issue also asks for a before/after comparison of CPU and
memory over chosen windows, built on the Prometheus history provider and
the investigation window that #253 and #255 build.

## Decision

1. A `/rollout` row inside every Deployment opens a read-only folder,
   built like `/diagnosis` (no reads on Enter, the per-type scope
   decision of [0056](0056-namespace-scoped-access.md), watches on the
   Deployment, ReplicaSets and pods of its namespace).

2. The summary says what `kubectl rollout status` would: complete, in
   progress with the count it waits for, stalled
   (`Progressing` reason `ProgressDeadlineExceeded`), paused, or waiting
   for the controller when `status.observedGeneration` is behind. It adds
   desired, updated, ready, available and unavailable replicas, and the
   strategy with maxSurge, maxUnavailable, progressDeadlineSeconds and
   revisionHistoryLimit. A stalled rollout links the pods of the current
   revision that are not ready, or the ReplicaSet when it has none (pods
   that were never created).

3. One group per ReplicaSet, newest revision first: revision number,
   current or old, replica counts, images, creation time, its pods with
   phase, readiness, restarts and waiting reasons, and a `changes` row
   that names what changed from the revision before (images, added and
   removed containers, and otherwise which part of the template). `F3`
   on it shows the unified diff of the two pod templates through the
   existing manifest diff, without the `pod-template-hash` label.

4. ReplicaSets are matched to the Deployment and pods to ReplicaSets by
   controller UID, never by name or labels. A ReplicaSet left by a
   deleted Deployment of the same name, or a pod of a replaced
   ReplicaSet, does not appear.

5. Investigation stays separate from mutation: the view has no restart
   or roll-back keys; the Command menu keeps them with their
   confirmations.

## Deviations from the issue

- **The usage comparison is deferred.** #255 (one investigation window
  for charts, logs and events) is not on main, and a second window
  picker here would be replaced by it. The issue's requirements for the
  comparison (explicit bounds, sample coverage, owner versus contributor
  counts, no attribution to a revision without per-revision series) need
  that shared window and the history provider's coverage data. Until
  then the help page points to the Info panel with the investigation
  window (`C-x w`) set before and after the rollout. A follow-up issue
  should add the comparison once #255 lands.
- Retained revisions beyond the cluster's ReplicaSets come from
  `/timeline`, which already records them while kc runs. The rollout
  view says so in its note instead of merging the two.
- Only `apps/v1` Deployments. StatefulSets and DaemonSets keep
  revisions in ControllerRevisions with other semantics.

## Consequences

- A new related-folder kind `RelatedRollout`, `rollout.go` in models and
  a help page. Diagnosis and rollout share the lazy folder constructor.
