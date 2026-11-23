# 0251. Date workload usage by its scoped PodMetrics

- Status: Accepted
- Date: 2026-11-23
- Issue: #251

## Context

In a one-namespace scope the overview reads three metrics.k8s.io polls:
NodeMetrics, PodMetrics of every namespace (for the capacity rows) and
PodMetrics of the namespace. The workload rows of Info sum the
namespace's PodMetrics (`m.pods`). The issue reports that their time
comes from the other two polls. Confirmed on main: `measuredUsage`
folds only the node and the all-namespaces snapshot into
`usageAt`/`usageReadAt` in the namespace branch, and
`workloadInfoSummary` copies those report-wide fields. As a result:

- workload usage is dated by samples it does not contain;
- `Widget.record` skips new workload measurements until the unrelated
  global timestamp moves, because a sample is added only for a later
  time;
- when NodeMetrics and cluster-wide PodMetrics are both denied (a
  namespace-only user) and the namespace's PodMetrics is readable, the
  times are zero and the workload history records nothing.

## Decision

1. `overviewMeasured` keeps `podsTiming` for the PodMetrics of the
   scope, whenever that snapshot is usable or stale (the same `timed`
   rule as the global times): each pod's own `timestamp`, the newest
   sample and its window, and when kc read the list. It is set in the
   all-namespaces branch too, where it is the same list.
2. `summarizeWorkloads` dates each workload by the **oldest** timestamp
   among the measured pods summed into it. The sum is then never shown
   as younger than any part of it, the same rule `addUsageTime` applies
   across sources.
3. `workloadInfoSummary` takes `UsageAt` from that per-workload time,
   falling back to the list's newest sample when the pods carry no
   timestamp, `UsageWindow` from the list's newest sample and
   `UsageReadAt` from the list's read. The report-wide `usageAt` keeps
   its combined policy for the cluster, node and scope summaries.

Measurement time, window and read time stay separate fields, as before.

## Alternatives considered

- Fold the scoped snapshot into the report-wide `usageAt`. That fixes
  the denied case but makes the capacity rows look as old as the
  oldest of three polls plus one more, and still dates a workload by
  pods of other workloads.
- The newest pod timestamp of the workload. Advances faster, but would
  show a sum as fresher than the pod with the oldest sample in it.
- Per-workload read time. All pods of a workload come from one list
  read, so the list's read time is exact.

## Consequences

- A workload's history now advances with its own pods and keeps
  recording when node and cluster metrics are denied.
- If one pod of a workload keeps an old sample while the others move,
  the workload time stays at that pod's time and no new history sample
  is recorded until it moves. metrics-server drops pods it cannot
  scrape, so this is rare, and a stale age is the honest display.
