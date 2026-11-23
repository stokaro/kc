# 0254. Requests and limits as they were

- Status: Accepted
- Date: 2026-11-23
- Issue: #254

## Context

The dashboard (ADR 0252) draws the requests, limits and allocatable of
now as flat lines across the whole range. After a rollout that raised a
request, the chart claims the old pods ran against the new request. The
issue asks for built-in dashboards for workload and node bottlenecks and
says the first increment is CPU and memory with historical requests and
limits. Its acceptance criterion is that past requests and limits come
from historical samples, never today's spec drawn backward.

ADR 0197 deferred requests and limits history because it needs
`kube_pod_container_resource_*` joins.

## Decision

1. **Requests, limits and allocatable come from their own history.** A
   Prometheus usage read (`promhistory.Usage`) also reads
   `promhistory.Resources`: per step, the summed requests and limits of
   CPU and memory and how many containers set each, the number of
   containers, and the allocatable for a node or all namespaces.
   - A pod counts while kube-state-metrics reports it `Pending` or
     `Running`, like the live sums, which count pods that have not
     terminated. A Succeeded pod's requests never count.
   - The scope's pods are the ones the usage queries use: the same owner
     joins for a workload, `kube_pod_info` for a node, and every selector
     carries the backend's configured labels.
   - It takes two range queries. Each returns several series told apart
     by a `stat` label (`label_replace(sum by (resource) …) or
     label_replace(count by (resource) …)`), so a usage read is seven
     range queries instead of five, still at most three at a time.
   - A failed read, or two series for one statistic (two
     kube-state-metrics behind labels that do not single one out), leaves
     the usage intact. `Resources` is nil, and `ResourcesErr` says why.
2. **A limit is a ceiling only when every container has one.** At a step
   where fewer containers had a limit than there were containers,
   `FullLimits` gives zero and the line is not drawn there. That step is
   known, not missing, so it never takes over the value of the step
   before.
3. **The chart draws a reference line from points.** `chart.Ref` takes
   `Points` and `Hold`. A cell shows the newest point before its end,
   while that point is within `Hold` of the cell's start; otherwise the
   line has a gap. `Chart.RefAt` reads the value of a cell back. The
   cursor line shows it: `CPU 0.25 req 0.5 lim 1`.
4. **Without Prometheus, the session keeps the values it sampled.** Each
   Metrics API sample of the session history also records the requests,
   the limits (zero when one pod had none) and the allocatable of that
   moment. The session chart draws those.
5. **No history, no line.** When Prometheus has no kube-state-metrics
   container series for the range, the chart draws no request or limit
   line, and the title says `no req/lim history: no kube-state-metrics
   container series` (or the error of the read). The current values stay
   in the status line, labeled `now`.

## Proof

Besides the unit and fake Prometheus tests, the generated queries ran
once against Prometheus v3.5.0 loaded with a promtool backfill. The data
was a Deployment rolled from a ReplicaSet with 0.5 CPU requested to one
with 1 + 0.1 (a sidecar without a limit), a Succeeded pod requesting 5
cores, and a copy of the series under another `cluster` label. The
results:
- 0.5 cores before the rollout, 1.6 while both pods ran, and 1.1 after.
- One limited container of two after the rollout, so no limit is drawn.
- The Succeeded pod and the other cluster's series never counted.
- The allocatable showed for the node and all-namespaces scopes only.

The run is not part of CI, which has no Prometheus.

## Deviations from the issue

The issue's own scope makes this the first increment. The rest are
separate pieces of work, each needing metrics kc does not read yet:

- **Not in this change:** restarts and OOM history
  (`kube_pod_container_status_restarts_total`,
  `..._last_terminated_reason`), CPU throttling
  (`container_cpu_cfs_throttled_periods_total` over
  `container_cpu_cfs_periods_total`, kept apart from utilization), and
  replica readiness. They need charts of their own and their own
  coverage rules.
- **Node bottlenecks** (CPU modes, PSI, disk, filesystem, network) need
  node_exporter and a per-node hotspot view. That is a follow-up.
- **Service RED presets** need application, ingress or mesh metric
  mappings that kc has no configuration for. That is a follow-up.
- **Declared prerequisites:** the families this change reads are listed
  in the Metric sources help page. The `C-x m` probe does not check them
  yet.
- **The Info panel's Prometheus history** draws no request or limit
  lines, so it has nothing to correct. It is unchanged.
- **The history sums app containers.** It does not read
  `kube_pod_init_container_resource_*`, as kubernetes-mixin does not.
  The live sums in the status line are the effective pod values
  (`resourcehelper.PodRequests`): they include init containers, native
  sidecars, pod overhead and pod-level resources. For such pods the
  newest step can differ from the `now` value. Reading the sidecars
  needs their restart policy from kube-state-metrics, which older
  versions do not export.

## Consequences

- Each Prometheus refresh of the dashboard asks two more range queries.
  Their cost grows with the number of containers in the scope, like the
  usage queries.
- The dashboard needs kube-state-metrics 2 or later for these lines.
  Version 1 metric names (`..._requests_cpu_cores`) are not read.
- A request changed during the range now shows where it changed, both
  with Prometheus and in the session history.
