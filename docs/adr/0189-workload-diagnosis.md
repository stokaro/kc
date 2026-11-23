# 0189. Workload diagnosis

- Status: Accepted
- Date: 2026-11-23
- Issue: #189

## Context

Issue #189 asks for a read-only explanation of one workload's failure:
its conditions, the affected pods and containers, recent events and
blocked dependencies, each statement linked to its source and dated,
facts kept apart from possible explanations, logs of the exact container,
unknown sources shown as unknown, objects separated by UID, and no extra
cluster scan.

What the code had, checked on main:

- The overview (`overview_rules.go`, ADR 0051) computes namespace-wide
  findings from cluster-wide informers, one row per rule with a count. It is
  not a per-object model, and another lane is changing it (#171).
- `overview_workloads.go` groups pods by controller owner for the overview
  Info summaries, through a `ruleContext` built for the whole overview input.
- ADR 0194 added `/diagnosis` inside a Service: a `RelatedObjectsFolder`
  with kind `RelatedDiagnosis` that reads through the scoped reader of
  ADR 0193 (the per-type access decision of ADR 0056), watches what it reads
  and renders findings with the objects they cite.
- A pod's container folder offers `logs` and `logs (previous)` rows built
  from a `LogsSpec`; the log viewer opens any row that provides one.

## Decision

### 1. `/diagnosis` inside Pods, Deployments, ReplicaSets, StatefulSets, DaemonSets and Jobs

The same row as for a Service, added by `builtinObjectRows` to the child
folder of each workload and by `PodContainersFolder` to a pod. Entering it
builds the folder on the UI thread without reads; the first populate and
the watch start decide the scopes in the background (as in ADR 0194).
CronJobs are left out: their failures are the failures of their Jobs.

### 2. What is read, and how pods are matched

The diagnosis reads, from the shared cache, in the object's namespace: the
object, the pods (not for a pod), the ReplicaSets (for a Deployment), the
events and the PersistentVolumeClaims. It watches the same types, so a
change refreshes the folder. A workload's pods are those whose controller
owner reference has the workload's UID, or the UID of a ReplicaSet the
Deployment controls. Events are matched by `involvedObject.uid`, and by
`fieldPath` for one container. Matching by UID is what keeps a recreated
object with the same name from inheriting the pods or events of the one it
replaced.

### 3. Findings

Workload status: a Deployment whose `Progressing` condition is False (a
failed rollout), a `ReplicaFailure` condition, unavailable replicas
(`availableReplicas` of a Deployment, `readyReplicas` of a StatefulSet or
ReplicaSet, `numberUnavailable` of a DaemonSet, plus misscheduled DaemonSet
pods as info), a Job with a `Failed` condition, and warning events of a
ReplicaSet the Deployment controls (FailedCreate from a quota).

Per pod: `PodScheduled=False` with the scheduler's message; for each init
and main container the worst of an image pull failure, a configuration
error (`CreateContainerConfigError` and its kin), an OOM kill, a crash loop
or a restart in the last hour, and a non-zero exit; and every claim the
pod's volumes name (including generic ephemeral volumes) that is missing or
not Bound. A claim the pod does not name is not checked.

Each finding has a time: the condition's `lastTransitionTime`, the
container's `finishedAt`, the newest matching event, or the read time for a
count, printed as "observed <RFC3339>". Its sources follow it as rows: the
object with the exact status field and UID, and at most three newest
matching events (type, reason, message, count, last seen). A container
failure is followed by log rows for that container of that pod: the
current log, and the previous one when it restarted. Image pull and
configuration failures have no log.

Facts and explanations are separate rows. A `maybe` row holds a possible
cause, a correlation: when the workload reports unavailable replicas and
its pods have problems, the replica finding says that the pod problems may
explain it, and that kc relates them by owner without confirming the
cause. A `next` row says what to inspect. The note at the end says the
checks do not cover probes, nodes or the application.

When pods, ReplicaSets, events or claims cannot be read (for example
forbidden for the user), the finding that needs them is `incomplete` and
the source is a dim row with the reason; a workload whose pods are unknown
never reports "no problem".

### 4. Bounds

Up to ten pods with problems are explained, in name order; one more finding
counts the rest. Each finding cites at most three events.

## Deviations

- The issue asks to reuse the overview findings. The diagnosis reuses the
  overview's pod helpers (`podCondition`, `podTerminal`, `replicasOf`) and
  its waiting reasons, but not its rules: they count namespace-wide over a
  window, and the overview lane is changing them. A per-object rule set is
  smaller than adapting them, and leaves `overview_rules.go` and
  `folder_overview.go` untouched.
- The issue asks to open each source in the opposite panel. Source rows
  behave as in the other related folders (ADR 0193): Enter goes into the
  object's own folder in the same panel, F3 views it, and Back returns to
  the diagnosis with its selection. Log rows open the log viewer like the
  log rows of a container.
- Obsolete work: the folder is the unit of cancellation. Leaving it or
  switching the context closes it, its watches and its pending populate, so
  a stale result cannot reach another object's view. No new cancellation
  path was needed.
- "Restarts" counts only a restart in the last hour (or a crash loop now),
  fixed rather than tied to the overview window: an old restart of a
  container that runs now is history.

## Alternatives considered

- A key that opens the diagnosis of the selected row in the other panel.
  Rejected for now: the row inside the object is where ADR 0194 put the
  Service diagnosis, and a second entry point can be added later without
  changing the model.
- Listing events cluster-wide or per object with a field selector. Rejected:
  a field-selected list is a request per populate, outside the shared cache;
  the namespace's events are already in the cache the overview and the
  events list use.
- Aggregating the same problem across pods into one finding. Rejected: the
  log and event sources are per pod and container, and the issue asks not
  to choose another pod silently.

## Consequences

- One diagnosis model now covers Services and workloads; `diagFinding`
  gained `Observed`, `Logs`, `Hint` and `Next`, and the report carries its
  own note.
- Opening a workload's diagnosis starts namespace informers for pods,
  events and claims (and ReplicaSets) when they are not running yet; a busy
  namespace's events refresh the folder often.
- The UI-level claim that a log row opens the exact container is covered by
  unit tests of the rows' `LogsSpec`; a headless run on a cluster would add
  a screen-level check.
